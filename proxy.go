package httproxy

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fumin/httproxy/api"
	"github.com/fumin/httproxy/util"
	"github.com/pkg/errors"
)

func ProxyTopup(ctx context.Context, s *Server, in api.ProxyTopupIn) (out Session, err error) {
	received, err := xmrReceived(s.XMR, s.XMRAddr, in.PaymentTx)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	usageLimit := received / api.ProxyPriceXMRPicoPerByte

	newSession := false
	sessionID := in.SessionID
	if sessionID == "" {
		newSession = true
		sessionID = util.RandStr()
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, errors.Wrap(err, "")
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "INSERT INTO payment (method, tx, service, serviceID) VALUES (?, ?, ?, ?)", in.Method, in.PaymentTx, serviceProxy, sessionID); err != nil {
		return out, errors.Wrap(err, "")
	}
	if newSession {
		if _, err := tx.ExecContext(ctx, "INSERT INTO proxy_session (id, usageLimit, usage, createAt) VALUES (?, ?, ?, ?)", sessionID, usageLimit, 0, time.Now().Unix()); err != nil {
			return out, errors.Wrap(err, "")
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE proxy_session SET usageLimit=usageLimit+? WHERE id=?", usageLimit, sessionID); err != nil {
			return out, errors.Wrap(err, "")
		}
	}
	if err := tx.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM proxy_session WHERE id=?", sessionID).Scan(&out.ID, &out.UsageLimit, &out.Usage); err != nil {
		return out, errors.Wrap(err, "")
	}

	if err := tx.Commit(); err != nil {
		return out, errors.Wrap(err, "")
	}

	return
}

func ProxyUsage(ctx context.Context, s *Server, in api.ProxyUsageIn) (out Session, err error) {
	if err := s.DB.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM proxy_session WHERE id=?", in.SessionID).Scan(&out.ID, &out.UsageLimit, &out.Usage); err != nil {
		return out, errors.Wrap(err, "")
	}
	return
}

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

	addUsage(r.Context(), s, sessionID, body.n.Load()+cw.n.Load())
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
	conn.SetDeadline(time.Now())
	co1 := <-c

	var usage int64
	usage += co0.n
	usage += co1.n
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addUsage(ctx, s, sessionID, usage)
}

//go:embed tmpl/proxydoc/proxydoc.html
var proxyDocHTML string
var proxyDocTmpl = template.Must(commonTmpl().Parse(proxyDocHTML))

//go:embed tmpl/proxydoc/proxydoc.md
var proxyDocMD []byte
var proxyDocBody = mustRenderMarkdown(proxyDocMD)

func ProxyDoc(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
		Common pageCommon
		Body   template.HTML
	}{
		Common: getPageCommon(s, r),
		Body:   proxyDocBody,
	}
	proxyDocTmpl.Execute(w, page)
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
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		err = s.DB.QueryRowContext(ctx, "SELECT id, usageLimit, usage FROM proxy_session WHERE id=?", sessionID).Scan(&ss.ID, &ss.UsageLimit, &ss.Usage)
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

func addUsage(ctx context.Context, s *Server, sessionID string, usage int64) error {
	var err error
	if strings.HasPrefix(sessionID, voucherPrefix) {
		s.vouchers.use(sessionID, usage)
	} else {
		_, err = s.DB.ExecContext(ctx, "UPDATE proxy_session SET usage=usage+? WHERE id=?", usage, sessionID)
	}
	if err != nil {
		return errors.Wrap(err, "")
	}
	return nil
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
