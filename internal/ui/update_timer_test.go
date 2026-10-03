package ui

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// #2472: the TUI's periodic check installs or heals this host's update
// timer once per process, never in a test-, CI- or script-driven TUI.
func TestUpdateCheck_EnsuresTheTimerOncePerProcess(t *testing.T) {
	stubUpdateSettings(t, session.UpdateSettings{})
	calls := 0
	prev := ensureUpdateTimer
	ensureUpdateTimer = func(*slog.Logger) (update.TimerEnsureResult, error) {
		calls++
		return update.TimerEnsureResult{Action: update.TimerActionMigrated, Migrated: update.LegacySystemdTimerTimer}, nil
	}
	prevPending := pendingLaunchAgents
	pendingLaunchAgents = func() bool { return false }
	t.Cleanup(func() { ensureUpdateTimer, pendingLaunchAgents = prev, prevPending })

	h := newRestartTestHome(t)
	h.autoUpdateSuppressedReason = "running under go test"
	if cmd := h.handleUpdateCheck(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}}); cmd != nil {
		t.Fatal("a suppressed TUI must not touch the timer")
	}

	h.autoUpdateSuppressedReason = ""
	cmd := h.handleUpdateCheck(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}})
	if cmd == nil {
		t.Fatal("the first check result must start the timer heal")
	}
	msg, ok := cmd().(updateTimerEnsuredMsg)
	if !ok || calls != 1 || msg.result.Action != update.TimerActionMigrated {
		t.Fatalf("heal msg = %+v ok=%v calls=%d", msg, ok, calls)
	}
	h.Update(msg)
	if cmd := h.handleUpdateCheck(updateCheckMsg{info: &update.UpdateInfo{CurrentVersion: "1.16.0", LatestVersion: "1.16.0"}}); cmd != nil {
		t.Fatal("the heal runs once per TUI process, not per check")
	}
}

func TestRemoteTimerPreviewLine(t *testing.T) {
	next := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		st   *update.TimerStatus
		want string
	}{
		{nil, ""},
		{&update.TimerStatus{Kind: update.TimerKindUnknown}, "update timer unknown (remote too old to report it)"},
		{&update.TimerStatus{Kind: update.TimerKindNone}, "update timer none · updates only when this controller nudges it"},
		{&update.TimerStatus{Kind: "systemd", Installed: true}, "update timer inactive (systemd)"},
		{&update.TimerStatus{Kind: "systemd", Installed: true, Active: true, NextRun: next.Format(time.RFC3339)}, "update timer active (systemd) · next " + next.Local().Format("Jan 2 15:04")},
		{&update.TimerStatus{Kind: update.TimerKindSystemdLegacy, Installed: true, Active: true}, "update timer active (systemd-legacy) · legacy unit, `remote update --install-timer` migrates it"},
	}
	for _, tc := range cases {
		if got := remoteTimerPreviewLine(tc.st); got != tc.want {
			t.Errorf("remoteTimerPreviewLine(%+v) = %q, want %q", tc.st, got, tc.want)
		}
	}

	// The remote preview shows the line under the version line.
	state := session.RemoteVersionState{Version: "1.16.25", Found: true, CheckedAt: time.Now(), Timer: &update.TimerStatus{Kind: update.TimerKindNone}}
	lines := remotePreviewFieldLines(state, "1.16.25", nil, remoteHostStatsResult{}, false, []string{session.PreviewFieldVersion}, time.Now(), previewLayout{})
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "update timer none") {
		t.Fatalf("preview lines = %q", lines)
	}
	// Without a timer reading the panel is unchanged.
	state.Timer = nil
	if lines := remotePreviewFieldLines(state, "1.16.25", nil, remoteHostStatsResult{}, false, []string{session.PreviewFieldVersion}, time.Now(), previewLayout{}); len(lines) != 1 {
		t.Fatalf("preview lines without a timer = %q", lines)
	}
}
