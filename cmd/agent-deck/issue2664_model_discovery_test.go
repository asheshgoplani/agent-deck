package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2664: the real binary's `launch --capabilities --json` (what a
// controller reads from a remote) reports the OpenCode catalog from
// `opencode models` and honors [models.<tool>] list / extra.
func TestIssue2664_CapabilitiesCarryOpenCodeProbeAndOverrides(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[models.opencode]\nextra = [\"local/qwen\"]\n\n[models.codex]\nlist = [\"nova-2\"]\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '1.19.3'; exit 0; fi\n" +
		"[ \"$1\" = models ] || exit 2\nprintf 'opencode/big-pickle\\nacme/nova-2\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A failing codex keeps the host's real one from being run for efforts.
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
	}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "launch", "--capabilities", "--json")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var catalog session.RemoteCreationCatalog
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	// This test process does not probe and has no config: the built-in list.
	builtin := session.KnownModelIDsForTool("opencode")
	for _, name := range []string{"opencode", "opencode2"} {
		models := capabilityTool(t, &catalog, name).Models
		want := append([]string{"local/qwen", "opencode/big-pickle", "acme/nova-2"}, builtin...)
		if !slices.Equal(models, want) {
			t.Fatalf("%s models = %v, want %v", name, models, want)
		}
	}
	if got := capabilityTool(t, &catalog, "codex").Models; !slices.Equal(got, []string{"nova-2"}) {
		t.Fatalf("codex models = %v, want the pinned list", got)
	}
}
