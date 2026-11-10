package httproxy

import (
	"crypto/subtle"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"

	"github.com/fumin/httproxy/util/jsonrpc"
)

const (
	PathAdminLogin   = "/AdminLogin"
	PathAdminLoginDo = "/AdminLoginDo"
	PathAdminLogout  = "/AdminLogout"
	PathAdminIndex   = "/Admin"
	PathAdminRPC     = "/admin/json_rpc"
)

func dealVoucher(s *Server, in struct{}) (Session, error) {
	ss := s.vouchers.deal()
	return ss, nil
}

func handleAdmin(s *Server) {
	adminMethods := map[string]jsonrpc.MethodParams{
		"deal_voucher": m(s, dealVoucher, struct{}{}),
	}
	s.ServeMux.HandleFunc(PathAdminRPC, jsonrpc.NewHandler(adminMethods, s.auth))

	handleFunc(s, PathAdminLogin, AdminLogin)
	handleFunc(s, PathAdminLoginDo, AdminLoginDo)
	handleFunc(s, PathAdminLogout, AdminLogout)
	handleFunc(s, PathAdminIndex, AdminIndex)
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

type navbar struct {
	LoggedIn   bool
	PathLogin  string
	PathLogout string
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
	if subtle.ConstantTimeCompare([]byte(password), []byte(s.C.Secret.Password)) != 1 {
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

	page := struct {
		Common   adminCommon
		PathRPC  string
		Vouchers []Session
	}{
		Common:   getAdminCommon(s, r),
		PathRPC:  PathAdminRPC,
		Vouchers: s.vouchers.all(),
	}
	adminIndexTmpl.Execute(w, page)
}
