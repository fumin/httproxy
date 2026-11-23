package httproxy

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"time"

	"github.com/fumin/httproxy/api"
	"github.com/fumin/httproxy/auth"
	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/util/gvisor"
	"github.com/fumin/httproxy/util/jsonrpc"
	"github.com/fumin/httproxy/util/monero"
	"github.com/pkg/errors"
	"github.com/tailscale/sqlite"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

const (
	serviceProxy = "p"
	serviceVM    = "v"

	cookieNameVMID = "vmid"

	PathVMSetID     = "/VMSetID"
	PathVMLoginPage = "/VMLoginPage"
	PathVMLogin     = "/VMLogin"
	PathVMDoc       = "/VMDoc"
	PathProxyDoc    = "/ProxyDoc"
)

const schema = `
CREATE TABLE IF NOT EXISTS payment (
	method TEXT NOT NULL,
	tx TEXT NOT NULL,
	service TEXT NOT NULL,
	serviceID TEXT NOT NULL,
	PRIMARY KEY (method, tx)
) STRICT;

CREATE TABLE IF NOT EXISTS proxy_session (
	id TEXT PRIMARY KEY,
	usageLimit INTEGER NOT NULL,
	usage INTEGER NOT NULL,
	createAt INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS vm (
	id TEXT PRIMARY KEY,
	usageLimit INTEGER NOT NULL,
	usage INTEGER NOT NULL,
	createAt INTEGER NOT NULL
) STRICT;
`

//go:embed static
var staticFS embed.FS

type Server struct {
	C       config.Config
	Root    *os.Root
	DB      *sql.DB
	XMR     *monero.Wallet
	XMRAddr string

	auth      *auth.Authorizer
	vouchers  *voucherDealer
	vmService *vmService

	ServeMux *http.ServeMux
	Server   http.Server
}

func NewServer(ctx context.Context, cfg config.Config) (*Server, error) {
	s := &Server{C: cfg}

	// Setup data folder.
	if err := os.MkdirAll(cfg.Root, 0700); err != nil {
		return nil, errors.Wrap(err, "")
	}
	var err error
	s.Root, err = os.OpenRoot(s.C.Root)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	s.DB, err = openDB(ctx, s.C.DB)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	// XMR wallet.
	s.XMR = monero.NewWallet(s.C.XMR)
	addrOut, err := s.XMR.GetAddress(monero.GetAddressIn{AddressIndex: []int{0}})
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	s.XMRAddr = addrOut.Address

	// Miscellaneous.
	s.auth = auth.NewAuthorizer()
	s.vouchers = newVoucherDealer()
	vmCfg := gvisor.DefaultConfig(s.C.VMRoot)
	vmCfg.MemoryMax = s.C.VMRam
	s.vmService, err = newVMService(ctx, s.DB, s.C.VMMaxNum, vmCfg, s.C.VMUsageGranularity)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	// Setup HTTP server.
	s.ServeMux = http.NewServeMux()
	s.Server.Addr = s.C.Addr
	s.Server.ReadHeaderTimeout = 10 * time.Second
	s.Server.ReadTimeout = 30 * time.Second
	s.Server.WriteTimeout = 30 * time.Second
	s.Server.IdleTimeout = 30 * time.Second
	s.Server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodConnect:
			Proxy(s, w, r)
		case r.URL.IsAbs():
			ProxyHTTP(s, w, r)
		default:
			s.ServeMux.ServeHTTP(w, r)
		}
	})
	handleAdmin(s)
	s.ServeMux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	methods := map[string]jsonrpc.MethodParams{
		"ProxyTopup": m(s, ProxyTopup, api.ProxyTopupIn{}),
		"ProxyUsage": m(s, ProxyUsage, api.ProxyUsageIn{}),
		"VMTopup":    m(s, VMTopup, api.VMTopupIn{}),
		"VMInfo":     m(s, VMInfo, api.VMInfoIn{}),
	}
	s.ServeMux.HandleFunc("/json_rpc", jsonrpc.NewHandler(methods, nil))
	handleFunc(s, PathVMSetID, VMSetID)
	handleFunc(s, PathVMLoginPage, VMLoginPage)
	handleFunc(s, PathVMLogin, VMLogin)
	handleFunc(s, PathVMDoc, VMDoc)
	handleFunc(s, PathProxyDoc, ProxyDoc)
	handleFunc(s, "/{$}", Index)

	return s, nil
}

func (s *Server) Close() error {
	s.vmService.Close()
	s.vouchers.Close()
	s.DB.Close()
	return nil
}

func handleFunc(s *Server, httpPath string, fn func(*Server, http.ResponseWriter, *http.Request)) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		fn(s, w, r)
	}
	s.ServeMux.HandleFunc(httpPath, handler)
}

func m[Params, Result any](s *Server, method func(ctx context.Context, s *Server, params Params) (Result, error), paramsTmpl Params) jsonrpc.MethodParams {
	m := func(ctx context.Context, params any) (any, error) {
		result, err := method(ctx, s, params.(Params))
		if err != nil {
			return nil, errors.Errorf("%+v", err)
		}
		return result, nil
	}
	return jsonrpc.MethodParams{Method: m, Params: paramsTmpl}
}

func mustRenderMarkdown(src []byte) template.HTML {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	return template.HTML(buf.String())
}

func openDB(ctx context.Context, sqliteURI string) (*sql.DB, error) {
	connInitFunc := func(ctx context.Context, conn driver.ConnPrepareContext) error {
		return sqlite.ExecScript(conn.(sqlite.SQLConn), `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA encoding="UTF-8";`)
	}
	db := sql.OpenDB(sqlite.Connector(sqliteURI, connInitFunc, nil))
	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	defer conn.Close()
	err = sqlite.ExecScript(conn, schema)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	return db, nil
}

//go:embed tmpl/navbar.html
var navbarHTML string
var navbarTmpl = template.Must(template.New("").Parse(navbarHTML))

func commonTmpl() *template.Template {
	return template.Must(navbarTmpl.Clone())
}

type pageCommon struct {
	Navbar struct {
		PathIndex string
	}
}

func getPageCommon(s *Server, r *http.Request) pageCommon {
	c := pageCommon{}
	c.Navbar.PathIndex = "/"
	return c
}

//go:embed tmpl/index.html
var indexHTML string
var indexTmpl = template.Must(commonTmpl().Parse(indexHTML))

func Index(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
		Common   pageCommon
		VMDoc    string
		ProxyDoc string
	}{
		Common:   getPageCommon(s, r),
		VMDoc:    PathVMDoc,
		ProxyDoc: PathProxyDoc,
	}
	indexTmpl.Execute(w, page)
}
