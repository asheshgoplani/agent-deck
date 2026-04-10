package web

import (
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"sync"
)

//go:generate go run bundle.go

// Assets manages the manifest-based mapping from logical source paths
// (e.g. "app/main.js") to hashed bundle output paths (e.g.
// "dist/main.a1b2c3.js"). In dev mode (no manifest present) or when the
// AGENTDECK_WEB_BUNDLE=0 env var is set, ResolveAsset falls back to
// serving the unbundled source file at /static/<logical>.
//
// PERF-H: the manifest is emitted by bundle.go (go:generate entry point)
// which drives github.com/evanw/esbuild/pkg/api. The dev/prod fork keeps
// live-reload friendly while shipping hashed, bundled JS in production.
type Assets struct {
	mu       sync.RWMutex
	manifest map[string]string // logical → hashed (relative to /static/)
	devMode  bool
}

// LoadAssets reads a manifest file from a filesystem path and returns
// an Assets instance. A missing manifest is NOT an error — the returned
// instance operates in dev mode (fallback to /static/<logical>). The
// AGENTDECK_WEB_BUNDLE=0 env var forces dev mode even if the manifest
// is present, which is the rollback lever.
func LoadAssets(manifestPath string) (*Assets, error) {
	if os.Getenv("AGENTDECK_WEB_BUNDLE") == "0" {
		return newDevAssets(), nil
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return newDevAssets(), nil
		}
		return nil, err
	}
	return loadAssetsFromBytes(data)
}

// LoadAssetsFromFS reads a manifest from an io/fs.FS (e.g. the server's
// embed.FS). Used by the server at startup so the manifest can travel
// inside the binary. Missing file → dev mode.
func LoadAssetsFromFS(fsys fs.FS, name string) (*Assets, error) {
	if os.Getenv("AGENTDECK_WEB_BUNDLE") == "0" {
		return newDevAssets(), nil
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return newDevAssets(), nil
	}
	return loadAssetsFromBytes(data)
}

func newDevAssets() *Assets {
	return &Assets{manifest: map[string]string{}, devMode: true}
}

func loadAssetsFromBytes(data []byte) (*Assets, error) {
	a := &Assets{manifest: map[string]string{}}
	if err := json.Unmarshal(data, &a.manifest); err != nil {
		return nil, err
	}
	return a, nil
}

// ResolveAsset maps a logical source path to the URL the browser should
// fetch. In dev mode: "/static/<logical>". In prod mode: "/static/<hashed>".
// Unknown logical paths fall back to the logical form so missing-asset
// regressions surface as 404s rather than silent manifest lookups.
func (a *Assets) ResolveAsset(logical string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.devMode {
		return "/static/" + logical
	}
	hashed, ok := a.manifest[logical]
	if !ok {
		return "/static/" + logical
	}
	return "/static/" + hashed
}

// SubstitutePlaceholders replaces every {{ASSET:logical}} token in the
// input string with the resolved URL. Used to fill index.html at serve
// time so the on-disk file stays hand-written (Pitfall 3 mitigation:
// dirty-tree check remains clean because the bundler never writes back
// to index.html).
func (a *Assets) SubstitutePlaceholders(template string) string {
	const prefix = "{{ASSET:"
	const suffix = "}}"
	var b strings.Builder
	i := 0
	for {
		idx := strings.Index(template[i:], prefix)
		if idx < 0 {
			b.WriteString(template[i:])
			break
		}
		start := i + idx
		b.WriteString(template[i:start])
		end := strings.Index(template[start+len(prefix):], suffix)
		if end < 0 {
			// Malformed placeholder. Leave the rest of the string as-is.
			b.WriteString(template[start:])
			break
		}
		logical := template[start+len(prefix) : start+len(prefix)+end]
		b.WriteString(a.ResolveAsset(logical))
		i = start + len(prefix) + end + len(suffix)
	}
	return b.String()
}
