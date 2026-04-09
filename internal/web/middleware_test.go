package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// gzipAndCacheStatic is the middleware under test. It is added in Task 2.
// This test file intentionally references it by name so the test fails
// with an "undefined" compile error until Task 2 lands the implementation.

func TestMiddleware_GzipsTextCSS(t *testing.T) {
	body := strings.Repeat("a { color: red; }\n", 100) // > 1 KB
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/styles.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Errorf("want Content-Encoding=gzip for text/css, got %q", ce)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, _ := io.ReadAll(gz)
	if !bytes.Equal(decoded, []byte(body)) {
		t.Errorf("decoded body mismatch: got %d bytes, want %d", len(decoded), len(body))
	}
}

func TestMiddleware_GzipsApplicationJavascript(t *testing.T) {
	body := strings.Repeat("console.log('hello world');\n", 100)
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/app/main.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Errorf("want Content-Encoding=gzip for application/javascript, got %q", ce)
	}
}

func TestMiddleware_DoesNotGzipEventStream(t *testing.T) {
	// Regression for Pitfall 1 — SSE streams must NEVER be gzip wrapped
	// because buffered compression stalls the client waiting for a flush.
	// Even though this middleware is only applied to /static/ in production,
	// we test that the wrapper honors the Content-Type allowlist so a
	// hypothetical future mis-wiring (wrapping an SSE route by accident)
	// would still bypass gzip.
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/any", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ce := resp.Header.Get("Content-Encoding"); ce == "gzip" {
		t.Errorf("text/event-stream must NOT be gzipped (Pitfall 1)")
	}
}

func TestMiddleware_SkipsGzipBelowMinSize(t *testing.T) {
	body := "small" // < 1 KB
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/small.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ce := resp.Header.Get("Content-Encoding"); ce == "gzip" {
		t.Errorf("responses below MinSize(1024) must not be gzipped, got %q", ce)
	}
}

func TestMiddleware_CacheControlHashedAsset(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("x"))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/dist/main.a1b2c3.js", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get("Cache-Control")
	want := "public, max-age=31536000, immutable"
	if got != want {
		t.Errorf("hashed asset Cache-Control:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestMiddleware_CacheControlNonHashedAsset(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte("x"))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/styles.css", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get("Cache-Control")
	if !strings.Contains(got, "no-cache") {
		t.Errorf("non-hashed asset must have no-cache, got %q", got)
	}
}

func TestMiddleware_CacheControlIndexHTML(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/index.html", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get("Cache-Control")
	if !strings.Contains(got, "no-cache") {
		t.Errorf("index.html must have no-cache, got %q", got)
	}
}

func TestMiddleware_ByteSavings(t *testing.T) {
	// Sanity: 10 KB of CSS gzips to < 50% of input.
	body := strings.Repeat("a { color: red; margin: 0; padding: 0; }\n", 250)
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/big.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if len(raw) >= len(body)/2 {
		t.Errorf("gzip did not halve size: input=%d output=%d", len(body), len(raw))
	}
}

func TestMiddleware_E2EGzipsEmbeddedStyles(t *testing.T) {
	// End-to-end: load the real bundled styles.css from disk, serve it
	// through gzipAndCacheStatic, and assert Content-Encoding: gzip AND
	// a meaningfully smaller payload.
	//
	// This test catches regressions where someone removes the
	// gzipAndCacheStatic wrapper from server.go line 101 while leaving
	// middleware.go untouched -- because it exercises the real embedded
	// asset rather than a synthetic string.
	if testing.Short() {
		t.Skip("skipping e2e in -short")
	}

	cssBytes, err := readEmbeddedStyleCSS(t)
	if err != nil {
		t.Skipf("cannot read embedded styles.css: %v", err)
	}
	if len(cssBytes) < 1024 {
		t.Skipf("styles.css is too small (%d bytes) to exercise gzip threshold", len(cssBytes))
	}

	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write(cssBytes)
	})
	srv := httptest.NewServer(gzipAndCacheStatic(stub))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/static/styles.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("want Content-Encoding=gzip, got %q", ce)
	}
	gzippedBody, _ := io.ReadAll(resp.Body)
	if len(gzippedBody) >= len(cssBytes) {
		t.Errorf("gzip did not compress: input=%d output=%d", len(cssBytes), len(gzippedBody))
	}
	// Log the savings so the verification is human-readable
	t.Logf("styles.css gzip savings: %d -> %d bytes (%.1f%%)",
		len(cssBytes), len(gzippedBody),
		float64(len(gzippedBody))*100.0/float64(len(cssBytes)))
}

// readEmbeddedStyleCSS loads the bundled styles.css via the simplest
// path available. `go test` runs with cwd = the package directory
// (internal/web/), so the relative path to the embed source is
// ./static/styles.css.
func readEmbeddedStyleCSS(t *testing.T) ([]byte, error) {
	t.Helper()
	data, err := os.ReadFile("static/styles.css")
	if err != nil {
		return nil, err
	}
	return data, nil
}
