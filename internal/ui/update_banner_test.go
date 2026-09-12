package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// manualRestart pins auto_restart = false so the banner shows the manual
// wording; the default (auto_restart on) is covered by
// TestUpdateBanner_AutoRestartWording.
func manualRestart(t *testing.T) {
	t.Helper()
	off := false
	stubUpdateSettings(t, session.UpdateSettings{AutoRestart: &off})
}

// TestUpdateBanner_InstalledStateWinsOverNudge pins the banner wording once
// a newer build is on disk: it names the installed version and the restart
// key, and it takes precedence over the releases-behind nudge because the
// user can act on it immediately.
func TestUpdateBanner_InstalledStateWinsOverNudge(t *testing.T) {
	manualRestart(t)
	h := &Home{
		updateInfo: &update.UpdateInfo{
			Available:      true,
			CurrentVersion: "1.16.0",
			LatestVersion:  "1.16.1",
			ReleasesBehind: 9,
		},
	}
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	w.observe(fpAt(2, 2))
	w.recordProbe(fpAt(2, 2), "1.16.1", nil)
	h.binaryWatch = w

	if !h.shouldRenderUpdateBanner() {
		t.Fatal("banner should render when an update is installed")
	}
	got := h.renderUpdateBannerText()
	want := "v1.16.1 installed, press ctrl+t to restart agent-deck"
	if !strings.Contains(got, want) {
		t.Fatalf("banner = %q, want it to contain %q", got, want)
	}
	if strings.Contains(got, "releases behind") {
		t.Fatalf("installed banner should replace the nudge text, got %q", got)
	}
}

// TestUpdateBanner_InstalledIgnoresNudgeDismiss pins that Esc-dismissing the
// nudge earlier in the session does not hide the restart hint: the two are
// independent states.
func TestUpdateBanner_InstalledIgnoresNudgeDismiss(t *testing.T) {
	h := &Home{updateNudgeDismissed: true}
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	w.observe(fpAt(2, 2))
	w.recordProbe(fpAt(2, 2), "1.16.1", nil)
	h.binaryWatch = w
	if !h.shouldRenderUpdateBanner() {
		t.Fatal("installed banner must survive a dismissed nudge")
	}
}

// TestUpdateBanner_NothingInstalledKeepsNudgeBehavior pins backward
// compatibility: with no installed update the banner is exactly the old
// nudge, including its threshold and Esc dismissal.
func TestUpdateBanner_NothingInstalledKeepsNudgeBehavior(t *testing.T) {
	h := &Home{updateInfo: &update.UpdateInfo{Available: true, ReleasesBehind: 2}}
	if h.shouldRenderUpdateBanner() {
		t.Fatal("2 releases behind and nothing installed should not render a banner")
	}
	h.updateInfo.ReleasesBehind = 8
	if !h.shouldRenderUpdateBanner() {
		t.Fatal("8 releases behind should render the nudge")
	}
	if got := h.renderUpdateBannerText(); !strings.Contains(got, "releases behind") {
		t.Fatalf("expected nudge text, got %q", got)
	}
	h.updateNudgeDismissed = true
	if h.shouldRenderUpdateBanner() {
		t.Fatal("dismissed nudge with nothing installed should not render")
	}
}

// TestUpdateBanner_UsesConfiguredRestartKey pins that a rebound restart_deck
// hotkey shows up in the banner instead of the default.
func TestUpdateBanner_UsesConfiguredRestartKey(t *testing.T) {
	manualRestart(t)
	h := &Home{hotkeys: resolveHotkeys(map[string]string{"restart_deck": "ctrl+y"})}
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	w.observe(fpAt(2, 2))
	w.recordProbe(fpAt(2, 2), "1.16.1", nil)
	h.binaryWatch = w
	if got := h.renderUpdateBannerText(); !strings.Contains(got, "press ctrl+y to restart") {
		t.Fatalf("banner = %q, want the rebound key", got)
	}
}

// TestUpdateBanner_AutoRestartWording pins that with auto_restart on (the
// default) the banner says the restart happens on its own, and still names
// the key for an immediate restart.
func TestUpdateBanner_AutoRestartWording(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	h := &Home{hotkeys: resolveHotkeys(nil)}
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	w.observe(fpAt(2, 2))
	w.recordProbe(fpAt(2, 2), "1.16.1", nil)
	h.binaryWatch = w
	got := h.renderUpdateBannerText()
	if !strings.Contains(got, "v1.16.1 installed, restarting when idle (ctrl+t now)") {
		t.Fatalf("banner = %q, want the auto-restart wording", got)
	}
}
