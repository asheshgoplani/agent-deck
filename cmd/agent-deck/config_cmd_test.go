package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigShowEffective exercises `agent-deck config show --effective` end
// to end in a subprocess (issue #2093): a workspace-parent .agent-deck/config.toml
// should apply to a sibling checkout beneath it, with the source correctly
// attributed, and an unknown key in a dir-local file should be refused.
func TestConfigShowEffective(t *testing.T) {
	if os.Getenv("AGENT_DECK_CONFIG_SHOW_HELPER") != "" {
		args := os.Args
		for i, a := range args {
			if a == "--" {
				handleConfig("_test", args[i+1:])
				os.Exit(0)
			}
		}
		os.Exit(2)
	}

	home := t.TempDir()
	workspace := filepath.Join(home, "projects", "example")
	main := filepath.Join(workspace, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	agentDeckDir := filepath.Join(workspace, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"),
		[]byte("[worktree]\ndefault_location = \"sibling\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runHelper := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		full := append([]string{"-test.run=^TestConfigShowEffective$", "--"}, args...)
		cmd := exec.Command(os.Args[0], full...)
		cmd.Env = append(os.Environ(),
			"AGENT_DECK_CONFIG_SHOW_HELPER=1",
			"HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
			"XDG_DATA_HOME="+filepath.Join(home, "data"),
			"XDG_STATE_HOME="+filepath.Join(home, "state"),
		)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("text shows sibling workspace-parent override", func(t *testing.T) {
		out, err := runHelper(t, "show", "--effective", main)
		if err != nil {
			t.Fatalf("config show --effective failed: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, "sibling") {
			t.Errorf("output %q does not show the workspace-parent default_location override", out)
		}
		if !strings.Contains(out, filepath.Join(agentDeckDir, "config.toml")) {
			t.Errorf("output %q does not name the source file", out)
		}
	})

	t.Run("json output is machine readable", func(t *testing.T) {
		out, err := runHelper(t, "show", "--effective", "--json", main)
		if err != nil {
			t.Fatalf("config show --effective --json failed: %v\noutput:\n%s", err, out)
		}
		var decoded struct {
			Path     string            `json:"path"`
			Worktree map[string]string `json:"worktree"`
			Sources  map[string]string `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", out, err)
		}
		if decoded.Worktree["default_location"] != "sibling" {
			t.Errorf("worktree.default_location = %q, want sibling", decoded.Worktree["default_location"])
		}
		if decoded.Sources["default_location"] != filepath.Join(agentDeckDir, "config.toml") {
			t.Errorf("sources.default_location = %q, want the workspace-parent file", decoded.Sources["default_location"])
		}
	})

	t.Run("unknown key in dir-local config is refused", func(t *testing.T) {
		badDir := filepath.Join(main, ".agent-deck")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(badDir, "config.toml"),
			[]byte("[worktree]\nauto_cleanup = false\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(badDir)

		out, err := runHelper(t, "show", "--effective", main)
		if err == nil {
			t.Fatalf("expected config show --effective to fail on an unknown key; output:\n%s", out)
		}
		if !strings.Contains(out, "auto_cleanup") {
			t.Errorf("error output %q does not name the offending key", out)
		}
	})
}
