package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName  = "isend_auth"
	maxFailures = 5
	lockWindow  = 5 * time.Minute
)

var (
	errBadPIN = errors.New("口令错误")
	errLocked = errors.New("尝试次数过多，请 5 分钟后再试")
)

// auth implements the optional PIN gate. A correct PIN earns a cookie holding
// a random per-process token, so restarting the hub logs everyone out.
type auth struct {
	pin   string
	token string

	mu    sync.Mutex
	fails map[string]*failure
}

type failure struct {
	count int
	first time.Time
}

func newAuth(pin string) *auth {
	b := make([]byte, 32)
	rand.Read(b)
	return &auth{pin: pin, token: hex.EncodeToString(b), fails: map[string]*failure{}}
}

func (a *auth) authorized(r *http.Request) bool {
	if a.pin == "" {
		return true
	}
	c, err := r.Cookie(cookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.token)) == 1
}

func (a *auth) login(w http.ResponseWriter, r *http.Request, pin string) error {
	if a.pin == "" {
		return nil
	}
	ip := clientIP(r)

	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f != nil && time.Since(f.first) > lockWindow {
		delete(a.fails, ip)
		f = nil
	}
	if f != nil && f.count >= maxFailures {
		return errLocked
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(a.pin)) != 1 {
		if f == nil {
			f = &failure{first: time.Now()}
			a.fails[ip] = f
		}
		f.count++
		return errBadPIN
	}
	delete(a.fails, ip)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: a.token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
	return nil
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
