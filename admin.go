package httproxy

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/fumin/httproxy/util"
	"github.com/fumin/httproxy/util/jsonrpc"
	"github.com/pkg/errors"
)

const (
	PathAdminLogin   = "/AdminLogin"
	PathAdminLoginDo = "/AdminLoginDo"
	PathAdminLogout  = "/AdminLogout"
	PathAdminIndex   = "/Admin"
	PathAdminRPC     = "/admin/json_rpc"
)

func handleAdmin(s *Server) {
	adminMethods := map[string]jsonrpc.MethodParams{
		"dealVMVoucher":    m(s, dealVMVoucher, struct{}{}),
		"dealProxyVoucher": m(s, dealProxyVoucher, struct{}{}),
	}
	s.ServeMux.HandleFunc(PathAdminRPC, jsonrpc.NewHandler(adminMethods, s.auth))

	handleFunc(s, PathAdminLogin, AdminLogin)
	handleFunc(s, PathAdminLoginDo, AdminLoginDo)
	handleFunc(s, PathAdminLogout, AdminLogout)
	handleFunc(s, PathAdminIndex, AdminIndex)
}

func dealVMVoucher(ctx context.Context, s *Server, in struct{}) (out VM, err error) {
	if !s.vmService.HasSpareCapacity() {
		return out, errors.Errorf("no capacity")
	}

	const usageLimit = 3 * 24 * time.Hour
	out.ID = "voucher_" + util.RandStr()
	out.UsageLimit = int(usageLimit.Seconds())
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO vm (id, usageLimit, usage, createAt) VALUES (?, ?, ?, ?)", out.ID, out.UsageLimit, out.Usage, time.Now().Unix()); err != nil {
		return out, errors.Wrap(err, "")
	}

	started, err := s.vmService.Put(out.ID)
	if err != nil {
		return out, nil
	}
	select {
	case <-ctx.Done():
	case <-started:
	}
	out.Running = (s.vmService.Get(out.ID) != nil)

	return
}

func dealProxyVoucher(ctx context.Context, s *Server, in struct{}) (Session, error) {
	ss := s.vouchers.deal()
	return ss, nil
}

//go:embed tmpl/admin/navbar.html
var adminNavbarHTML string
var adminNavbarTmpl = template.Must(template.New("").Parse(adminNavbarHTML))

func adminCommonTmpl() *template.Template {
	return template.Must(adminNavbarTmpl.Clone())
}

type adminCommon struct {
	Navbar struct {
		LoggedIn   bool
		PathIndex  string
		PathLogin  string
		PathLogout string
	}
}

func getAdminCommon(s *Server, r *http.Request) adminCommon {
	c := adminCommon{}
	c.Navbar.LoggedIn = s.auth.LoggedIn(r)
	c.Navbar.PathIndex = PathAdminIndex
	c.Navbar.PathLogin = PathAdminLogin
	c.Navbar.PathLogout = PathAdminLogout
	return c
}

//go:embed tmpl/admin/login.html
var adminLoginHTML string
var adminLoginTmpl = template.Must(adminCommonTmpl().Parse(adminLoginHTML))

func AdminLogin(s *Server, w http.ResponseWriter, r *http.Request) {
	page := struct {
		Common      adminCommon
		PathLoginDo string
	}{
		Common:      getAdminCommon(s, r),
		PathLoginDo: PathAdminLoginDo,
	}
	adminLoginTmpl.Execute(w, page)
}

func AdminLoginDo(s *Server, w http.ResponseWriter, r *http.Request) {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host {
		http.Error(w, "", http.StatusForbidden)
		return
	}
	password := r.FormValue("password")
	if subtle.ConstantTimeCompare([]byte(password), []byte(s.C.Secret.Password.Reveal())) != 1 {
		http.Error(w, "", http.StatusForbidden)
		return
	}

	s.auth.SetCookie(w)
	http.Redirect(w, r, PathAdminIndex, http.StatusSeeOther)
}

func AdminLogout(s *Server, w http.ResponseWriter, r *http.Request) {
	s.auth.RotateToken()
	s.auth.DeleteCookie(w)
	http.Redirect(w, r, PathAdminLogin, http.StatusSeeOther)
}

//go:embed tmpl/admin/index.html
var adminIndexHTML string
var adminIndexTmpl = template.Must(adminCommonTmpl().Parse(adminIndexHTML))

func AdminIndex(s *Server, w http.ResponseWriter, r *http.Request) {
	if !s.auth.LoggedIn(r) {
		http.Redirect(w, r, PathAdminLogin, http.StatusSeeOther)
		return
	}

	vms, err := runnableVMs(r.Context(), s.DB)
	if err != nil {
		http.Error(w, fmt.Sprintf("%+v", err), http.StatusInternalServerError)
		return
	}
	for i, m := range vms {
		vms[i].Running = (s.vmService.Get(m.ID) != nil)
	}

	page := struct {
		Common        adminCommon
		PathRPC       string
		VMs           []VM
		ProxyVouchers []Session
	}{
		Common:        getAdminCommon(s, r),
		PathRPC:       PathAdminRPC,
		VMs:           vms,
		ProxyVouchers: s.vouchers.all(),
	}
	adminIndexTmpl.Execute(w, page)
}
