//go:build ignore

// bundle.go — go:generate entry point that drives esbuild via
// github.com/evanw/esbuild/pkg/api. Produces bundled, minified, hashed
// ESM output in internal/web/static/dist/ plus a manifest.json mapping
// logical source paths to hashed output paths.
//
// Run via: go generate ./internal/web/...
//
// PERF-H: ships LAST in Phase 8 per ROADMAP.md ordering constraint
// because minification obscures pre-existing bugs, bundling reorders
// module load, and HTM + esbuild is an unusual combination. Running
// this plan after all other Phase 8 optimizations means the byte
// budget gate measures the combined effect of every perf win.
//
// This file is `//go:build ignore` so it is excluded from the regular
// build — only `go run bundle.go` (or `go generate`) ever executes it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

func main() {
	result := api.Build(api.BuildOptions{
		EntryPoints: []string{"static/app/main.js"},
		Bundle:      true,
		Splitting:   true,
		Format:      api.FormatESModule,
		Outdir:      "static/dist",
		Target:      api.ES2022,
		Platform:    api.PlatformBrowser,

		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,

		EntryNames: "[name].[hash]",
		ChunkNames: "chunks/[name].[hash]",
		AssetNames: "assets/[name].[hash]",

		// Externals served via the index.html import map. Do NOT bundle —
		// these stay served as /static/vendor/*.mjs at runtime. If you add
		// a new bare-specifier import to the app code, also add it here
		// AND to the import map in index.html.
		External: []string{
			"preact",
			"preact/hooks",
			"preact/compat",
			"htm/preact",
			"@preact/signals",
			"@xterm/xterm",
			"@xterm/addon-fit",
			"@xterm/addon-webgl",
		},

		Write:    true,
		Metafile: true,
		LogLevel: api.LogLevelInfo,
	})

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "esbuild error: %s\n", e.Text)
		}
		os.Exit(1)
	}

	if result.Metafile == "" {
		fmt.Fprintln(os.Stderr, "esbuild: metafile missing")
		os.Exit(1)
	}

	type metaOutput struct {
		EntryPoint string `json:"entryPoint"`
	}
	type meta struct {
		Outputs map[string]metaOutput `json:"outputs"`
	}
	var m meta
	if err := json.Unmarshal([]byte(result.Metafile), &m); err != nil {
		fmt.Fprintf(os.Stderr, "esbuild: metafile parse error: %v\n", err)
		os.Exit(1)
	}

	// Emit manifest.json mapping logical entry point paths to hashed
	// output paths. Chunk files (split shared code) are not registered in
	// the manifest — the bundle's <script type="module"> entry discovers
	// them automatically via ES module imports, so the index.html only
	// needs to reference the entry.
	manifest := map[string]string{}
	for outputPath, info := range m.Outputs {
		if info.EntryPoint == "" {
			continue
		}
		// Strip the "static/" prefix so the manifest is relative to the
		// /static/ mount. The EntryPoint field is the INPUT path; the
		// outputPath is the OUTPUT path in the same relative space.
		logical := stripPrefix(info.EntryPoint, "static/")
		hashed := stripPrefix(outputPath, "static/")
		manifest[logical] = hashed
	}

	manifestPath := filepath.Join("static", "dist", "manifest.json")
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("esbuild: wrote %d bundled entry points + manifest.json\n", len(manifest))
}

func stripPrefix(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}
