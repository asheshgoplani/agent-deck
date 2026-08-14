package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1846: the session-row badge and the preview's "⏱" line computed
// "last activity" with two different formulas (pickBadgeTime vs
// DisplayLastActivityTime), so the same session showed two different — and
// both stale — ages. sessionActivityTime is the single composition both
// surfaces now render from: pickBadgeTime's evidence layers, falling back
// to LastAccessedAt only when no activity evidence beyond creation exists
// (a TUI attach is a better floor than CreatedAt, but a peek must never
// override real recorded activity).

func TestSessionActivityTime_FallsBackToLastAccessed(t *testing.T) {
	created := time.Now().Add(-3 * 24 * time.Hour)
	accessed := time.Now().Add(-7 * time.Hour)

	got := sessionActivityTime(created, time.Time{}, time.Time{}, accessed, time.Time{}, false, nil)
	if !got.Equal(accessed) {
		t.Errorf("with no activity evidence, LastAccessedAt should beat CreatedAt, expected %v, got %v", accessed, got)
	}
}

func TestSessionActivityTime_EvidenceBeatsNewerAccess(t *testing.T) {
	// Attaching to look at a quiet session is a peek, not activity: real
	// recorded evidence wins even when the attach is newer.
	created := time.Now().Add(-3 * 24 * time.Hour)
	persisted := time.Now().Add(-2 * time.Hour)
	accessed := time.Now().Add(-5 * time.Minute)

	got := sessionActivityTime(created, time.Time{}, persisted, accessed, time.Time{}, false, nil)
	if !got.Equal(persisted) {
		t.Errorf("recorded activity must beat a newer attach, expected %v, got %v", persisted, got)
	}
}

func TestSessionActivityTime_NoAccessNoEvidenceIsCreatedAt(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	got := sessionActivityTime(created, time.Time{}, time.Time{}, time.Time{}, time.Time{}, false, nil)
	if !got.Equal(created) {
		t.Errorf("with nothing but CreatedAt, expected %v, got %v", created, got)
	}
}

func TestSessionActivityTime_LifecycleEvidenceSuppressesAccessFallback(t *testing.T) {
	// A restart IS activity evidence: once present, LastAccessedAt no
	// longer participates, even when the attach is newer than the restart.
	created := time.Now().Add(-3 * 24 * time.Hour)
	started := time.Now().Add(-2 * time.Hour)
	accessed := time.Now().Add(-5 * time.Minute)

	got := sessionActivityTime(created, started, time.Time{}, accessed, time.Time{}, false, nil)
	if !got.Equal(started) {
		t.Errorf("lifecycle evidence must suppress the access fallback, expected %v, got %v", started, got)
	}
}

// TestPreviewActivityLine_UsesUnifiedActivitySource pins the preview's "⏱"
// line to the shared composition. Pre-#1846 the preview ignored
// LastStartedAt entirely (DisplayLastActivityTime fell straight to
// LastAccessedAt/CreatedAt), so a session restarted 30 minutes ago showed a
// days-old age while the row badge showed "30m ago".
func TestPreviewActivityLine_UsesUnifiedActivitySource(t *testing.T) {
	forceTrueColorProfile()

	h := NewHome()
	h.width = 120
	h.height = 40
	h.initialLoading = false

	inst := session.NewInstance("preview-activity-unified", t.TempDir())
	inst.Tool = "bash"
	inst.Status = session.StatusIdle
	inst.CreatedAt = time.Now().Add(-3 * 24 * time.Hour)
	inst.LastAccessedAt = time.Now().Add(-8 * time.Hour)
	inst.LastStartedAt = time.Now().Add(-30 * time.Minute)

	h.instancesMu.Lock()
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.instancesMu.Unlock()

	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	h.cursor = 0
	h.setHotkeys(resolveHotkeys(nil))

	out := h.renderPreviewPane(60, 30)
	if !strings.Contains(out, "30m ago") {
		t.Fatalf("preview ⏱ line should show the 30m-old restart via the unified activity source, got:\n%s", out)
	}
}

// TestSessionTimestamp_BadgeFallsBackToLastAccessed is the row-badge half
// of the same agreement: with no activity evidence at all, the badge must
// show the last attach instead of collapsing to CreatedAt ("2d 9h ago" in
// the #1846 report).
func TestSessionTimestamp_BadgeFallsBackToLastAccessed(t *testing.T) {
	inst := &session.Instance{
		ID:             "sess-ts-accessed-fallback",
		Title:          "accessed-fallback",
		CreatedAt:      time.Now().Add(-3 * 24 * time.Hour),
		LastAccessedAt: time.Now().Add(-30 * time.Minute),
	}

	row := renderRowWithTimestamps(t, inst, true)

	if !strings.Contains(row, "30m ago") {
		t.Fatalf("badge should fall back to LastAccessedAt when no activity evidence exists, got: %q", row)
	}
}
