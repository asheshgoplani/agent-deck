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
	return s.hasValidTokenCookie(r)
}

// hasValidTokenCookie reports whether any agentdeck_token cookie on the
// request matches the token. It checks every cookie of that name, not just
// the first: a same-site sibling origin (another port on the host) can set a
// bogus agentdeck_token with a longer Path, which browsers send first, and
// r.Cookie would then lock the real cookie out.
func (s *Server) hasValidTokenCookie(r *http.Request) bool {
	for _, c := range r.CookiesNamed(tokenCookieName) {
		if secureEqual(c.Value, s.cfg.Token) {
			return true
		}
	}
	return false
}

// tokenCookieName is the cookie a tokened visit (/?token=...) sets so the app
// shell, /api, /events and the WebSocket bridge authenticate without the token
// in the URL. authorize() accepts it everywhere the bearer header is accepted.
const tokenCookieName = "agentdeck_token"

// tokenCookieMaxAge is 30 days. Without a Max-Age the cookie is a session
// cookie and an installed web app (start_url "/") loses it as soon as it is
// closed (#3031). Each app-shell load with a valid cookie renews it, so a
// device in regular use stays signed in and one left unused for 30 days has
// to visit /?token= again. Rotating the token invalidates every cookie at
// once, because the cookie is compared against the current token.
const tokenCookieMaxAge = 30 * 24 * 60 * 60

func (s *Server) tokenCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.shouldSetTokenCookie(r) {
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly and SameSite=Strict are set; Secure follows HTTPS so plain-HTTP localhost keeps working
				Name: tokenCookieName, Value: s.cfg.Token, Path: "/", MaxAge: tokenCookieMaxAge,
				HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: requestCameOverHTTPS(r),
			})
		}
		next.ServeHTTP(w, r)
	})
}

// shouldSetTokenCookie reports whether this response should (re)issue the
// token cookie: on any request carrying the right ?token=, and on an app-shell
// load that already carries a valid cookie (renewal). It never issues a cookie
// for a token net/http cannot store verbatim, because that cookie would be
// rewritten on the way out and then rejected.
func (s *Server) shouldSetTokenCookie(r *http.Request) bool {
	if s.cfg.Token == "" || !isCookieSafeValue(s.cfg.Token) {
		return false
	}
	if secureEqual(r.URL.Query().Get("token"), s.cfg.Token) {
		return true
	}
	if r.Method != http.MethodGet || !isAppShellPath(r.URL.Path) {
		return false
	}
	return s.hasValidTokenCookie(r)
}

// requestCameOverHTTPS decides the cookie's Secure flag. The server itself
// serves plain HTTP, so HTTPS normally means a TLS-terminating proxy in front
// (tailscale serve, Caddy) that sends X-Forwarded-Proto: https. That header
// is NOT trusted for anything else: it can only add Secure, which makes the
// cookie stricter. A client that forges it on plain HTTP only stops its own
// browser from storing the cookie.
func requestCameOverHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// isAppShellPath mirrors the paths handleIndex serves the app shell on.
func isAppShellPath(path string) bool {
	return path == "/" || strings.HasPrefix(path, "/s/") || strings.HasPrefix(path, "/g/")
}

// isCookieSafeValue reports whether v survives net/http's cookie encoder
// unchanged: printable ASCII without '"', ',', ';', backslash or space. net/http
// drops or quotes anything else.
func isCookieSafeValue(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b <= 0x20 || b >= 0x7f || b == '"' || b == ',' || b == ';' || b == '\\' {
			return false
		}
	}
	return true
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
