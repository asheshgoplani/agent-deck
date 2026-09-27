package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenCookiePersistsAuth(t *testing.T) {
	s := NewServer(Config{ListenAddr: "127.0.0.1:8420", Token: "secret"})
	visit := httptest.NewRequest(http.MethodGet, "http://localhost:8420/?token=secret", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, visit)
	if w.Code != http.StatusOK {
		t.Fatalf("visit: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	c := cookies[0]
	if c.Name != "agentdeck_token" || c.Value != "secret" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Fatalf("cookie attributes: %+v", c)
	}
	for _, path := range []string{"/api/sessions", "/events/menu", "/ws/session/fake"} {
		r := httptest.NewRequest(http.MethodGet, "http://localhost:8420"+path, nil)
		r.AddCookie(c)
		if !s.authorizeRequest(r) || !s.authorizeStreamRequest(r) || !s.authorizeWSRequest(r) {
			t.Errorf("cookie rejected on %s", path)
		}
	}
	bad := httptest.NewRequest(http.MethodGet, "http://localhost:8420/?token=wrong", nil)
	bad.AddCookie(&http.Cookie{Name: "agentdeck_token", Value: "wrong"})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, bad)
	if len(w.Result().Cookies()) != 0 || s.authorizeRequest(bad) {
		t.Fatal("invalid token accepted")
	}
	tlsVisit := httptest.NewRequest(http.MethodGet, "https://localhost:8420/?token=secret", nil)
	tlsVisit.TLS = &tls.ConnectionState{}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, tlsVisit)
	if !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS cookie lacks Secure")
	}
}
