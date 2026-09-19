package ui

// Finding 1, 2026-09-18 live UI audit: the TUI header pill and the group
// preview panel both counted archived cross-harness sources still
// superseded by a "Restart with new session ID" — the old session record
// stays in storage with its old tmux process often still alive, but
// `list --json` correctly hides it. Both aggregates must derive from the
// same tracked set list --json enumerates (session.VisibleInstances).
//
// This uses REAL *session.Instance values (unlike the fabricated-snapshot
// issue953 tests) so refreshSessionRenderSnapshot's archivedSuperseded flag
// and renderGroupPreview's session.VisibleInstances filter are both
// exercised end-to-end.

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func supersededSourceAndTarget() (source, target *session.Instance) {
	source = &session.Instance{
		ID: "source", Title: "old-pi-session", Tool: "pi",
		Status: session.StatusWaiting, ArchivedAt: time.Now(), SupersededBy: "target",
	}
	target = &session.Instance{
		ID: "target", Title: "old-pi-session", Tool: "claude",
		Status: session.StatusRunning, Supersedes: "source",
	}
	return
}

func TestFinding1_HeaderCounter_ExcludesSupersededSource(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30

	source, target := supersededSourceAndTarget()
	other := &session.Instance{ID: "plain", Title: "plain-session", Tool: "claude", Status: session.StatusIdle}
	home.instances = []*session.Instance{source, target, other}
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	if running != 1 || waiting != 0 || idle != 1 || stopped != 0 || errored != 0 {
		t.Fatalf("counts = running=%d waiting=%d idle=%d stopped=%d errored=%d, want running=1 (target) waiting=0 (source excluded) idle=1 (plain) stopped=0 errored=0",
			running, waiting, idle, stopped, errored)
	}
}

func TestFinding1_GroupPreview_ExcludesSupersededSource(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30

	source, target := supersededSourceAndTarget()
	group := &session.Group{Name: "My Sessions", Path: session.DefaultGroupPath, Sessions: []*session.Instance{source, target}}

	preview := home.renderGroupPreview(group, 60, 30)
	if !strings.Contains(preview, "1 sessions") {
		t.Fatalf("preview session count did not exclude the superseded source:\n%s", preview)
	}
	if !strings.Contains(preview, "old-pi-session") {
		t.Fatalf("preview must still list the surviving target session:\n%s", preview)
	}
	// Both source and target happen to share a title, so distinguishing them
	// by title alone isn't reliable; instead assert only one status badge
	// shows up (the target's "running"), never the source's "waiting" too.
	if !strings.Contains(preview, "1 running") || strings.Contains(preview, "waiting") {
		t.Fatalf("preview status badges must reflect only the target, not the superseded source:\n%s", preview)
	}
}
