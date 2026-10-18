package web

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2664: with a fake `opencode` that lists models, /api/settings serves
// the probed OpenCode catalog, identical to the TUI's view.
func TestIssue2664_SettingsModelCatalogCarriesOpenCodeProbe(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '1.19.3'; exit 0; fi\nprintf 'opencode/big-pickle\\nacme/nova-2\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGENTDECK_TEST_MODEL_PROBE", "1")
	session.ResetModelProbeMemo()
	t.Cleanup(session.ResetModelProbeMemo)

	entry := fetchSettingsModelCatalog(t)["opencode"]
	if want := []string{"opencode/big-pickle", "acme/nova-2"}; len(entry.Models) < 2 || !slices.Equal(entry.Models[:2], want) {
		t.Fatalf("opencode models = %v, want probed prefix %v", entry.Models, want)
	}
	if !slices.Equal(entry.Models, session.KnownModelIDsForTool("opencode")) {
		t.Fatalf("settings %v != TUI %v", entry.Models, session.KnownModelIDsForTool("opencode"))
	}
}
