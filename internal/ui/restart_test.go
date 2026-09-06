package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newRestartTestHome(t *testing.T) *Home {
	t.Helper()
	h := NewHome()
	h.initialLoading = false
	h.width, h.height = 80, 24
	h.binaryWatch = newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	return h
}

func assertRestartBlocked(t *testing.T, h *Home, wantReason string) {
	t.Helper()
	_, cmd := h.tryRestartDeck()
	if h.restartRequested || h.isQuitting {
		t.Fatalf("restart should be refused (%s), got requested=%v quitting=%v", wantReason, h.restartRequested, h.isQuitting)
	}
	if cmd != nil {
		t.Fatalf("refused restart must not schedule a quit command")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "restart blocked") || !strings.Contains(h.err.Error(), wantReason) {
		t.Fatalf("footer error = %v, want it to mention %q", h.err, wantReason)
	}
	if _, ok := h.RestartTarget(); ok {
		t.Fatal("RestartTarget must stay unset after a refused restart")
	}
}

// TestRestartDeck_RefusedWhileDialogOpen pins the modal guard: with any
// overlay up the key only produces a footer message.
func TestRestartDeck_RefusedWhileDialogOpen(t *testing.T) {
	h := newRestartTestHome(t)
	h.jumpMode = true
	assertRestartBlocked(t, h, "close the open dialog")

	h = newRestartTestHome(t)
	h.insertMode = true
	assertRestartBlocked(t, h, "close the open dialog")
}

// TestRestartDeck_RefusedWhileSessionActionInFlight pins the in-flight
// guard for every tracked action map plus a tmux attach in progress.
func TestRestartDeck_RefusedWhileSessionActionInFlight(t *testing.T) {
	arm := map[string]func(h *Home){
		"launching":     func(h *Home) { h.launchingSessions["s"] = time.Now() },
		"resuming":      func(h *Home) { h.resumingSessions["s"] = time.Now() },
		"forking":       func(h *Home) { h.forkingSessions["s"] = time.Now() },
		"setup running": func(h *Home) { h.setupRunningSessions["s"] = time.Now() },
		"creating":      func(h *Home) { h.creatingSessions["s"] = &CreatingSession{} },
		"remote restart": func(h *Home) {
			h.remoteRestarting["op"] = struct{}{}
		},
		"attaching": func(h *Home) { h.isAttaching.Store(true) },
	}
	for name, fn := range arm {
		t.Run(name, func(t *testing.T) {
			h := newRestartTestHome(t)
			fn(h)
			assertRestartBlocked(t, h, "session action is still running")
		})
	}
}

// TestRestartDeck_ArmsQuitSequence pins the happy path: a clean home screen
// arms the restart, shows the shutdown splash and schedules the quit tick,
// and RestartTarget hands main() the fingerprinted executable path.
func TestRestartDeck_ArmsQuitSequence(t *testing.T) {
	h := newRestartTestHome(t)
	_, cmd := h.tryRestartDeck()
	if !h.restartRequested || !h.isQuitting {
		t.Fatalf("requested=%v quitting=%v, want both true", h.restartRequested, h.isQuitting)
	}
	if cmd == nil {
		t.Fatal("expected the quit sequence to be scheduled")
	}
	exe, ok := h.RestartTarget()
	if !ok || exe != "/bin/agent-deck" {
		t.Fatalf("RestartTarget = %q, %v; want the fingerprinted path", exe, ok)
	}
	if !strings.Contains(h.View(), "Restarting") {
		t.Fatal("splash should say Restarting while a restart is armed")
	}
	// A second press while the sequence runs is a no-op with a message.
	_, cmd = h.tryRestartDeck()
	if cmd != nil || h.err == nil || !strings.Contains(h.err.Error(), "already in progress") {
		t.Fatalf("second press: cmd=%v err=%v", cmd, h.err)
	}
}

// TestRestartDeck_KeyRoutingHonorsRebinding pins that the default ctrl+t
// reaches the handler and that a rebound restart_deck key moves it.
func TestRestartDeck_KeyRoutingHonorsRebinding(t *testing.T) {
	h := newRestartTestHome(t)
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	if !h.restartRequested {
		t.Fatal("default ctrl+t should request a restart")
	}

	h = newRestartTestHome(t)
	h.hotkeys = resolveHotkeys(map[string]string{"restart_deck": "ctrl+y"})
	h.hotkeyLookup, h.blockedHotkeys = buildHotkeyLookup(h.hotkeys)
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyCtrlT})
	if h.restartRequested {
		t.Fatal("ctrl+t must be inert once restart_deck is rebound")
	}
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if !h.restartRequested {
		t.Fatal("rebound ctrl+y should request a restart")
	}
}

// TestRestartTarget_UnsetWithoutRequest pins that main() never execs unless
// the user pressed the key.
func TestRestartTarget_UnsetWithoutRequest(t *testing.T) {
	h := &Home{}
	if exe, ok := h.RestartTarget(); ok || exe != "" {
		t.Fatalf("RestartTarget = %q, %v on a fresh Home", exe, ok)
	}
	if err := ExecSelf(""); err == nil {
		t.Fatal("ExecSelf with an empty path must fail instead of exec'ing")
	}
}
