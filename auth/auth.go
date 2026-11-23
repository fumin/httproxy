package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	cookieNameAuthentication = "auth"
	bearerPrefix             = "Bearer "
)

type Authorizer struct {
	sync.RWMutex
	token string
}

func NewAuthorizer() *Authorizer {
	a := &Authorizer{}
	a.RotateToken()
	return a
}

func (a *Authorizer) SetCookie(w http.ResponseWriter) {
	a.RLock()
	token := a.token
	a.RUnlock()

	c := &http.Cookie{
		Name:     cookieNameAuthentication,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().AddDate(3, 0, 0),
	}
	http.SetCookie(w, c)
}

func (a *Authorizer) DeleteCookie(w http.ResponseWriter) {
	c := &http.Cookie{
		Name:   cookieNameAuthentication,
		MaxAge: -1,
	}
	http.SetCookie(w, c)
}

func (a *Authorizer) RotateToken() {
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)

	a.Lock()
	a.token = token
	a.Unlock()
}

func (a *Authorizer) Check(w http.ResponseWriter, r *http.Request) bool {
	reqToken := GetToken(r, cookieNameAuthentication)
	a.RLock()
	ok := subtle.ConstantTimeCompare([]byte(reqToken), []byte(a.token)) == 1
	a.RUnlock()
	if ok {
		return true
	}

	w.Header().Set("WWW-Authenticate", bearerPrefix)
	http.Error(w, "", http.StatusForbidden)
	return false
}

func (a *Authorizer) LoggedIn(r *http.Request) bool {
	cookie, err := r.Cookie(cookieNameAuthentication)
	if err != nil {
		return false
	}
	reqToken := []byte(cookie.Value)

	a.RLock()
	ok := (subtle.ConstantTimeCompare(reqToken, []byte(a.token)) == 1)
	a.RUnlock()
	return ok
}

func (a *Authorizer) Token() string {
	a.RLock()
	token := a.token
	a.RUnlock()
	return token
}

func GetToken(r *http.Request, cookieName string) string {
	if token := fromBearer(r); token != "" {
		return token
	}
	if token := fromCookie(r, cookieName); token != "" {
		return token
	}
	return ""
}

func fromBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) < len(bearerPrefix) || auth[:len(bearerPrefix)] != bearerPrefix {
		return ""
	}
	return auth[len(bearerPrefix):]
}

func fromCookie(r *http.Request, cookieName string) string {
	// Defend against cross-site-request-forgery.
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil {
		return ""
	}
	if origin.Host != r.Host {
		return ""
	}

	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
