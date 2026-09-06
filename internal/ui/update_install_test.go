package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

func newInstallTestHome(t *testing.T) *Home {
	t.Helper()
	h := newRestartTestHome(t)
	h.updateInfo = &update.UpdateInfo{
		Available:      true,
		CurrentVersion: "1.16.0",
		LatestVersion:  "1.16.1",
		ReleasesBehind: 7,
	}
	return h
}

// TestInstallUpdate_NudgeOffersKey pins the banner wording: the nudge now
// points at the in-app key while keeping the CLI command visible for people
// who prefer a terminal.
func TestInstallUpdate_NudgeOffersKey(t *testing.T) {
	h := &Home{updateInfo: &update.UpdateInfo{Available: true, CurrentVersion: "1.16.0", LatestVersion: "1.16.1", ReleasesBehind: 7}}
	text := h.renderUpdateNudgeText()
	for _, want := range []string{"press ctrl+y to install", "agent-deck update", "Esc to dismiss"} {
		if !strings.Contains(text, want) {
			t.Fatalf("nudge %q missing %q", text, want)
		}
	}
}

// TestInstallUpdate_RefusedWithoutUpdateOrWithDialog pins the guards.
func TestInstallUpdate_RefusedWithoutUpdateOrWithDialog(t *testing.T) {
	h := newRestartTestHome(t)
	_, cmd := h.tryInstallUpdate()
	if cmd != nil || h.err == nil || !strings.Contains(h.err.Error(), "no update available") {
		t.Fatalf("no update: cmd=%v err=%v", cmd, h.err)
	}

	h = newInstallTestHome(t)
	h.jumpMode = true
	_, cmd = h.tryInstallUpdate()
	if cmd != nil || h.err == nil || !strings.Contains(h.err.Error(), "close the open dialog") {
		t.Fatalf("dialog open: cmd=%v err=%v", cmd, h.err)
	}

	// Already installed on disk: point at the restart key instead of
	// downloading again.
	h = newInstallTestHome(t)
	h.binaryWatch.observe(fpAt(2, 2))
	h.binaryWatch.recordProbe(fpAt(2, 2), "1.16.1", nil)
	_, cmd = h.tryInstallUpdate()
	if cmd != nil || h.err == nil || !strings.Contains(h.err.Error(), "press ctrl+t to restart") {
		t.Fatalf("already installed: cmd=%v err=%v", cmd, h.err)
	}
}

// TestInstallUpdate_CleanHomeRunsUpdater pins the happy path: a clean home
// screen with an available update hands Bubble Tea an exec command and
// leaves the footer untouched.
func TestInstallUpdate_CleanHomeRunsUpdater(t *testing.T) {
	h := newInstallTestHome(t)
	_, cmd := h.tryInstallUpdate()
	if cmd == nil {
		t.Fatal("expected an exec command")
	}
	if h.err != nil {
		t.Fatalf("unexpected footer error: %v", h.err)
	}
}

// TestInstallUpdate_FinishedRechecksBinary pins the post-install step: the
// handler always schedules a recheck and only reports a footer error when
// the updater exited non-zero.
func TestInstallUpdate_FinishedRechecksBinary(t *testing.T) {
	h := newInstallTestHome(t)
	if cmd := h.handleUpdateInstallFinished(updateInstallFinishedMsg{}); cmd == nil {
		t.Fatal("expected a recheck command after a clean exit")
	}
	if h.err != nil {
		t.Fatalf("clean exit must not set a footer error, got %v", h.err)
	}
	if cmd := h.handleUpdateInstallFinished(updateInstallFinishedMsg{err: errors.New("exit status 1")}); cmd == nil {
		t.Fatal("expected a recheck command even after a failed exit")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "agent-deck update failed") {
		t.Fatalf("failed exit should set a footer error, got %v", h.err)
	}
}
