package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/assistant"
)

// ownerRouteRequest is one concrete request for a registered route.
type ownerRouteRequest struct {
	pattern string
	method  string
	path    string
}

// registeredRoutePatterns reads every Handle/HandleFunc pattern in the
// package sources, the same scan TestAssistantFeatureKeepsEveryExistingRoute
// uses, so a route added later is covered without editing this test.
func registeredRoutePatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing web sources: %v", err)
	}
	pat := regexp.MustCompile(`Handle(?:Func)?\(([^"\n]*)"([^"]+)"`)
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, m := range pat.FindAllStringSubmatch(string(b), -1) {
			p := m[2]
			if strings.Contains(m[1], "TrimSuffix") || strings.Contains(m[1], "assistant.") {
				continue
			}
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// concreteRequests turns a pattern into requests: its own method, or GET and
// POST for a pattern without one; {params} and subtree slashes get a value.
func concreteRequests(pattern string) []ownerRouteRequest {
	methods := []string{http.MethodGet, http.MethodPost}
	p := pattern
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		methods = []string{pattern[:i]}
		p = pattern[i+1:]
	}
	p = pathParam.ReplaceAllString(p, "x")
	if strings.HasSuffix(p, "/") && p != "/" {
		p += "x"
	}
	out := make([]ownerRouteRequest, 0, len(methods))
	for _, m := range methods {
		out = append(out, ownerRouteRequest{pattern: pattern, method: m, path: p})
	}
	return out
}

