package httproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"text/template"
	"time"

	"github.com/fumin/httproxy/client"
	"github.com/fumin/httproxy/config"
	"github.com/fumin/httproxy/util"
	"github.com/fumin/httproxy/util/jsonrpc"
	"github.com/fumin/httproxy/util/monero"
	"github.com/pkg/errors"
	"github.com/tailscale/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS session (
	id TEXT PRIMARY KEY,
	usageLimit INTEGER NOT NULL,
	usage INTEGER NOT NULL,
	createAt INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS payment (
	method TEXT NOT NULL,
	tx TEXT NOT NULL,
	session TEXT NOT NULL,
	PRIMARY KEY (method, tx)
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

	ServeMux *http.ServeMux
	Server   http.Server
}

func NewServer(cfg config.Config) (*Server, error) {
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

	// Setup database.
	connInitFunc := func(ctx context.Context, conn driver.ConnPrepareContext) error {
		return sqlite.ExecScript(conn.(sqlite.SQLConn), "PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;")
	}
	s.DB = sql.OpenDB(sqlite.Connector(s.C.DB, connInitFunc, nil))
	if err := s.DB.Ping(); err != nil {
		return nil, errors.Wrap(err, "")
	}
	conn, err := s.DB.Conn(context.Background())
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	err = sqlite.ExecScript(conn, schema)
	conn.Close()
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	s.XMR = monero.NewWallet(s.C.XMR)
	addrOut, err := s.XMR.GetAddress(monero.GetAddressIn{AddressIndex: []int{0}})
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	s.XMRAddr = addrOut.Address

	// Setup HTTP server.
	s.ServeMux = http.NewServeMux()
	s.Server.Addr = s.C.Addr
	s.Server.ReadHeaderTimeout = 10 * time.Second
	s.Server.ReadTimeout = 30 * time.Second
	s.Server.WriteTimeout = 30 * time.Second
	s.Server.IdleTimeout = 30 * time.Second
	s.Server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			Proxy(s, w, r)
			return
		}
		s.ServeMux.ServeHTTP(w, r)
	})
	s.ServeMux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	methods := map[string]jsonrpc.MethodParams{
		"topup": m(s, topup, client.TopupIn{}),
		"usage": m(s, usage, client.UsageIn{}),
	}
	s.ServeMux.HandleFunc("/json_rpc", jsonrpc.NewHandler(methods))
	handleFunc(s, "/{$}", Index)

	return s, nil
}

func (s *Server) Close() error {
	s.DB.Close()
	return nil
}

func handleFunc(s *Server, httpPath string, fn func(*Server, http.ResponseWriter, *http.Request)) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		fn(s, w, r)
	}
	s.ServeMux.HandleFunc(httpPath, handler)
}

func m[Params, Result any](s *Server, method func(s *Server, params Params) (Result, error), paramsTmpl Params) jsonrpc.MethodParams {
	m := func(params any) (any, error) { return method(s, params.(Params)) }
	return jsonrpc.MethodParams{Method: m, Params: paramsTmpl}
}

//go:embed tmpl/index.html
var indexHTML string
var indexTmpl = template.Must(template.New("").Parse(indexHTML))

func Index(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
	}{}
	if err := indexTmpl.Execute(w, page); err != nil {
		log.Printf("%+v", err)
	}
}

func Proxy(s *Server, w http.ResponseWriter, r *http.Request) {
	conn, client, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "hijacking not supported", http.StatusBadRequest)
		return
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}

	target, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "cannot reach "+r.Host, http.StatusBadGateway)
		return
	}
	defer target.Close()

	if _, err := client.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := client.Flush(); err != nil {
		return
	}

	errc := make(chan error)
	go func() { _, err := io.Copy(target, client); errc <- err }()
	go func() { _, err := io.Copy(client, target); errc <- err }()
	<-errc
	conn.Close()
	target.Close()
	<-errc
}

func topup(s *Server, in client.TopupIn) (out Session, err error) {
	received, err := xmrReceived(s.XMR, s.XMRAddr, in.PaymentTx)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	usageLimit := time.Duration(received/client.ProxyPriceXMRPiconeroPerSecond) * time.Second

	newSession := false
	sessionID := in.SessionID
	if sessionID == "" {
		newSession = true
		sessionID = util.RandStr()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "INSERT INTO payment (method, tx, session) VALUES (?, ?, ?)", in.Method, in.PaymentTx, sessionID); err != nil {
		return out, errors.Wrap(err, "")
	}
	if newSession {
		if _, err := tx.ExecContext(ctx, "INSERT INTO session (id, usageLimit, usage, createAt) VALUES (?, ?, ?, ?)", sessionID, usageLimit.Seconds(), 0, time.Now().Unix()); err != nil {
			return out, errors.Wrap(err, "")
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE session SET usageLimit=usageLimit+? WHERE id=?", usageLimit.Seconds(), sessionID); err != nil {
			return out, errors.Wrap(err, "")
		}
	}
	out.ID = sessionID
	out.ByID(ctx, tx)

	if err := tx.Commit(); err != nil {
		return out, errors.Wrap(err, "")
	}

	return
}

func usage(s *Server, in client.UsageIn) (out Session, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out.ID = in.SessionID
	if err := out.ByID(ctx, s.DB); err != nil {
		return out, errors.Wrap(err, "")
	}
	return
}
