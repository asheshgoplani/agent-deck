package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
	if err := ExecSelf("", RestartHandoff{}); err == nil {
		t.Fatal("ExecSelf with an empty path must fail instead of exec'ing")
	}
}

// newAutoRestartTestHome is a clean home with a newer build already on
// disk and the default settings (auto_restart on).
func newAutoRestartTestHome(t *testing.T) *Home {
	t.Helper()
	stubUpdateSettings(t, session.UpdateSettings{})
	h := newRestartTestHome(t)
	h.binaryWatch.observe(fpAt(2, 2))
	h.binaryWatch.recordProbe(fpAt(2, 2), "1.16.1", nil)
	return h
}

// TestAutoRestart_ArmsQuitWhenIdle pins the no-key path: with a newer
// build on disk and nothing blocking, a tick arms the same restart the
// key would.
func TestAutoRestart_ArmsQuitWhenIdle(t *testing.T) {
	h := newAutoRestartTestHome(t)
	cmd := h.maybeAutoRestart()
	if cmd == nil || !h.restartRequested || !h.isQuitting {
		t.Fatalf("cmd=%v requested=%v quitting=%v, want the quit sequence armed", cmd, h.restartRequested, h.isQuitting)
	}
	if exe, ok := h.RestartTarget(); !ok || exe != "/bin/agent-deck" {
		t.Fatalf("RestartTarget = %q, %v", exe, ok)
	}
	if h.err != nil {
		t.Fatalf("auto restart must not set a footer error, got %v", h.err)
	}
	// The next tick is a no-op while the sequence runs.
	if again := h.maybeAutoRestart(); again != nil {
		t.Fatal("restart must not be armed twice")
	}
}

// TestAutoRestart_WaitsQuietlyWhileBlocked pins that a blocked restart
// neither errors nor gives up: the banner stays and the next idle tick
// restarts.
func TestAutoRestart_WaitsQuietlyWhileBlocked(t *testing.T) {
	h := newAutoRestartTestHome(t)
	h.jumpMode = true
	for i := 0; i < 3; i++ {
		if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
			t.Fatalf("tick %d: blocked restart must wait", i)
		}
	}
	if h.err != nil {
		t.Fatalf("waiting must not show a footer error, got %v", h.err)
	}
	if !strings.Contains(h.renderUpdateBannerText(), "restarting when idle (ctrl+t now)") {
		t.Fatalf("banner = %q, want the restarting-when-idle wording", h.renderUpdateBannerText())
	}
	h.jumpMode = false
	if cmd := h.maybeAutoRestart(); cmd == nil || !h.restartRequested {
		t.Fatal("once unblocked the restart must be armed")
	}

	h = newAutoRestartTestHome(t)
	h.launchingSessions["s"] = time.Now()
	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested || h.err != nil {
		t.Fatalf("session action in flight: cmd=%v requested=%v err=%v", cmd, h.restartRequested, h.err)
	}
}

// TestAutoRestart_RespectsSettingAndInstallState pins auto_restart=false
// (banner keeps the manual wording, tick never restarts) and that nothing
// happens while the file on disk is still the running build.
func TestAutoRestart_RespectsSettingAndInstallState(t *testing.T) {
	h := newAutoRestartTestHome(t)
	stubUpdateSettings(t, session.UpdateSettings{AutoRestart: boolPtr(false)})
	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
		t.Fatal("auto_restart=false must never restart on its own")
	}
	if got := h.renderUpdateBannerText(); !strings.Contains(got, "press ctrl+t to restart agent-deck") {
		t.Fatalf("banner with auto_restart off = %q", got)
	}
	// The key still works.
	if _, cmd := h.tryRestartDeck(); cmd == nil || !h.restartRequested {
		t.Fatal("manual restart must still work with auto_restart off")
	}

	stubUpdateSettings(t, session.UpdateSettings{})
	h = newRestartTestHome(t)
	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
		t.Fatal("no newer build on disk: nothing to restart into")
	}
}

// TestRestartEnv_RoundTrip pins the hand-off: stale copies are dropped,
// both values survive, and parse reads back exactly what build wrote.
func TestRestartEnv_RoundTrip(t *testing.T) {
	env := buildRestartEnv([]string{"PATH=/bin", restartSelectEnv + "=stale", restartedFromEnv + "=0.0.1"},
		RestartHandoff{SelectedID: "sess-42", OldVersion: "1.16.0"})
	want := []string{"PATH=/bin", restartSelectEnv + "=sess-42", restartedFromEnv + "=1.16.0"}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env = %q, want %q", env, want)
	}
	lookup := func(key string) string {
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
	if got := parseRestartEnv(lookup); got != (RestartHandoff{SelectedID: "sess-42", OldVersion: "1.16.0"}) {
		t.Fatalf("parseRestartEnv = %+v", got)
	}
	// Nothing selected: only the version travels.
	env = buildRestartEnv([]string{"A=1"}, RestartHandoff{OldVersion: "1.16.0"})
	if len(env) != 2 || env[1] != restartedFromEnv+"=1.16.0" {
		t.Fatalf("env without selection = %q", env)
	}
	// The process-level consume reads and unsets both.
	t.Setenv(restartSelectEnv, "sess-7")
	t.Setenv(restartedFromEnv, "1.15.0")
	if got := consumeRestartEnv(); got != (RestartHandoff{SelectedID: "sess-7", OldVersion: "1.15.0"}) {
		t.Fatalf("consumeRestartEnv = %+v", got)
	}
	if os.Getenv(restartSelectEnv) != "" || os.Getenv(restartedFromEnv) != "" {
		t.Fatal("consumeRestartEnv must unset both variables so children never inherit them")
	}
}

// TestRestartHandoff_AppliedAfterFirstLoad pins that the new process puts
// the cursor back on the handed-over session and shows the version notice
// once.
func TestRestartHandoff_AppliedAfterFirstLoad(t *testing.T) {
	home, inst := buildFocusHome(t)
	home.groupTree.CollapseGroup("beta")
	home.rebuildFlatItems()
	home.restartHandoff = RestartHandoff{SelectedID: inst[3].ID, OldVersion: "1.15.0"}
	home.applyRestartHandoff()
	if idx := home.flatItemIndexByID(inst[3].ID); idx < 0 || home.cursor != idx {
		t.Fatalf("cursor = %d, want the handed-over session revealed at %d", home.cursor, idx)
	}
	if home.err == nil || !strings.Contains(home.err.Error(), "restarted into v") || !strings.Contains(home.err.Error(), "(was v1.15.0)") {
		t.Fatalf("notice = %v", home.err)
	}
	if home.restartHandoff != (RestartHandoff{}) {
		t.Fatal("hand-off must be cleared after it was applied")
	}
	// RestartHandoff() on the old side names the selected session.
	if got := home.RestartHandoff(); got.SelectedID != inst[3].ID || got.OldVersion != Version {
		t.Fatalf("RestartHandoff() = %+v", got)
	}
}
