package web

import (
	"net/http"
	"net/url"
	"strings"
)

// csrfProtect rejects cross-origin state-changing requests (POST, PUT, PATCH,
// DELETE) by validating the Origin header against the request's Host. When no
// Origin is present, it falls back to the Referer header.
//
// This prevents CSRF attacks where a malicious page triggers fetch() or form
// submissions to the local agent-deck API (e.g. creating sessions that execute
// arbitrary commands via tmux).
//
// Report #4: when an auth token is configured (the exposed mode that report #1
// forces for any non-loopback bind), CSRF additionally fails closed — a
// mutation carrying NEITHER Origin NOR Referer is rejected, so a non-browser
// caller (or an SSRF pivot) can't slip a mutation past the Origin check. In the
// default loopback no-token dev mode this fail-closed step is skipped, leaving
// behavior unchanged for normal local/CLI users.
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	failClosed := s.cfg.Token != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		if !validateOrigin(r, failClosed) {
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

func validateOrigin(r *http.Request, failClosed bool) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		return originMatchesHost(origin, r.Host)
	}

	referer := strings.TrimSpace(r.Header.Get("Referer"))
	if referer != "" {
		return refererMatchesHost(referer, r.Host)
	}

	// No Origin or Referer. A browser cannot be coerced into omitting both on a
	// cross-origin request, so this is sound against classic web-page CSRF.
	// When failClosed is set (a token is configured — the exposed mode) we
	// still reject, because the residual risk is a non-browser local caller or
	// an SSRF pivot reaching the API. In the default loopback no-token dev mode
	// we allow it so curl/CLI tooling keeps working unchanged.
	return !failClosed
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
