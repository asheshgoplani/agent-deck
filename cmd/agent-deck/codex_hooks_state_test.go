package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A codex session made through `add` / `remote add` has no notify hook until
// `codex-hooks install` is run in its CODEX_HOME, and until then its light is
// content detection only. That state must be visible, not silent: `session
// show` and `doctor` say so, and a remote session's hook state is unknown
// (its CODEX_HOME lives on the other host).

func TestCodexHooksStateForConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		exists  bool
		want    string
	}{
		{"no config file", "", false, codexHooksNotInstalled},
		{"config without notify", "model = \"gpt-5\"\n", true, codexHooksNotInstalled},
		{"managed block", codexNotifyMarkerBegin + "\n" + codexNotifyLine + "\n" + codexNotifyMarkerEnd + "\n", true, codexHooksInstalled},
		{"exact line without markers", codexNotifyLine + "\n", true, codexHooksInstalled},
		{"legacy notify table", "[notify]\nprogram = [\"agent-deck\", \"codex-notify\"]\n", true, codexHooksLegacy},
		{"custom notify", "notify = [\"my-notifier\"]\n", true, codexHooksCustom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if tc.exists {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := codexHooksStateForConfig(path); got != tc.want {
				t.Fatalf("codexHooksStateForConfig = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexHooksStateForInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	local := session.NewInstanceWithTool("codex-local", home, "codex")
	state, configPath := codexHooksStateForInstance(local)
	if state != codexHooksNotInstalled {
		t.Fatalf("local codex without a config: state = %q, want %q", state, codexHooksNotInstalled)
	}
	if want := filepath.Join(home, ".codex", "config.toml"); configPath != want {
		t.Fatalf("config path = %q, want %q", configPath, want)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(codexNotifyLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state, _ := codexHooksStateForInstance(local); state != codexHooksInstalled {
		t.Fatalf("after install: state = %q, want %q", state, codexHooksInstalled)
	}

	remote := session.NewInstanceWithTool("codex-remote", home, "codex")
	remote.SSHHost = "g14"
	if state, _ := codexHooksStateForInstance(remote); state != codexHooksUnknown {
		t.Fatalf("remote codex: state = %q, want %q (CODEX_HOME is on the remote host)", state, codexHooksUnknown)
	}

	claude := session.NewInstanceWithTool("claude", home, "claude")
	if state, _ := codexHooksStateForInstance(claude); state != "" {
		t.Fatalf("non-codex tool: state = %q, want empty", state)
	}
}

func TestCodexHooksLine(t *testing.T) {
	cases := map[string]string{
		codexHooksNotInstalled: "hooks not installed (content detection only)",
		codexHooksInstalled:    "hooks installed",
		codexHooksLegacy:       "hooks legacy notify table",
		codexHooksCustom:       "hooks custom notify",
		codexHooksUnknown:      "hooks unknown",
	}
	for state, want := range cases {
		if got := codexHooksLine(state, "/x/config.toml"); got[:len(want)] != want {
			t.Fatalf("codexHooksLine(%q) = %q, want prefix %q", state, got, want)
		}
	}
	if got := codexHooksLine(codexHooksNotInstalled, "/x/config.toml"); !containsAll(got, "CODEX_HOME=/x", "codex-hooks install") {
		t.Fatalf("not-installed line must name the install command for that CODEX_HOME: %q", got)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
