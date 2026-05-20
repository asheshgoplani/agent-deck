// Issue #953 (re-opened) — Status-line aggregate counter buckets manually
// stopped sessions under "✕ errors". PR #1072 fixed StatusStopped
// persistence for the per-session display, but countSessionStatuses still
// folded StatusStopped into the errored count, so the TUI header read
// "3 running, 1 waiting, 2 errors" when one of those "errors" was in fact a
// user-initiated stop.
//
// Reporter: @halfmu. The fix gives StatusStopped its own bucket so the
// header now reads e.g. "3 running, 1 waiting, 1 stopped, 1 error". This
// applies to both local instances and remote sessions discovered via SSH.
//
// The same bucketing must hold whether the status arrives as a
// session.Status (local snapshot) or a lowercase string (remote payload),
// because both paths feed the same aggregate counter.

package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIssue953_StoppedSessions_NotCountedAsErrors_Local(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// Build a render snapshot directly. We don't need real Instances — the
	// counter only consults sessionRenderState.status. 3 running + 1 waiting
	// + 1 idle + 1 stopped + 2 errors covers every bucket the counter must
	// distinguish.
	snap := map[string]sessionRenderState{
		"r1": {status: session.StatusRunning},
		"r2": {status: session.StatusRunning},
		"r3": {status: session.StatusRunning},
		"w1": {status: session.StatusWaiting},
		"i1": {status: session.StatusIdle},
		"s1": {status: session.StatusStopped},
		"e1": {status: session.StatusError},
		"e2": {status: session.StatusError},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()

	if running != 3 {
		t.Errorf("running = %d, want 3", running)
	}
	if waiting != 1 {
		t.Errorf("waiting = %d, want 1", waiting)
	}
	if idle != 1 {
		t.Errorf("idle = %d, want 1", idle)
	}
	if stopped != 1 {
		t.Errorf("stopped = %d, want 1 — manual-stop sessions need their own bucket (issue #953)", stopped)
	}
	if errored != 2 {
		t.Errorf("errored = %d, want 2 — stopped sessions must NOT inflate the error count (issue #953)", errored)
	}
}

func TestIssue953_StoppedSessions_NotCountedAsErrors_Remote(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30

	// No local instances — same bug class must hold for remote sessions
	// whose status arrives as a lowercase string from `agent-deck list
	// --json` on the remote host (see internal/session/discovery.go).
	home.refreshSessionRenderSnapshot(nil)

	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "r1", Title: "run-a", Tool: "claude", Status: "running", RemoteName: "dev"},
			{ID: "r2", Title: "wait-a", Tool: "claude", Status: "waiting", RemoteName: "dev"},
			{ID: "r3", Title: "stop-a", Tool: "claude", Status: "stopped", RemoteName: "dev"},
			{ID: "r4", Title: "err-a", Tool: "claude", Status: "error", RemoteName: "dev"},
		},
	}
	home.remoteSessionsMu.Unlock()
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()

	if running != 1 {
		t.Errorf("running = %d, want 1", running)
	}
	if waiting != 1 {
		t.Errorf("waiting = %d, want 1", waiting)
	}
	if idle != 0 {
		t.Errorf("idle = %d, want 0", idle)
	}
	if stopped != 1 {
		t.Errorf("stopped = %d, want 1 — remote stopped session must land in its own bucket", stopped)
	}
	if errored != 1 {
		t.Errorf("errored = %d, want 1 — remote stopped session must NOT inflate the error count", errored)
	}
}

// TestIssue953_StatusLineRender_ShowsStoppedBucket asserts the header
// stats string the user actually reads contains a "stopped" segment when
// stopped sessions are present and does NOT silently fold them into the
// "error" segment.
func TestIssue953_StatusLineRender_ShowsStoppedBucket(t *testing.T) {
	home := NewHome()
	home.width = 200
	home.height = 30

	snap := map[string]sessionRenderState{
		"r1": {status: session.StatusRunning},
		"r2": {status: session.StatusRunning},
		"r3": {status: session.StatusRunning},
		"w1": {status: session.StatusWaiting},
		"s1": {status: session.StatusStopped},
		"e1": {status: session.StatusError},
		"e2": {status: session.StatusError},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)
	home.initialLoading = false // Skip splash so View() renders the real header

	out := home.View()

	// Stats segment uses the words "running", "waiting", "stopped",
	// "error". Stopped must appear with count 1, error with count 2.
	mustContain := []string{
		"3 running",
		"1 waiting",
		"1 stopped",
		"2 error",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("header stats missing %q (full output below):\n%s", want, out)
		}
	}

	// Negative: the wrong count "3 error" (2 real errors + 1 stopped
	// folded in) must NOT appear. This is the regression to lock down.
	if strings.Contains(out, "3 error") {
		t.Errorf("header stats contains %q — stopped session was wrongly folded into the error bucket (issue #953)", "3 error")
	}
}

// TestIssue953_FilterBar_HasStoppedPill verifies the filter pill bar gains
// a stopped pill when stopped sessions are present. The pill row sits
// directly under the header and is the user's primary filter affordance —
// if there's no pill, there's no way to filter to "stopped".
func TestIssue953_FilterBar_HasStoppedPill(t *testing.T) {
	home := NewHome()
	home.width = 200
	home.height = 30

	snap := map[string]sessionRenderState{
		"s1": {status: session.StatusStopped},
		"r1": {status: session.StatusRunning},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	bar := home.renderFilterBar()

	// Pills are "● N", "◐ N", "○ N", "■ N", "✕ N". The stopped pill must
	// show its count next to the stopped icon (■ — matching the canonical
	// symbol mapping in internal/session/notifications.go). Error count must
	// remain at 0 (no pill, since the bar hides empty error pills).
	if !strings.Contains(bar, "● 1") {
		t.Errorf("filter bar missing running pill: %s", bar)
	}
	if !strings.Contains(bar, "■ 1") {
		t.Errorf("filter bar missing stopped pill (■ 1): %s", bar)
	}
	// No error pill should appear when errored == 0.
	if strings.Contains(bar, "✕ ") {
		t.Errorf("filter bar shows error pill even though errored == 0: %s", bar)
	}
}

