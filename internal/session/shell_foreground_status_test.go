package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// These tests cover the shell foreground-process status detection added so that
// a shell session running `yarn dev` / `mvn spring-boot:run` shows a running
// indicator instead of idle, while interactive programs (editors, pagers, ssh)
// keep showing idle. The feature is opt-in via [status] shell_running_indicator
// (default false) and only fresh pane-cache snapshots may promote idle→running.

// setShellRunningIndicatorForTest writes a config.toml with the
// shell_running_indicator flag into the TestMain-isolated HOME and clears the
// user-config cache. The previous file contents (if any) are restored on
// cleanup. TestMain's IsolateHome guarantees this never touches the real
// ~/.agent-deck (2026-06-04 data-loss incident, S5).
func setShellRunningIndicatorForTest(t testing.TB, enabled bool) {
	t.Helper()

	path, err := GetUserConfigPath()
	require.NoError(t, err)

	prev, prevErr := os.ReadFile(path)
	hadPrev := prevErr == nil

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := "[status]\nshell_running_indicator = false\n"
	if enabled {
		content = "[status]\nshell_running_indicator = true\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	ClearUserConfigCache()

	t.Cleanup(func() {
		if hadPrev {
			_ = os.WriteFile(path, prev, 0o644)
		} else {
			_ = os.Remove(path)
		}
		ClearUserConfigCache()
	})
}

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
	setShellRunningIndicatorForTest(t, true)

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

// The indicator is opt-in. With no config (or the flag explicitly false) a
// running foreground process must NOT promote the session — this preserves the
// historical "shell maps to idle" default for everyone who has not opted in
// (a shell sitting in psql/a REPL/fzf would otherwise read running).
func TestShellForegroundRunning_OptInDefaultOff(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(t *testing.T)
	}{
		{"no config file (defaults)", func(t *testing.T) { t.Helper(); ClearUserConfigCache() }},
		{"flag explicitly false", func(t *testing.T) { t.Helper(); setShellRunningIndicatorForTest(t, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.configure(t)
			inst := NewInstance("optin-test", "/tmp")
			inst.Tool = "shell"
			inst.tmuxSession = tmux.NewSession("optin-test", "/tmp")
			tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
				inst.tmuxSession.Name: {CurrentCommand: "node"},
			})
			assert.False(t, inst.shellForegroundRunning(),
				"foreground process must not promote status unless shell_running_indicator is opted in")
		})
	}
}

// A cold pane-info cache (no RefreshPaneInfoCache / seed) must preserve the
// historical "shell maps to idle" behavior — shellForegroundRunning returns
// false when the foreground command for the pane is unknown.
func TestShellForegroundRunning_ColdCacheReturnsFalse(t *testing.T) {
	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("cold-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = tmux.NewSession("cold-test", "/tmp")
	// Intentionally no SeedPaneInfoCacheForTest: the cache holds no entry for
	// this session's unique name, so the lookup misses.
	assert.False(t, inst.shellForegroundRunning())
}

// A warm-but-stale cache must not promote idle→running: a snapshot past the
// freshness TTL can describe a command that has since completed.
func TestShellForegroundRunning_StaleCacheReturnsFalse(t *testing.T) {
	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("stale-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = tmux.NewSession("stale-test", "/tmp")
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "node"},
	})
	require.True(t, inst.shellForegroundRunning(), "fresh seed should promote")

	tmux.ExpirePaneInfoCacheForTest(t)
	assert.False(t, inst.shellForegroundRunning(),
		"stale pane info must not drive the idle→running transition")
}

// A dead pane (#{pane_dead}) means the foreground command already exited —
// never promote from it, no matter how fresh the snapshot is.
func TestShellForegroundRunning_DeadPaneReturnsFalse(t *testing.T) {
	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("dead-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = tmux.NewSession("dead-test", "/tmp")
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "node", Dead: true},
	})
	assert.False(t, inst.shellForegroundRunning())
}

// A snapshot taken BEFORE the instance's last start describes a previous
// same-name session (kill + recreate within the freshness TTL), not this one.
// It must not promote the freshly started session to running.
func TestShellForegroundRunning_SnapshotPredatingStartReturnsFalse(t *testing.T) {
	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("recreate-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = tmux.NewSession("recreate-test", "/tmp")
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "node"},
	})

	// Control: snapshot taken after the last start may promote.
	inst.lastStartTime = time.Now().Add(-time.Minute)
	require.True(t, inst.shellForegroundRunning())

	// The session was (re)started after the snapshot was taken: reject.
	inst.lastStartTime = time.Now().Add(time.Minute)
	assert.False(t, inst.shellForegroundRunning(),
		"pane snapshot predating the session start belongs to the previous same-name session")
}

// Defensive: a nil tmuxSession must not panic.
func TestShellForegroundRunning_NilSession(t *testing.T) {
	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("nil-test", "/tmp")
	inst.Tool = "shell"
	inst.tmuxSession = nil
	assert.False(t, inst.shellForegroundRunning())
}

// End-to-end coverage of the changed UpdateStatus path (not just the
// classifiers): a real tmux-backed shell session whose tmux-derived status is
// idle/waiting gets promoted to StatusRunning only when (a) the opt-in flag is
// set AND (b) the pane-info cache holds a fresh non-interactive foreground
// command — and falls back to StatusIdle when the cache goes stale or the
// flag is off. Mirrors the issue-953 test setup: `sleep 60` keeps the pane
// quiet so the tmux status settles on waiting/idle past the 1.5s grace period.
func TestUpdateStatus_ShellForegroundPromotion(t *testing.T) {
	skipIfNoTmuxBinary(t)

	setShellRunningIndicatorForTest(t, true)

	inst := NewInstance("test-shell-fg-e2e", "/tmp")
	inst.Tool = "shell"
	inst.Command = "sleep 60"

	require.NoError(t, inst.Start())
	defer func() { _ = inst.Kill() }()

	// Wait past the 1.5s grace period so UpdateStatus does real detection.
	time.Sleep(2 * time.Second)

	// Fresh cache + flag on: the foreground process promotes idle→running.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "node"},
	})
	require.NoError(t, inst.UpdateStatus())
	assert.Equal(t, StatusRunning, inst.GetStatusThreadSafe(),
		"fresh foreground command with opt-in flag should report running")

	// Stale cache: the promotion must drop back to the historical idle.
	tmux.ExpirePaneInfoCacheForTest(t)
	require.NoError(t, inst.UpdateStatus())
	assert.Equal(t, StatusIdle, inst.GetStatusThreadSafe(),
		"stale pane info must not keep the session promoted")

	// Interactive foreground program: stays idle even with a fresh cache.
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "vim"},
	})
	require.NoError(t, inst.UpdateStatus())
	assert.Equal(t, StatusIdle, inst.GetStatusThreadSafe(),
		"interactive foreground programs must not promote")

	// Flag off: identical fresh cache, no promotion — the gate holds the
	// historical shell→idle default for non-opted-in users.
	setShellRunningIndicatorForTest(t, false)
	tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
		inst.tmuxSession.Name: {CurrentCommand: "node"},
	})
	require.NoError(t, inst.UpdateStatus())
	assert.Equal(t, StatusIdle, inst.GetStatusThreadSafe(),
		"without the opt-in flag the running indicator must stay off")
}
