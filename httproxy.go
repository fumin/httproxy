package httproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/fumin/httproxy/config"
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
	C    config.Config
	Root *os.Root
	DB   *sql.DB

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
	sqliteURI := filepath.Join(s.C.Root, "sqlite.db")
	connInitFunc := func(ctx context.Context, conn driver.ConnPrepareContext) error {
		return sqlite.ExecScript(conn.(sqlite.SQLConn), "PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;")
	}
	s.DB = sql.OpenDB(sqlite.Connector(sqliteURI, connInitFunc, nil))
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

func handleJSON(s *Server, httpPath string, fn func(*Server, http.ResponseWriter, *http.Request) (any, error)) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		res, err := fn(s, w, r)
		if err != nil {
			msg := struct {
				Error struct {
					Msg string
				}
			}{}
			msg.Error.Msg = fmt.Sprintf("%+v", err)
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(msg)
			return
		}
		if err := json.NewEncoder(w).Encode(res); err != nil {
			log.Printf("%+v", err)
			return
		}
	}
	s.ServeMux.HandleFunc(httpPath, handler)
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
