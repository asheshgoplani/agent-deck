package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `session start` / `session restart` of an archived session used to start it
// with archived_at still set: a live session hidden in the archived view,
// counted and woken as parked. Both now unarchive durably first, say so in
// text ("Unarchived session: <title>") and in --json ("unarchived": true), on
// the registry path (the default) and on the legacy handlers alike.
func TestSessionStartAndRestartOfArchivedSessionUnarchive(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, mode := range []struct {
		name string
		env  []string
	}{
		{"registry", nil},
		{"legacy", []string{envCoreRegistry + "=0"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "proj")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			title := "archived-start-" + mode.name
			stdout, stderr, code := runAgentDeckEnv(t, home, "", mode.env, "add", "-t", title, "-c", "bash", "--no-parent", "--json", work)
			if code != 0 {
				t.Fatalf("add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			var added struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
				t.Fatalf("add: no id in %q (%v)", stdout, err)
			}
			id := added.ID
			t.Cleanup(func() { runAgentDeckEnv(t, home, "", mode.env, "session", "stop", id) })

			archive := func() {
				t.Helper()
				if _, stderr, code := runAgentDeckEnv(t, home, "", mode.env, "session", "archive", id, "--json"); code != 0 {
					t.Fatalf("archive failed (exit %d): %s", code, stderr)
				}
				if !archivedFlag(t, home, id) {
					t.Fatalf("archive did not take effect for %s", id)
				}
			}

			// start --json
			archive()
			stdout, stderr, code = runAgentDeckEnv(t, home, "", mode.env, "session", "start", id, "--json")
			if code != 0 {
				t.Fatalf("start failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			var started map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &started); err != nil {
				t.Fatalf("parse start --json: %v\n%s", err, stdout)
			}
			if started["unarchived"] != true {
				t.Errorf("start --json = %s, want \"unarchived\": true", stdout)
			}
			if archivedFlag(t, home, id) {
				t.Errorf("session %s still archived after session start", id)
			}

			// restart, text output
			archive()
			stdout, stderr, code = runAgentDeckEnv(t, home, "", mode.env, "session", "restart", id, "--force")
			if code != 0 {
				t.Fatalf("restart failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if !strings.Contains(stdout, "Unarchived session: "+title) {
				t.Errorf("restart stdout = %q, want an \"Unarchived session: %s\" line", stdout, title)
			}
			if archivedFlag(t, home, id) {
				t.Errorf("session %s still archived after session restart", id)
			}

			// A start of a session that was never archived says nothing new.
			if _, stderr, code := runAgentDeckEnv(t, home, "", mode.env, "session", "stop", id); code != 0 {
				t.Fatalf("stop failed (exit %d): %s", code, stderr)
			}
			stdout, _, code = runAgentDeckEnv(t, home, "", mode.env, "session", "start", id, "--json")
			if code != 0 {
				t.Fatalf("plain start failed (exit %d): %s", code, stdout)
			}
			if strings.Contains(stdout, "unarchived") {
				t.Errorf("start --json of an active session = %s, want no unarchived key", stdout)
			}
		})
	}
}
