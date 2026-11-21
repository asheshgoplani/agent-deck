package tmux

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParsePaneDeadStatus covers the "#{pane_dead}|#{pane_dead_status}" parsing
// that lets status detection tell a clean one-shot exit (code 0) from a crash.
func TestParsePaneDeadStatus(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantCode int
		wantOK   bool
	}{
		{"clean exit 0", "1|0\n", 0, true},
		{"crash exit 1", "1|1", 1, true},
		{"crash exit 137", "1|137\n", 137, true},
		{"live pane", "0|", 0, false},
		{"live pane with stale status", "0|0", 0, false},
		{"dead pane, no remain-on-exit (empty status)", "1|", 0, false},
		{"dead pane, non-numeric status", "1|foo", 0, false},
		{"malformed, no separator", "1", 0, false},
		{"empty", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := parsePaneDeadStatus(tt.raw)
			if code != tt.wantCode || ok != tt.wantOK {
				t.Errorf("parsePaneDeadStatus(%q) = (%d, %v), want (%d, %v)",
					tt.raw, code, ok, tt.wantCode, tt.wantOK)
			}
		})
	}
}

// The probes must find the first pane when window 0 and pane 0 do not exist.
func TestPaneDeadProbes_NonZeroBaseIndex(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-g", "base-index", "1")
	ctl("set-option", "-g", "pane-base-index", "1")
	ctl("set-option", "-g", "remain-on-exit", "on")
	ctl("new-session", "-d", "-s", "based", "sh", "-c", "exit 7")
	require.Equal(t, "1.1", ctl("list-panes", "-t", "based", "-F", "#{window_index}.#{pane_index}"))
	waitPaneDead(t, ctl, "based")

	s := &Session{Name: "based", SocketName: socket}
	assertDeadWithExit7(t, s)
}

func TestPaneDeadProbes_FirstWindowNotZero(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-g", "remain-on-exit", "on")
	ctl("new-session", "-d", "-s", "based", "sh")
	ctl("new-window", "-d", "-t", "based:", "sh", "-c", "exit 7")
	ctl("kill-window", "-t", "based:0")
	require.Equal(t, "1.0", ctl("list-panes", "-t", "based", "-F", "#{window_index}.#{pane_index}"))
	waitPaneDead(t, ctl, "based")

	s := &Session{Name: "based", SocketName: socket}
	assertDeadWithExit7(t, s)
}

// A bare target prefix-matches, so "based" must not resolve to "basedx".
func TestPaneDeadProbes_ExactSessionMatch(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-g", "remain-on-exit", "on")
	ctl("new-session", "-d", "-s", "basedx", "sh", "-c", "exit 7")
	waitPaneDead(t, ctl, "basedx")

	s := &Session{Name: "based", SocketName: socket}
	assert.False(t, s.IsPaneDead(), "IsPaneDead read basedx")
	_, ok := s.PaneDeadExitStatus()
	assert.False(t, ok, "PaneDeadExitStatus read basedx")
}

func TestPaneDeadProbes_LiveSplitPane(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-g", "remain-on-exit", "on")
	ctl("new-session", "-d", "-s", "based", "sh", "-c", "sleep 0.5; exit 7")
	ctl("split-window", "-d", "-t", "=based:^", "sh")
	waitPaneDead(t, ctl, "based")
	require.Equal(t, "1\n0", ctl("list-panes", "-t", "=based:^", "-F", "#{pane_dead}"))

	s := &Session{Name: "based", SocketName: socket}
	assertDeadWithExit7(t, s)
}

func TestPaneDeadExitStatus_OneSpawn(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-g", "remain-on-exit", "on")
	ctl("new-session", "-d", "-s", "based", "sh", "-c", "exit 7")
	waitPaneDead(t, ctl, "based")

	s := &Session{Name: "based", SocketName: socket}
	before := SubprocessStarts()
	_, _ = s.PaneDeadExitStatus()
	assert.Equal(t, int64(1), SubprocessStarts()-before)
}

func waitPaneDead(t *testing.T, ctl func(args ...string) string, session string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.HasPrefix(ctl("list-panes", "-t", "="+session, "-F", "#{pane_dead}"), "1")
	}, 5*time.Second, 20*time.Millisecond)
	// Under load tmux 3.3a can leave the exited child unreaped until another server child exits.
	ctl("run-shell", "true")
	require.Eventually(t, func() bool {
		first, _, _ := strings.Cut(ctl("list-panes", "-t", "="+session, "-F", "#{pane_dead}|#{pane_dead_status}"), "\n")
		return first != "1|"
	}, 5*time.Second, 20*time.Millisecond)
}

func assertDeadWithExit7(t *testing.T, s *Session) {
	t.Helper()
	assert.True(t, s.IsPaneDead(), "IsPaneDead")
	code, ok := s.PaneDeadExitStatus()
	assert.True(t, ok, "PaneDeadExitStatus ok")
	assert.Equal(t, 7, code, "PaneDeadExitStatus code")
}
