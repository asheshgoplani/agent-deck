package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// newLocalRequest gives direct-handler fixtures the Host a local browser sends.
// Absolute URLs retain their own Host for tests that exercise authority rules.
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	if strings.HasPrefix(target, "/") {
		r.Host = "localhost"
	}
	return r
}
