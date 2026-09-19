package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// A TUI that has had a newer build on disk for restartOverdueAfter without
// an idle window says so: tui_restart_overdue with the blocking reason (at
// most once an hour), and a banner that names the reason instead of the
// "restarting when idle" promise. 2026-09-19: two TUIs ran an old image
// for hours and nothing said why.
func TestAutoRestart_OverdueLogsReasonAndChangesBanner(t *testing.T) {
	logs := captureUILog(t)
	h := newAutoRestartTestHome(t)
	h.jumpMode = true // blocks the restart: "close the open dialog first"
	h.binaryWatch.installedSince = time.Now().Add(-restartOverdueAfter - time.Minute)

	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested {
		t.Fatal("still blocked")
	}
	if !strings.Contains(logs.String(), `"msg":"tui_restart_overdue"`) {
		t.Fatalf("expected tui_restart_overdue, got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "close the open dialog first") || !strings.Contains(logs.String(), `"installed":"1.16.1"`) {
		t.Fatalf("the overdue line carries the reason and the version:\n%s", logs.String())
	}
	banner := h.renderUpdateBannerText()
	if !strings.Contains(banner, "v1.16.1 installed") || !strings.Contains(banner, "restart overdue") || !strings.Contains(banner, "close the open dialog first") {
		t.Fatalf("banner = %q", banner)
	}
	// Repeats inside the hour are silent.
	for i := 0; i < 3; i++ {
		_ = h.maybeAutoRestart()
	}
	if n := strings.Count(logs.String(), `"msg":"tui_restart_overdue"`); n != 1 {
		t.Fatalf("overdue lines = %d, want 1", n)
	}
	h.restartOverdueLoggedAt = time.Now().Add(-2 * time.Hour)
	_ = h.maybeAutoRestart()
	if n := strings.Count(logs.String(), `"msg":"tui_restart_overdue"`); n != 2 {
		t.Fatalf("overdue lines = %d, want 2 after an hour", n)
	}

	// Unblocked: the restart arms and the overdue note clears.
	h.jumpMode = false
	if cmd := h.maybeAutoRestart(); cmd == nil || !h.restartRequested {
		t.Fatal("restart must arm once unblocked")
	}
	if h.restartOverdueReason != "" {
		t.Fatalf("overdue reason must clear on restart, got %q", h.restartOverdueReason)
	}
}

// The silent paths are overdue too: a restart that was requested but whose
// shutdown never finished, auto_restart off, or a held target.
func TestAutoRestart_OverdueCoversSilentPaths(t *testing.T) {
	logs := captureUILog(t)
	h := newAutoRestartTestHome(t)
	h.binaryWatch.installedSince = time.Now().Add(-3 * time.Hour)
	h.restartRequested = true
	_ = h.maybeAutoRestart()
	if !strings.Contains(logs.String(), "restart already requested") {
		t.Fatalf("a stuck restart request is reported:\n%s", logs.String())
	}

	logs.Reset()
	h = newAutoRestartTestHome(t)
	h.binaryWatch.installedSince = time.Now().Add(-3 * time.Hour)
	stubUpdateSettings(t, session.UpdateSettings{AutoRestart: boolPtr(false)})
	_ = h.maybeAutoRestart()
	if !strings.Contains(logs.String(), "auto_restart is off") {
		t.Fatalf("auto_restart off is reported:\n%s", logs.String())
	}
	if got := h.renderUpdateBannerText(); !strings.Contains(got, "press ctrl+t to restart") {
		t.Fatalf("with auto_restart off the banner keeps the manual wording, got %q", got)
	}

	// Not yet overdue: nothing logged, banner unchanged.
	logs.Reset()
	stubUpdateSettings(t, session.UpdateSettings{})
	h = newAutoRestartTestHome(t)
	h.jumpMode = true
	h.binaryWatch.installedSince = time.Now().Add(-time.Hour)
	_ = h.maybeAutoRestart()
	if strings.Contains(logs.String(), "tui_restart_overdue") {
		t.Fatal("an hour is not overdue")
	}
	if !strings.Contains(h.renderUpdateBannerText(), "restarting when idle") {
		t.Fatalf("banner = %q", h.renderUpdateBannerText())
	}
}

// installedSince is stamped when the watch first sees the newer build and
// cleared when the file goes back to the running one.
func TestBinaryWatch_InstalledSince(t *testing.T) {
	w := newBinaryWatch("/bin/agent-deck", "1.16.0", fpAt(1, 1))
	w.observe(fpAt(2, 2))
	w.recordProbe(fpAt(2, 2), "1.16.1", nil)
	if w.installedSince.IsZero() {
		t.Fatal("installedSince must be stamped")
	}
	first := w.installedSince
	w.observe(fpAt(3, 3))
	w.recordProbe(fpAt(3, 3), "1.16.2", nil)
	if !w.installedSince.Equal(first) {
		t.Fatal("a second newer build keeps the first stamp: the wait started then")
	}
	w.observe(fpAt(4, 4))
	w.recordProbe(fpAt(4, 4), "1.16.0", nil)
	if !w.installedSince.IsZero() || w.installedVersion != "" {
		t.Fatal("back to the running build: cleared")
	}
}

// Every tick refreshes the heartbeat file at most every TUIHeartbeatEvery;
// it carries the restart state the fleet watch reports.
func TestHeartbeat_WrittenFromTickAndRemovedOnExit(t *testing.T) {
	dir := t.TempDir()
	h := newAutoRestartTestHome(t)
	h.heartbeatDir = dir
	h.jumpMode = true
	h.binaryWatch.installedSince = time.Now().Add(-3 * time.Hour)
	_ = h.maybeAutoRestart()

	now := time.Now()
	h.maybeWriteHeartbeat(now)
	hbs, err := update.ListTUIHeartbeats(dir, func(int) bool { return true })
	if err != nil || len(hbs) != 1 {
		t.Fatalf("heartbeats = %v, %v", hbs, err)
	}
	hb := hbs[0]
	if hb.PID != os.Getpid() || hb.Version != Version || hb.InstalledVersion != "1.16.1" {
		t.Fatalf("heartbeat = %+v", hb)
	}
	if hb.RestartState != "overdue" || !strings.Contains(hb.BlockReason, "close the open dialog first") {
		t.Fatalf("restart state = %q / %q", hb.RestartState, hb.BlockReason)
	}
	if !hb.LastTickAt.Equal(now.Truncate(time.Second)) && hb.LastTickAt.Before(now.Add(-time.Second)) {
		t.Fatalf("last tick = %v, want about %v", hb.LastTickAt, now)
	}

	// Inside the interval the file is left alone; after it, it reflects
	// the new state (unblocked: the restart armed, "requested").
	h.jumpMode = false
	_ = h.maybeAutoRestart()
	h.maybeWriteHeartbeat(now.Add(time.Second))
	hbs, _ = update.ListTUIHeartbeats(dir, func(int) bool { return true })
	if hbs[0].RestartState != "overdue" {
		t.Fatal("no rewrite inside the interval")
	}
	h.maybeWriteHeartbeat(now.Add(update.TUIHeartbeatEvery + time.Second))
	hbs, _ = update.ListTUIHeartbeats(dir, func(int) bool { return true })
	if hbs[0].RestartState != "requested" {
		t.Fatalf("after the interval the file reflects the new state, got %+v", hbs[0])
	}

	h.removeHeartbeat()
	hbs, _ = update.ListTUIHeartbeats(dir, func(int) bool { return true })
	if len(hbs) != 0 {
		t.Fatal("removed on exit")
	}
}
