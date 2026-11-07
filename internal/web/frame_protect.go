package web

import "net/http"

// frameAncestorsNone is the CSP that forbids every page from framing a
// response. Nothing in agent-deck frames the web UI: the Mac app and the
// AgentDeck browser load it as a top-level page, and file links open in a
// new tab.
const frameAncestorsNone = "frame-ancestors 'none'"

// frameProtect forbids framing on every response (X-Frame-Options for old
// browsers, CSP frame-ancestors for current ones). The token cookie lasts 30
// days (#3031) and SameSite=Strict still sends it to frames embedded by a
// same-site page (another port on the host, a sibling tailnet machine), so
// without this such a page could frame the logged-in deck and clickjack it.
//
// It wraps the whole handler chain so error responses (421, 404) are covered
// too. Handlers that set their own Content-Security-Policy replace this one
// and must include frame-ancestors 'none' themselves (see fileLinkCSP).
func frameProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", frameAncestorsNone)
		next.ServeHTTP(w, r)
	})
}
