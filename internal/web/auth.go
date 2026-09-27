package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorizeRequest accepts a bearer header or a cookie established by a
// valid tokened visit. API query strings are never credentials.
func (s *Server) authorizeRequest(r *http.Request) bool {
	return s.authorize(r, false)
}

// authorizeWSRequest authorizes the WebSocket terminal-bridge upgrade. It
// additionally accepts the token via the query string because browsers cannot
// set headers on the WS handshake. This is the one documented exception to the
// header-only rule (report #5); handleIndex sets Referrer-Policy: no-referrer
// and the client strips the token from the URL after connecting.
func (s *Server) authorizeWSRequest(r *http.Request) bool {
	return s.authorize(r, true)
}

// authorizeStreamRequest authorizes an SSE stream request. Like the WebSocket
// upgrade, an EventSource cannot set an Authorization header, so the token is
// also accepted via the query string here. This mirrors authorizeWSRequest and
// is the documented SSE exception to the header-only rule (report #5): the
// affected pages set Referrer-Policy: no-referrer. JSON API endpoints stay on
// header-only authorizeRequest. Loopback/no-token behavior is unchanged; a
// bad or missing token still 401s.
func (s *Server) authorizeStreamRequest(r *http.Request) bool {
	return s.authorize(r, true)
}

func (s *Server) authorize(r *http.Request, allowQueryToken bool) bool {
	if s.cfg.Token == "" {
		return true
	}

	if allowQueryToken {
		queryToken := strings.TrimSpace(r.URL.Query().Get("token"))
		if queryToken != "" && secureEqual(queryToken, s.cfg.Token) {
			return true
		}
	}

	headerToken := bearerToken(r.Header.Get("Authorization"))
	if headerToken != "" && secureEqual(headerToken, s.cfg.Token) {
		return true
	}
	if cookie, err := r.Cookie("agentdeck_token"); err == nil && secureEqual(cookie.Value, s.cfg.Token) {
		return true
	}

	return false
}

func (s *Server) tokenCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" && secureEqual(r.URL.Query().Get("token"), s.cfg.Token) {
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Strict are set; Secure follows TLS so plain-HTTP localhost keeps working
				Name: "agentdeck_token", Value: s.cfg.Token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
			})
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}

	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return ""
	}

	token := strings.TrimSpace(strings.TrimPrefix(authHeader, bearerPrefix))
	if token == "" {
		return ""
	}
	return token
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
