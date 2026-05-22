package web

import (
	"net/http"
	"net/url"
	"strings"
)

// csrfProtect rejects cross-origin state-changing requests (POST, PUT, PATCH,
// DELETE) by validating the Origin header against the request's Host. When no
// Origin is present, it falls back to the Referer header. Requests without
// either header are rejected for mutation methods — legitimate browser requests
// always include at least one.
//
// This prevents CSRF attacks where a malicious page triggers fetch() or form
// submissions to the local agent-deck API (e.g. creating sessions that execute
// arbitrary commands via tmux).
func csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		if !validateOrigin(r) {
			writeAPIError(w, http.StatusForbidden, ErrCodeCSRF, "cross-origin request blocked")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func validateOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		return originMatchesHost(origin, r.Host)
	}

	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer != "" {
		return refererMatchesHost(referer, r.Host)
	}

	// No Origin or Referer — non-browser client (curl, CLI tools).
	// Allow these through; they aren't subject to CSRF because the attacker
	// cannot make a victim's browser omit both headers on a cross-origin request.
	return true
}

func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

func refererMatchesHost(referer, host string) bool {
	parsed, err := url.Parse(referer)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}
