package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2388: the web dialog and the TUI dialog read one model catalog.

// staticJSCatalog extracts `tool -> [value, ...]` from one of the exported
// tables in modelCatalog.js.
func staticJSCatalog(t *testing.T, table string) map[string][]string {
	t.Helper()
	data, err := embeddedStaticFiles.ReadFile("static/app/modelCatalog.js")
	if err != nil {
		t.Fatalf("read modelCatalog.js: %v", err)
	}
	src := string(data)
	start := strings.Index(src, "export const "+table+" = {")
	if start < 0 {
		t.Fatalf("modelCatalog.js has no %s", table)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("unterminated %s", table)
	}
	block := src[start : start+end]
	toolRe := regexp.MustCompile(`(?s)\n  (\w+): \[(.*?)\n  \]`)
	valueRe := regexp.MustCompile(`value: '([^']+)'`)
	out := map[string][]string{}
	for _, m := range toolRe.FindAllStringSubmatch(block, -1) {
		for _, v := range valueRe.FindAllStringSubmatch(m[2], -1) {
			out[m[1]] = append(out[m[1]], v[1])
		}
	}
	return out
}

// The web fallback tables must match the Go static catalog exactly, so the
// dialog shows the TUI's list even before /api/settings answers. Probing is
// off in test binaries, so session.* return the static catalog here.
func TestIssue2388_WebStaticCatalogMatchesTUI(t *testing.T) {
	models := staticJSCatalog(t, "MODEL_ID_CATALOG")
	for _, tool := range []string{"claude", "codex", "gemini", "opencode"} {
		if got, want := models[tool], session.KnownModelIDsForTool(tool); !slices.Equal(got, want) {
			t.Errorf("%s models: web %v, TUI %v", tool, got, want)
		}
	}
	efforts := staticJSCatalog(t, "REASONING_EFFORT_CATALOG")
	for _, tool := range []string{"claude", "codex"} {
		if got, want := efforts[tool], session.LaunchReasoningEffortsForTool(tool); !slices.Equal(got, want) {
			t.Errorf("%s efforts: web %v, TUI %v", tool, got, want)
		}
	}
}

func fetchSettingsModelCatalog(t *testing.T) map[string]ToolModelCatalog {
	t.Helper()
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/settings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d: %s", rr.Code, rr.Body.String())
	}
	var resp SettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.ModelCatalog
}

// /api/settings serves the same lists the TUI dialog reads, per picker tool.
func TestIssue2388_SettingsModelCatalogMatchesTUI(t *testing.T) {
	catalog := fetchSettingsModelCatalog(t)
	for _, tool := range []string{"claude", "codex", "gemini", "opencode"} {
		entry, ok := catalog[tool]
		if !ok {
			t.Fatalf("modelCatalog missing %s: %v", tool, catalog)
		}
		if !slices.Equal(entry.Models, session.KnownModelIDsForTool(tool)) {
			t.Errorf("%s models: settings %v, TUI %v", tool, entry.Models, session.KnownModelIDsForTool(tool))
		}
		if want := session.LaunchReasoningEffortsForTool(tool); !slices.Equal(entry.ReasoningEfforts, want) && len(want) > 0 {
			t.Errorf("%s efforts: settings %v, TUI %v", tool, entry.ReasoningEfforts, want)
		}
	}
	if _, ok := catalog["shell"]; ok {
		t.Error("shell has no models and must be omitted")
	}
}

// With a probe-capable fake codex installed, the settings payload carries the
// probed models and per-model efforts, identical to the TUI's view.
func TestIssue2388_SettingsModelCatalogCarriesProbe(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "models.json")
	models := `{"models":[{"slug":"gpt-7-nova","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"hyper"}]}]}`
	if err := os.WriteFile(fixture, []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\ncat '" + fixture + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGENTDECK_TEST_MODEL_PROBE", "1")
	session.ResetModelProbeMemo()
	t.Cleanup(session.ResetModelProbeMemo)

	entry := fetchSettingsModelCatalog(t)["codex"]
	if len(entry.Models) == 0 || entry.Models[0] != "gpt-7-nova" {
		t.Fatalf("codex models = %v, want probed gpt-7-nova first", entry.Models)
	}
	if !slices.Equal(entry.Models, session.KnownModelIDsForTool("codex")) {
		t.Fatalf("settings %v != TUI %v", entry.Models, session.KnownModelIDsForTool("codex"))
	}
	if !slices.Contains(entry.ReasoningEfforts, "hyper") {
		t.Fatalf("efforts = %v, want probed hyper", entry.ReasoningEfforts)
	}
	if !slices.Equal(entry.ModelEfforts["gpt-7-nova"], []string{"low", "hyper"}) {
		t.Fatalf("modelEfforts = %v", entry.ModelEfforts)
	}
}
