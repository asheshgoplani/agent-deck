package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) authorizeRequest(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}

	queryToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if queryToken != "" && secureEqual(queryToken, s.cfg.Token) {
		return true
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
