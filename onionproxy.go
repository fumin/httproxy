package httproxy

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"text/template"
	"time"

	"github.com/fumin/httproxy/config"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	C config.Config

	ServeMux *http.ServeMux
	Server   http.Server
}

func NewServer(cfg config.Config) (*Server, error) {
	s := &Server{C: cfg}

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
