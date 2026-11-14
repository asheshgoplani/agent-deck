package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review M2: on a new install, `launch --json` into a folder agent-deck may
// not trust is not a clean success: the result carries a FOLDER_NOT_TRUSTED
// warning (and stderr keeps its note). An existing install reports none.
func TestLaunchJSON_FolderNotTrustedWarning(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, existing := range []bool{false, true} {
		name := "new install"
		if existing {
			name = "existing install"
		}
		t.Run(name, func(t *testing.T) {
			home, env := installDefaultsCLIHome(t)
			binDir := filepath.Join(home, "bin")
			tmuxDir := filepath.Join(home, "tmux")
			project := filepath.Join(home, "code", "proj")
			for _, d := range []string{binDir, tmuxDir, project} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if existing {
				cfg := filepath.Join(home, ".config", "agent-deck", "config.toml")
				if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg, []byte("theme = \"dark\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			env = append(env,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMUX_TMPDIR="+tmuxDir,
			)
			out, stderr, code := runAgentDeckEnv(t, home, "", env, "launch", project,
				"--title", "trust-warn", "--cmd", "claude", "--no-parent", "--no-wait", "--no-assert-done", "--json")
			t.Cleanup(func() { runAgentDeckEnv(t, home, "", env, "session", "stop", "trust-warn") })
			if code != 0 {
				t.Fatalf("launch: exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
			}
			var res struct {
				Warnings []struct {
					Code   string `json:"code"`
					Folder string `json:"folder"`
					Hint   string `json:"hint"`
				} `json:"warnings"`
			}
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("launch --json output is not JSON: %v\n%s", err, out)
			}
			if existing {
				if len(res.Warnings) != 0 {
					t.Errorf("existing install: unexpected warnings %+v", res.Warnings)
				}
				return
			}
			if len(res.Warnings) != 1 || res.Warnings[0].Code != "FOLDER_NOT_TRUSTED" ||
				!strings.HasSuffix(res.Warnings[0].Folder, filepath.Join("code", "proj")) ||
				!strings.Contains(res.Warnings[0].Hint, "--trust-folder") {
				t.Errorf("new install: warnings = %+v, want one FOLDER_NOT_TRUSTED for the project\n%s", res.Warnings, out)
			}
			if !strings.Contains(stderr, "did not mark") {
				t.Errorf("stderr note missing:\n%s", stderr)
			}
		})
	}
}
