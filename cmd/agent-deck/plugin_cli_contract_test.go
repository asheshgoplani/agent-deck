package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginCLIFixture is an isolated HOME with a one-plugin catalog and three
// unstarted claude sessions: "alpha" and two sessions titled "dup".
func pluginCLIFixture(t *testing.T) (home, alphaID string) {
	t.Helper()
	home = t.TempDir()
	writeTestConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	add := func(title, dir string) string {
		project := filepath.Join(home, dir)
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		out, stderr, code := runAgentDeck(t, home, "add", project, "-t", title, "-c", "claude", "--no-parent", "--json")
		if code != 0 {
			t.Fatalf("add %s exit %d: %s", title, code, stderr)
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &row); err != nil || row.ID == "" {
			t.Fatalf("parse add %s: %v\n%s", title, err, out)
		}
		return row.ID
	}
	alphaID = add("alpha", "a")
	add("dup", "b")
	add("dup", "c")
	return home, alphaID
}

// `plugin attach` resolves sessions like `mcp attach` and `skill attach`:
// the short ID that `list` prints works, a missing session exits 2, and a
// title two sessions share is AMBIGUOUS instead of silently the first one.
func TestPluginCLI_ResolvesLikeOtherSessionCommands(t *testing.T) {
	home, alphaID := pluginCLIFixture(t)

	stdout, stderr, code := runAgentDeck(t, home, "plugin", "attach", alphaID[:8], "octopus", "--json")
	if code != 0 || !strings.Contains(stdout, "octopus") {
		t.Fatalf("short ID prefix: exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	stdout, stderr, code = runAgentDeck(t, home, "plugin", "attach", "dup", "octopus", "--json")
	if code != 2 || !strings.Contains(stdout, ErrCodeAmbiguous) {
		t.Fatalf("duplicate title: exit %d, want 2 with %s\nstdout: %s\nstderr: %s", code, ErrCodeAmbiguous, stdout, stderr)
	}

	_, stderr, code = runAgentDeck(t, home, "plugin", "attached", "no-such-session")
	if code != 2 {
		t.Fatalf("missing session: exit %d, want 2\nstderr: %s", code, stderr)
	}
}

// -q is documented for attach/detach, so the success line must honor it.
func TestPluginCLI_QuietSuppressesSuccessLine(t *testing.T) {
	home, _ := pluginCLIFixture(t)

	stdout, stderr, code := runAgentDeck(t, home, "plugin", "attach", "alpha", "octopus", "-q")
	if code != 0 {
		t.Fatalf("attach -q exit %d\nstderr: %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("attach -q printed to stdout: %q", stdout)
	}
}

// --json --restart must leave exactly one JSON document on stdout. The
// session's account slot is removed from config.toml after `add`, so the
// restart fails in ValidateAccount before any tmux work: nothing is started,
// and the test sees the restart-failure path. Neither the progress line nor
// the failure may be glued onto a success payload.
func TestPluginCLI_JSONRestartKeepsStdoutOneJSONDocument(t *testing.T) {
	home := t.TempDir()
	catalog := "[plugins.octopus]\nname = \"octopus\"\nsource = \"nyldn/claude-octopus\"\n"
	writeTestConfig(t, home, catalog+"[profiles.gone.claude]\nconfig_dir = \""+filepath.Join(home, "slot")+"\"\n")
	project := filepath.Join(home, "a")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeck(t, home, "add", project, "-t", "alpha", "-c", "claude", "--account", "gone", "--no-parent", "--json"); code != 0 {
		t.Fatalf("add exit %d: %s", code, stderr)
	}
	writeTestConfig(t, home, catalog)

	stdout, stderr, code := runAgentDeck(t, home, "plugin", "attach", "alpha", "octopus", "--json", "--restart")
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if code == 0 || doc["success"] != false {
		t.Fatalf("failed restart must report failure: exit %d, doc %v", code, doc)
	}
}
