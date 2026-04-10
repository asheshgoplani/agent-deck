package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests reference symbols that do NOT yet exist when the failing
// spec is first committed:
//   - LoadAssets(manifestPath string) (*Assets, error)
//   - (*Assets).ResolveAsset(logical string) string
//   - (*Assets).SubstitutePlaceholders(template string) string
// Task 2 of plan 08-05 creates them. Until then, these tests fail to
// compile — which is the desired RED state for TDD.

func TestAssets_ResolveDevFallback(t *testing.T) {
	// No manifest: dev mode. ResolveAsset should return /static/<logical>
	dir := t.TempDir()
	a, err := LoadAssets(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadAssets should tolerate missing manifest in dev mode, got error: %v", err)
	}
	got := a.ResolveAsset("app/main.js")
	want := "/static/app/main.js"
	if got != want {
		t.Errorf("dev fallback: got %q, want %q", got, want)
	}
}

func TestAssets_ResolveProdManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestJSON := `{"app/main.js": "dist/main.a1b2c3.js"}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAssets(manifestPath)
	if err != nil {
		t.Fatalf("LoadAssets: %v", err)
	}
	got := a.ResolveAsset("app/main.js")
	want := "/static/dist/main.a1b2c3.js"
	if got != want {
		t.Errorf("prod manifest: got %q, want %q", got, want)
	}
}

func TestAssets_RollbackEnvVar(t *testing.T) {
	t.Setenv("AGENTDECK_WEB_BUNDLE", "0")
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestJSON := `{"app/main.js": "dist/main.a1b2c3.js"}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAssets(manifestPath)
	if err != nil {
		t.Fatalf("LoadAssets: %v", err)
	}
	got := a.ResolveAsset("app/main.js")
	want := "/static/app/main.js"
	if got != want {
		t.Errorf("rollback env var should force dev mode: got %q, want %q", got, want)
	}
}

func TestAssets_SubstituteSinglePlaceholder(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestJSON := `{"app/main.js": "dist/main.a1b2c3.js"}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAssets(manifestPath)
	if err != nil {
		t.Fatalf("LoadAssets: %v", err)
	}

	input := `<script type="module" src="{{ASSET:app/main.js}}"></script>`
	got := a.SubstitutePlaceholders(input)
	want := `<script type="module" src="/static/dist/main.a1b2c3.js"></script>`
	if got != want {
		t.Errorf("substitute single: got %q, want %q", got, want)
	}
}

func TestAssets_SubstituteMultiplePlaceholders(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	manifestJSON := `{
		"app/main.js": "dist/main.a1b2c3.js",
		"app/styles.css": "dist/styles.9f8e7d.css",
		"app/vendor.js": "dist/vendor.abc123.js"
	}`
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAssets(manifestPath)
	if err != nil {
		t.Fatalf("LoadAssets: %v", err)
	}

	input := `
<link rel="stylesheet" href="{{ASSET:app/styles.css}}">
<script src="{{ASSET:app/vendor.js}}"></script>
<script type="module" src="{{ASSET:app/main.js}}"></script>
`
	got := a.SubstitutePlaceholders(input)
	if !strings.Contains(got, "/static/dist/main.a1b2c3.js") {
		t.Errorf("missing main.js replacement in %q", got)
	}
	if !strings.Contains(got, "/static/dist/styles.9f8e7d.css") {
		t.Errorf("missing styles.css replacement in %q", got)
	}
	if !strings.Contains(got, "/static/dist/vendor.abc123.js") {
		t.Errorf("missing vendor.js replacement in %q", got)
	}
	if strings.Contains(got, "{{ASSET:") {
		t.Errorf("found unreplaced placeholder in %q", got)
	}
}
