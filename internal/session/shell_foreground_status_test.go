package session

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// These tests cover the shell foreground-process status detection added so that
// a shell session running `yarn dev` / `mvn spring-boot:run` shows a running
// indicator instead of idle, while interactive programs (editors, pagers, ssh)
// keep showing idle.

func TestIsShellBinary(t *testing.T) {
	for _, s := range []string{
		"bash", "zsh", "sh", "fish", "dash", "ksh", "tcsh", "csh",
		"nu", "nushell", "pwsh", "powershell",
		"BASH", "Zsh", // case-insensitive
	} {
		assert.Truef(t, isShellBinary(s), "%q should be classified as a shell", s)
	}
	for _, s := range []string{"node", "java", "python", "ssh", "vim", "sleep", ""} {
		assert.Falsef(t, isShellBinary(s), "%q should not be classified as a shell", s)
	}
}

func TestIsInteractiveForegroundProgram(t *testing.T) {
	for _, c := range []string{
		"ssh", "mosh", "mosh-client", "et", "tmux", "screen", "zellij",
		"vi", "vim", "nvim", "nano", "emacs", "emacsclient", "helix", "hx", "micro", "kak",
		"less", "more", "most", "man", "bat",
		"top", "htop", "btop", "btm", "glances", "atop",
		"SSH", "Vim", // case-insensitive
	} {
		assert.Truef(t, isInteractiveForegroundProgram(c), "%q should be treated as interactive", c)
	}

	// REPLs/interpreters/servers must NOT be denylisted: they share a process
	// name with the long-running commands this feature targets (yarn dev -> node,
	// runserver -> python). Denylisting them would defeat the feature.
	for _, c := range []string{
		"node", "python", "python3", "ruby", "java", "go", "deno", "bun",
		"sleep", "make", "cargo", "gradle", "mvn", "",
	} {
		assert.Falsef(t, isInteractiveForegroundProgram(c),
			"%q must not be treated as interactive (would mask a running process)", c)
	}
}

func TestShellForegroundRunning(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"node dev server (yarn dev)", "node", true},
		{"java spring boot (mvn spring-boot:run)", "java", true},
		{"python runserver", "python", true},
		{"sleep / generic process", "sleep", true},
		{"idle bash prompt", "bash", false},
		{"idle zsh prompt", "zsh", false},
		{"ssh remote shell", "ssh", false},
		{"vim editor", "vim", false},
		{"less pager", "less", false},
		{"htop monitor", "htop", false},
		{"empty command", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst := NewInstance("fg-test", "/tmp")
			inst.Tool = "shell"
			inst.tmuxSession = tmux.NewSession("fg-test", "/tmp")
			tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
				inst.tmuxSession.Name: {CurrentCommand: tc.command},
			})
			assert.Equal(t, tc.want, inst.shellForegroundRunning())
		})
	}
}

// A cold pane-info cache (no RefreshPaneInfoCache / seed) must preserve the
// historical "shell maps to idle" behavior — shellForegroundRunning returns
// false when the foreground command for the pane is unknown.
func TestShellForegroundRunning_ColdCacheReturnsFalse(t *testing.T) {
	inst := NewInstance("cold-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = tmux.NewSession("cold-test", "/tmp")
	// Intentionally no SeedPaneInfoCacheForTest: the cache holds no entry for
	// this session's unique name, so the lookup misses.
	assert.False(t, inst.shellForegroundRunning())
}

// Defensive: a nil tmuxSession must not panic.
func TestShellForegroundRunning_NilSession(t *testing.T) {
	inst := NewInstance("nil-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = nil
	assert.False(t, inst.shellForegroundRunning())
}
