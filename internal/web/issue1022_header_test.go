package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestWebShell_HasHeaderElement_RegressionFor1022 is the regression test for
// issue #1022 part 1: the web shell's Topbar component renders a generic
// <div class="topbar"> instead of a semantic <header> element. Several
// Playwright visual tests (tests/e2e/cascade-baseline.spec.ts,
// tests/e2e/mobile-e2e.spec.ts, tests/e2e/visual/p9-pol*.spec.ts) wait for
// `page.waitForSelector('header', ...)` to detect that the Preact app has
// mounted. With no <header> in the rendered DOM, those tests time out
// before the screenshot step, so the weekly visual regression suite
// surfaces a failure with no diff image to triage.
//
// The web shell is a Preact SPA: the served index.html is just an empty
// <div id="app-root"> shell; the real markup is produced client-side by
// the modules under /static/app/. So this test asserts on the source of
// Topbar.js (the component that owns the top-bar landmark) rather than
// on a server-rendered DOM. That is what the Playwright tests actually
// observe at runtime — once Topbar.js emits <header>, the selector
// resolves and the weekly suite is unblocked.
//
// The fix is to change the root element of Topbar() from
// `<div class="topbar">` to `<header class="topbar">` (the CSS grid in
// app.css targets the .topbar class, not the tag, so layout is preserved).
func TestWebShell_HasHeaderElement_RegressionFor1022(t *testing.T) {
	t.Parallel()

	s := NewServer(Config{})
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticFileServer()))

	req := httptest.NewRequest(http.MethodGet, "/static/app/Topbar.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /static/app/Topbar.js: status %d, want 200", w.Code)
	}
	body := w.Body.String()

	// The Topbar component must render a semantic <header> landmark so
	// that page.locator('header') in the Playwright visual suite resolves.
	// htm/preact tagged templates compile from JSX-like syntax, so the
	// source contains the literal opening tag.
	headerOpen := regexp.MustCompile(`<header[\s>]`)
	if !headerOpen.MatchString(body) {
		t.Errorf("Topbar.js must render a <header> element (issue #1022): " +
			"found neither `<header>` nor `<header ...>` in the component source")
	}

	// Guard against the regression itself: the previous root was
	// `<div class="topbar">`. If that string still appears as the
	// Topbar's outer element, the fix has not landed.
	if strings.Contains(body, `<div class="topbar">`) {
		t.Errorf("Topbar.js still uses `<div class=\"topbar\">` as its root; " +
			"issue #1022 requires a <header class=\"topbar\"> root")
	}
}
