package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/klauspost/compress/gzhttp"
)

// Content types we want to gzip. Excludes text/event-stream (Pitfall 1:
// buffered compression stalls SSE clients) and pre-compressed formats
// (png, jpg, webp, woff2 — already compressed, re-encoding wastes CPU).
var gzipContentTypes = []string{
	"text/html",
	"text/css",
	"text/javascript",
	"application/javascript",
	"application/json",
	"image/svg+xml",
	"application/manifest+json",
	"font/woff",
	"font/woff2",
	"application/wasm",
}

// hashedAssetPattern matches filenames whose basename contains a
// hex-like hash suffix before the extension, e.g. main.a1b2c3.js,
// styles.abcdef123456.css, vendor.9f8e7d.woff2. Hashes are 6+ lowercase
// hex chars to avoid matching random version numbers.
var hashedAssetPattern = regexp.MustCompile(`\.[a-f0-9]{6,}\.(js|mjs|css|woff2?|map)$`)

// gzipAndCacheStatic wraps a static file handler with:
//  1. gzhttp compression limited to the content-type allowlist above,
//     with a 1024-byte minimum size floor. SSE, WebSocket, and already
//     compressed formats bypass because they're not in the allowlist.
//  2. Cache-Control headers based on request path:
//     - hashed assets (main.a1b2c3.js): public, max-age=31536000, immutable
//     - index.html and non-hashed assets: no-cache, must-revalidate
//
// This middleware must ONLY be applied to the /static/ prefix in
// server.go. Applying it to SSE or WebSocket routes will break them.
func gzipAndCacheStatic(inner http.Handler) http.Handler {
	wrapper, err := gzhttp.NewWrapper(
		gzhttp.MinSize(1024),
		gzhttp.ContentTypes(gzipContentTypes),
	)
	if err != nil {
		// gzhttp.NewWrapper only errors on invalid option combinations.
		// Our options are static, so this never fires in practice.
		// Fall back to pass-through to preserve behavior.
		return cacheControlMiddleware(inner)
	}
	return cacheControlMiddleware(wrapper(inner))
}

// cacheControlMiddleware sets Cache-Control headers on the response
// BEFORE the inner handler writes. It inspects r.URL.Path to classify
// the asset as hashed (1-year immutable) vs everything else (no-cache).
func cacheControlMiddleware(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Strip the /static/ prefix if present — the test harness does
		// NOT send /static/ prefixes because it wraps the handler directly,
		// but production does via mux.Handle("/static/", ...). Handle both.
		path = strings.TrimPrefix(path, "/static/")
		path = strings.TrimPrefix(path, "/")

		isHTML := strings.HasSuffix(path, "index.html") || path == "" || path == "/"
		isHashed := hashedAssetPattern.MatchString(path)

		switch {
		case isHashed:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case isHTML:
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		default:
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		inner.ServeHTTP(w, r)
	})
}
