package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// helper: build a minimal Home with a single session at cursor 0
func homeWithSession(inst *session.Instance) *Home {
	h := NewHome()
	h.width = 120
	h.height = 40
	h.initialLoading = false

	h.instancesMu.Lock()
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.instancesMu.Unlock()

	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.cursor = 0

	// Use default hotkeys so actionKey calls work
	h.setHotkeys(resolveHotkeys(nil))
	return h
}

// Test 1: Stopped session preview contains "Session Stopped" header
func TestPreviewPane_Stopped_HasSessionStoppedHeader(t *testing.T) {
	inst := session.NewInstance("stopped-session", t.TempDir())
	inst.Status = session.StatusStopped

	h := homeWithSession(inst)
	rendered := h.renderPreviewPane(80, 30)

	if !strings.Contains(rendered, "Session Stopped") {
		t.Fatalf("expected 'Session Stopped' in stopped-session preview\nrendered=%q", rendered)
	}
	if strings.Contains(rendered, "Session Inactive") {
		t.Fatalf("stopped-session preview should not contain 'Session Inactive'\nrendered=%q", rendered)
	}
	if strings.Contains(rendered, "Session Error") {
		t.Fatalf("stopped-session preview should not contain 'Session Error'\nrendered=%q", rendered)
	}
}

// Test 2: Error session preview contains "Session Error" header
func TestPreviewPane_Error_HasSessionErrorHeader(t *testing.T) {
	inst := session.NewInstance("error-session", t.TempDir())
	inst.Status = session.StatusError

	h := homeWithSession(inst)
	rendered := h.renderPreviewPane(80, 30)

	if !strings.Contains(rendered, "Session Error") {
		t.Fatalf("expected 'Session Error' in error-session preview\nrendered=%q", rendered)
	}
	if strings.Contains(rendered, "Session Inactive") {
		t.Fatalf("error-session preview should not contain 'Session Inactive'\nrendered=%q", rendered)
	}
	if strings.Contains(rendered, "Session Stopped") {
		t.Fatalf("error-session preview should not contain 'Session Stopped'\nrendered=%q", rendered)
	}
}

func TestPreviewPane_Error_PromotesRestartBesideStateLine(t *testing.T) {
	inst := session.NewInstance("error-recovery", t.TempDir())
	inst.Status = session.StatusError

	rendered := tmux.StripANSI(homeWithSession(inst).renderPreviewPane(80, 30))
	stateLine := "✕ No tmux session running   R Restart"
	if !strings.Contains(rendered, stateLine) {
		t.Fatalf("error recovery must sit beside the state line as %q\nrendered=%q", stateLine, rendered)
	}
	if strings.Contains(rendered, "R Start") {
		t.Fatalf("error recovery must use the canonical Restart name, got %q", rendered)
	}
}

// Test 3: Stopped session preview contains user-intentional language
func TestPreviewPane_Stopped_HasResumeOrientedText(t *testing.T) {
	inst := session.NewInstance("stopped-resume", t.TempDir())
	inst.Status = session.StatusStopped

	h := homeWithSession(inst)
	rendered := h.renderPreviewPane(80, 30)

	if !strings.Contains(rendered, "stopped") {
		t.Fatalf("stopped-session preview should contain 'stopped'\nrendered=%q", rendered)
	}

	// Must have intentional/user-oriented language
	hasIntentionalLanguage := strings.Contains(rendered, "intentionally") ||
		strings.Contains(rendered, "by user") ||
		strings.Contains(rendered, "preserved") ||
		strings.Contains(rendered, "resuming")

	if !hasIntentionalLanguage {
		t.Fatalf("stopped-session preview should contain user-intentional language (intentionally/by user/preserved/resuming)\nrendered=%q", rendered)
	}
}

// Test 4: Error session preview contains crash-diagnostic language
func TestPreviewPane_Error_HasCrashDiagnosticText(t *testing.T) {
	inst := session.NewInstance("error-crash", t.TempDir())
	inst.Status = session.StatusError

	h := homeWithSession(inst)
	rendered := h.renderPreviewPane(80, 30)

	// Must have crash/system-failure language
	hasCrashLanguage := strings.Contains(rendered, "tmux server") ||
		strings.Contains(rendered, "restarted") ||
		strings.Contains(rendered, "No tmux session")

	if !hasCrashLanguage {
		t.Fatalf("error-session preview should contain crash-diagnostic text (tmux server/restarted/No tmux session)\nrendered=%q", rendered)
	}
}

// Test 5: Both paths pad output to approximately the same height (no layout shifts).
// The function pads using the existing pattern: pad until lines >= height, then strip
// the trailing newline. This yields height-1 lines in strings.Split. The caller always
// calls ensureExactHeight afterwards for final correction. We verify both statuses
// produce the same number of lines (consistent height behaviour), not a specific count.
func TestPreviewPane_BothStatuses_PadToHeight(t *testing.T) {
	const width = 80
	const height = 30

	var lineCounts []int
	for _, status := range []session.Status{session.StatusStopped, session.StatusError} {
		inst := session.NewInstance("pad-test", t.TempDir())
		inst.Status = status

		h := homeWithSession(inst)
		rendered := h.renderPreviewPane(width, height)

		lines := strings.Split(rendered, "\n")
		lineCounts = append(lineCounts, len(lines))

		// Must produce at least height-1 lines (the pad-then-strip pattern yields height-1)
		if len(lines) < height-1 {
			t.Fatalf("status %q: expected at least %d lines but got %d\nrendered=%q",
				status, height-1, len(lines), rendered)
		}
	}

	// Both statuses must produce the same line count (consistent layout)
	if lineCounts[0] != lineCounts[1] {
		t.Fatalf("stopped and error preview produced different line counts: stopped=%d error=%d",
			lineCounts[0], lineCounts[1])
	}
}

// Test for VIS-01: stopped sessions appear in flat items list when no filter active
func TestFlatItems_IncludesStoppedSessions(t *testing.T) {
	h := NewHome()
	h.width = 120
	h.height = 40
	h.initialLoading = false

	instances := []*session.Instance{
		session.NewInstance("running-session", t.TempDir()),
		session.NewInstance("stopped-session", t.TempDir()),
		session.NewInstance("error-session", t.TempDir()),
	}
	instances[0].Status = session.StatusRunning
	instances[1].Status = session.StatusStopped
	instances[2].Status = session.StatusError

	h.instancesMu.Lock()
	h.instances = instances
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.instancesMu.Unlock()

	h.groupTree = session.NewGroupTree(instances)
	// Ensure no status filter is active
	h.statusFilter = ""
	h.rebuildFlatItems()

	var foundStopped, foundRunning, foundError bool
	for _, item := range h.flatItems {
		if item.Type != session.ItemTypeSession {
			continue
		}
		switch item.Session.Status {
		case session.StatusStopped:
			foundStopped = true
		case session.StatusRunning:
			foundRunning = true
		case session.StatusError:
			foundError = true
		}
	}

	if !foundStopped {
		t.Error("stopped session should appear in flatItems when no status filter is active (VIS-01)")
	}
	if !foundRunning {
		t.Error("running session should appear in flatItems")
	}
	if !foundError {
		t.Error("error session should appear in flatItems")
	}
}
