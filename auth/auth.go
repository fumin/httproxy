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
	if ok := a.checkBearer(r); ok {
		return true
	}
	if ok := a.checkCookie(r); ok {
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
	return a.checkToken(cookie.Value)
}

func (a *Authorizer) Token() string {
	a.RLock()
	token := a.token
	a.RUnlock()
	return token
}

func (a *Authorizer) checkBearer(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if len(auth) < len(bearerPrefix) || auth[:len(bearerPrefix)] != bearerPrefix {
		return false
	}
	token := auth[len(bearerPrefix):]

	return a.checkToken(token)
}

func (a *Authorizer) checkCookie(r *http.Request) bool {
	// Defend against cross-site-request-forgery.
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil {
		return false
	}
	if origin.Host != r.Host {
		return false
	}

	cookie, err := r.Cookie(cookieNameAuthentication)
	if err != nil {
		return false
	}
	token := cookie.Value

	return a.checkToken(token)
}

func (a *Authorizer) checkToken(token string) bool {
	a.RLock()
	ok := subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) == 1
	a.RUnlock()
	return ok
}