func doRequest(srv *Server, method, path string, auth []string) *httptest.ResponseRecorder {
	// A stream route that wrongly lets the request in would otherwise
	// never return; the deadline ends it and the status shows the leak.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := newLocalRequest(method, path, strings.NewReader("{}")).WithContext(ctx)
	for _, a := range auth {
		req.Header.Add("Authorization", a)
	}
	req.Header.Set("Content-Type", "application/json")
	// Same-origin, so the CSRF check passes and auth decides.
	req.Header.Set("Origin", "http://localhost")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

// ownerRoutes are the registered routes that refuse an unauthenticated
// request on a server with an owner token: the ones an assistant token
// must never open.
func ownerRoutes(t *testing.T) []ownerRouteRequest {
	t.Helper()
	srv, _, _ := newAssistantServer(t, Config{Token: "owner-secret", WebMutations: true})
	var out []ownerRouteRequest
	for _, p := range registeredRoutePatterns(t) {
		if assistant.IsAssistantPath(strings.TrimPrefix(p, "POST ")) {
			continue
		}
		for _, req := range concreteRequests(p) {
			if rr := doRequest(srv, req.method, req.path, nil); rr.Code == http.StatusUnauthorized {
				out = append(out, req)
			}
		}
	}
	return out
}

// assistantHeaderForms are spellings of an assistant token in the
// Authorization header(s) that the assistant routes accept, or that must at
// least be refused everywhere.
func assistantHeaderForms(tok string) map[string][]string {
	return map[string][]string{
		"Bearer":              {"Bearer " + tok},
		"lowercase bearer":    {"bearer " + tok},
		"uppercase BEARER":    {"BEARER " + tok},
		"extra spaces":        {"Bearer    " + tok},
		"surrounding spaces":  {"   Bearer " + tok + "   "},
		"bare token":          {tok},
		"bare token, spaces":  {"  " + tok + "  "},
		"two headers":         {"Bearer " + tok, "Bearer " + tok},
		"second header":       {"Basic dXNlcjpwYXNz", "Bearer " + tok},
		"owner header first":  {"Bearer owner-secret", "Bearer " + tok},
		"assistant then junk": {"Bearer " + tok, "Bearer nope"},
	}
}

// Every owner route refuses an assistant token in any header spelling the
// assistant routes understand, with or without an owner token configured.
func TestOwnerRoutes_RefuseAssistantTokenInEveryHeaderForm(t *testing.T) {
	routes := ownerRoutes(t)
	want := map[string]bool{"/api/menu": false, "/api/connect/tokens": false, "/api/settings": false, "/events/menu": false}
	for _, r := range routes {
		if _, ok := want[r.path]; ok {
			want[r.path] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Fatalf("owner route discovery missed %s (found %d routes): the scan or the probe is broken", p, len(routes))
		}
	}

	t.Logf("checking %d owner route requests", len(routes))
	for _, cfg := range []Config{{Token: "owner-secret", WebMutations: true}, {WebMutations: true}} {
		srv, tok, fx := newAssistantServer(t, cfg)
		for form, headers := range assistantHeaderForms(tok) {
			for _, r := range routes {
				rr := doRequest(srv, r.method, r.path, headers)
				if rr.Code != http.StatusUnauthorized {
					t.Errorf("owner token %q, %s: %s %s = %d, want 401", cfg.Token, form, r.method, r.path, rr.Code)
				}
			}
		}
		if fx.calls != 0 {
			t.Errorf("owner token %q: an assistant token ran %d command(s) through an owner route", cfg.Token, fx.calls)
		}
	}
}

// /healthz is public but tells authorized callers the profile and version;
// an assistant token is not authorized for that detail.
func TestHealthz_AssistantTokenGetsNoDetail(t *testing.T) {
	srv, tok, _ := newAssistantServer(t, Config{Profile: "work"})
	for form, headers := range assistantHeaderForms(tok) {
		rr := doRequest(srv, http.MethodGet, "/healthz", headers)
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"profile"`) {
			t.Errorf("%s: /healthz = %d %s, want 200 without profile", form, rr.Code, rr.Body)
		}
	}
}

// One parser on both sides: each header spelling is accepted on the
// assistant routes (with an assistant token) exactly when the same spelling
// is accepted on owner routes (with the owner token). The one designed
// difference is the bare form, which only an adk_ token may use.
func TestBearerParsing_SameOnOwnerAndAssistantRoutes(t *testing.T) {
	srv, tok, _ := newAssistantServer(t, Config{Token: "owner-secret", WebMutations: true})
	cases := []struct {
		name     string
		headers  func(tok string) []string
		accepted bool
	}{
		{"Bearer", func(x string) []string { return []string{"Bearer " + x} }, true},
		{"lowercase bearer", func(x string) []string { return []string{"bearer " + x} }, true},
		{"uppercase BEARER", func(x string) []string { return []string{"BEARER " + x} }, true},
		{"extra spaces after scheme", func(x string) []string { return []string{"Bearer     " + x} }, true},
		{"surrounding spaces", func(x string) []string { return []string{"  Bearer " + x + "  "} }, true},
		{"tab after scheme", func(x string) []string { return []string{"Bearer\t" + x} }, false},
		{"no space after scheme", func(x string) []string { return []string{"Bearer" + x} }, false},
		{"scheme only", func(string) []string { return []string{"Bearer"} }, false},
		{"scheme and spaces", func(string) []string { return []string{"Bearer    "} }, false},
		{"empty header", func(string) []string { return []string{""} }, false},
		{"other scheme", func(x string) []string { return []string{"Basic " + x} }, false},
		{"token then junk", func(x string) []string { return []string{"Bearer " + x + " junk"} }, false},
		{"two identical headers", func(x string) []string { return []string{"Bearer " + x, "Bearer " + x} }, false},
		{"right token second", func(x string) []string { return []string{"Bearer wrong", "Bearer " + x} }, false},
		{"right token first", func(x string) []string { return []string{"Bearer " + x, "Bearer wrong"} }, false},
	}
	for _, c := range cases {
		ownerOK := doRequest(srv, http.MethodGet, "/api/settings", c.headers("owner-secret")).Code == http.StatusOK
		assistantOK := doRequest(srv, http.MethodGet, "/api/assistant/v1/whoami", c.headers(tok)).Code == http.StatusOK
		if ownerOK != c.accepted || assistantOK != c.accepted {
			t.Errorf("%s: owner route accepted=%v, assistant route accepted=%v, want both %v", c.name, ownerOK, assistantOK, c.accepted)
		}
	}

	// The bare form: an assistant token may be sent without a scheme (xAI),
	// an owner token may not.
	if rr := doRequest(srv, http.MethodGet, "/api/assistant/v1/whoami", []string{tok}); rr.Code != http.StatusOK {
		t.Errorf("bare assistant token on the assistant route = %d, want 200", rr.Code)
	}
	if rr := doRequest(srv, http.MethodGet, "/api/settings", []string{"owner-secret"}); rr.Code != http.StatusUnauthorized {
		t.Errorf("bare owner token on an owner route = %d, want 401", rr.Code)
	}
	// The owner token is never an assistant credential, in any form.
	for form, headers := range assistantHeaderForms("owner-secret") {
		if rr := doRequest(srv, http.MethodGet, "/api/assistant/v1/whoami", headers); rr.Code != http.StatusUnauthorized {
			t.Errorf("owner token, %s, on the assistant route = %d, want 401", form, rr.Code)
		}
	}
}

// There is exactly one Authorization parser: the web package has no copy
// of its own.
func TestWebHasNoOwnBearerParser(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if strings.Contains(src, `"Bearer "`) || strings.Contains(src, `Header.Get("Authorization")`) {
			t.Errorf("%s parses the Authorization header itself; use assistant.RequestBearerToken", f)
		}
	}
}
