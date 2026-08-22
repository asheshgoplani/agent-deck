package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupHooksHelpTest sandboxes HOME so a regression that actually installs
// writes into a throwaway dir instead of the developer's real agent config.
func setupHooksHelpTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

// assertNoClaudeHooksInstalled fails when the help request had side effects.
func assertNoClaudeHooksInstalled(t *testing.T, home string) {
	t.Helper()
	configDir := filepath.Join(home, ".claude")
	if session.CheckClaudeHooksInstalled(configDir) {
		t.Fatalf("`--help` installed Claude Code hooks into %s", configDir)
	}
	if _, err := os.Stat(filepath.Join(configDir, "settings.json")); err == nil {
		t.Fatalf("`--help` wrote %s", filepath.Join(configDir, "settings.json"))
	}
}

// The reported repro: `hooks install --help` must describe itself, not install.
func TestHooksInstallHelpDoesNotInstall(t *testing.T) {
	home := setupHooksHelpTest(t)

	out := captureStdout(t, func() { handleHooks([]string{"install", "--help"}) })

	if !strings.Contains(out, "Usage: agent-deck hooks") {
		t.Fatalf("expected usage output; got:\n%s", out)
	}

	assertNoClaudeHooksInstalled(t, home)
}

func TestHooksBareHelpPrintsUsageWithoutSideEffects(t *testing.T) {
	home := setupHooksHelpTest(t)

	out := captureStdout(t, func() { handleHooks([]string{"--help"}) })

	if !strings.Contains(out, "Usage: agent-deck hooks") {
		t.Fatalf("expected usage output; got:\n%s", out)
	}
	assertNoClaudeHooksInstalled(t, home)
}

// The four sibling handlers already handled bare `--help`; the guard must
// also cover flags AFTER the subcommand, which previously fell through to
// the install path on every one of them.
func TestSiblingHooksInstallHelpDoesNotInstall(t *testing.T) {
	cases := []struct {
		name    string
		run     func(args []string)
		checkFn func(configDir string) bool
		dirName string
	}{
		{"codex", handleCodexHooks, nil, ".codex"},
		{"cursor", handleCursorHooks, session.CheckCursorHooksInstalled, ".cursor"},
		{"gemini", handleGeminiHooks, nil, ".gemini"},
		{"hermes", handleHermesHooks, nil, ".hermes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := setupHooksHelpTest(t)

			out := captureStdout(t, func() { tc.run([]string{"install", "--help"}) })

			if !strings.Contains(out, "Usage: agent-deck") {
				t.Fatalf("expected usage output; got:\n%s", out)
			}
			if tc.checkFn != nil {
				configDir := filepath.Join(home, tc.dirName)
				if tc.checkFn(configDir) {
					t.Fatalf("`--help` installed hooks into %s", configDir)
				}
			}
		})
	}
}
