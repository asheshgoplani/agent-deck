package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorizeRequest authorizes an HTTP API request using the
// Authorization: Bearer header ONLY. Report #5: the token is never accepted
// from the query string here, because query-string secrets leak to access
// logs, browser history, Referer, and reverse-proxy logs.
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

	return false
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
