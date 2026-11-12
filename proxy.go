package httproxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync/atomic"
	"time"
)

func ProxyHTTP(s *Server, w http.ResponseWriter, r *http.Request) {
	sessionID, ok := getProxySession(s, w, r)
	if !ok {
		return
	}

	noProxyTransport := http.DefaultTransport.(*http.Transport).Clone()
	noProxyTransport.Proxy = nil
	rp := &httputil.ReverseProxy{
		Rewrite:   func(pr *httputil.ProxyRequest) {},
		Transport: noProxyTransport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, fmt.Sprintf("%+v", err), http.StatusBadGateway)
		},
	}

	body := &countReader{r: r.Body}
	r.Body = body
	cw := &countWriter{w: w}
	rp.ServeHTTP(cw, r)

	addUsage(s, sessionID, body.n.Load()+cw.n.Load())
}

func Proxy(s *Server, w http.ResponseWriter, r *http.Request) {
	sessionID, ok := getProxySession(s, w, r)
	if !ok {
		return
	}

	target, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "cannot reach "+r.Host, http.StatusBadGateway)
		return
	}
	defer target.Close()

	// Begin proxy by taking over connection.
	conn, client, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "hijacking not supported", http.StatusBadRequest)
		return
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return
	}
	// Notify client we are ready.
	if _, err := client.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := client.Flush(); err != nil {
		return
	}

	type copyOut struct {
		n   int64
		err error
	}
	c := make(chan copyOut)
	go func() { n, err := io.Copy(target, client); c <- copyOut{n: n, err: err} }()
	go func() { n, err := io.Copy(client, target); c <- copyOut{n: n, err: err} }()
	co0 := <-c
	target.Close()
	// Unblock the other io.Copy without closing the client connection, so
	// that usage is recorded before the client observes the conn close.
	conn.SetDeadline(time.Now())
	co1 := <-c

	var usage int64
	usage += co0.n
	usage += co1.n
	addUsage(s, sessionID, usage)
}

func getProxySession(s *Server, w http.ResponseWriter, r *http.Request) (string, bool) {
	// Read request for session ID.
	ar := &http.Request{Header: http.Header{"Authorization": r.Header.Values("Proxy-Authorization")}}
	sessionID, _, _ := ar.BasicAuth()
	if sessionID == "" {
		w.Header().Set("Proxy-Authenticate", `Basic realm="please_enter_your_sessionID_as_username_and_leave_password_blank"`)
		http.Error(w, "proxy authentication required, please provide the session ID as the username", http.StatusProxyAuthRequired)
		return "", false
	}

	// Get session by ID from database.
	var ss Session
	var err error
	if strings.HasPrefix(sessionID, voucherPrefix) {
		ss, err = s.vouchers.get(sessionID)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = s.DB.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM session WHERE id=?", sessionID).Scan(&ss.ID, &ss.UsageLimit, &ss.Usage)
	}
	if err != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="payment_required"`)
		http.Error(w, "payment required", http.StatusProxyAuthRequired)
		return "", false
	}

	// Check usage limits.
	if ss.Usage > ss.UsageLimit {
		w.Header().Set("Proxy-Authenticate", `Basic realm="payment_required_usage_exceeds_limit_please_top_up"`)
		http.Error(w, fmt.Sprintf("payment required, usage %d exceeds limit %d bytes, please top up", ss.Usage, ss.UsageLimit), http.StatusProxyAuthRequired)
		return "", false
	}
	return sessionID, true
}

func addUsage(s *Server, sessionID string, usage int64) {
	var err error
	if strings.HasPrefix(sessionID, voucherPrefix) {
		s.vouchers.use(sessionID, usage)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err = s.DB.ExecContext(ctx, "UPDATE session SET usage=usage+? WHERE id=?", usage, sessionID)
	}
	if err != nil {
		log.Printf("update usage error for session %s usage %d: %s", sessionID, usage, err)
	}
}

type countReader struct {
	r io.ReadCloser
	n atomic.Int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countReader) Close() error { return c.r.Close() }

type countWriter struct {
	w http.ResponseWriter
	n atomic.Int64
}

func (c *countWriter) Header() http.Header { return c.w.Header() }

func (c *countWriter) WriteHeader(code int) { c.w.WriteHeader(code) }

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countWriter) Flush() { http.NewResponseController(c.w).Flush() }
