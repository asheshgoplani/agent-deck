package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Session row timestamp badge. The "5m ago" / "1h ago" suffix appears on
// every row when [display] show_session_timestamps = true, and is absent
// otherwise. The flag is cached on Home at startup and refreshed when the
// settings panel saves.

func renderRowWithTimestamps(t *testing.T, inst *session.Instance, enabled bool) string {
	t.Helper()
	forceTrueColorProfile()

	h := &Home{width: 140, showSessionTimestamps: enabled}
	item := session.Item{
		Type:          session.ItemTypeSession,
		Session:       inst,
		Level:         1,
		Path:          "test",
		IsLastInGroup: true,
	}
	snapshot := map[string]sessionRenderState{
		inst.ID: {
			status:    session.StatusRunning,
			tool:      "claude",
			paneTitle: "",
		},
	}

	var b strings.Builder
	h.renderSessionItem(&b, item, false, snapshot, h.width)
	return b.String()
}

func TestSessionTimestamp_EnabledShowsAgoBadge(t *testing.T) {
	inst := &session.Instance{
		ID:        "sess-ts-on",
		Title:     "with-timestamp",
		CreatedAt: time.Now().Add(-90 * time.Minute), // 1h ago
	}

	row := renderRowWithTimestamps(t, inst, true)

	if !strings.Contains(row, "ago") {
		t.Fatalf("show_session_timestamps=true should append a relative-time badge to the row, "+
			"but no 'ago' suffix was found. Got: %q", row)
	}
}

func TestSessionTimestamp_DisabledOmitsBadge(t *testing.T) {
	inst := &session.Instance{
		ID:        "sess-ts-off",
		Title:     "without-timestamp",
		CreatedAt: time.Now().Add(-90 * time.Minute),
	}

	row := renderRowWithTimestamps(t, inst, false)

	// The relative-time output is "1h ago" / "5m ago" / "just now". None
	// of those substrings should appear when the toggle is off — they're
	// the load-bearing signal that the badge leaked into the default path.
	for _, sig := range []string{"ago", "just now"} {
		if strings.Contains(row, sig) {
			t.Fatalf("show_session_timestamps=false must not emit a timestamp badge, "+
				"but row contains %q. Got: %q", sig, row)
		}
	}
}

func TestSessionTimestamp_JustNowForFreshSession(t *testing.T) {
	inst := &session.Instance{
		ID:        "sess-ts-fresh",
		Title:     "fresh-session",
		CreatedAt: time.Now(), // < 1 minute → "just now"
	}

	row := renderRowWithTimestamps(t, inst, true)

	if !strings.Contains(row, "just now") {
		t.Fatalf("freshly-created session should render with 'just now' badge. Got: %q", row)
	}
}

// Old data source (Instance.GetLastActivityTime) returned tmuxSession.lastChangeTime
// which is initialized to time.Now() the first time the StateTracker is built
// for that session inside the running process. That made every session report
// "just now" right after agent-deck started — the exact bug surfaced in
// manual testing. This test pins the new contract: the badge reads from the
// SQLite-persisted timestamps so an idle session with an old CreatedAt and no
// LastAccessedAt/LastStartedAt updates does NOT collapse to "just now".
func TestSessionTimestamp_OldSessionDoesNotRenderJustNow(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	inst := &session.Instance{
		ID:             "sess-ts-stale",
		Title:          "stale-session",
		CreatedAt:      old,
		LastAccessedAt: old,
		LastStartedAt:  old,
	}

	row := renderRowWithTimestamps(t, inst, true)

	if strings.Contains(row, "just now") {
		t.Fatalf("session with all timestamps ~3h old must not render as 'just now'. Got: %q", row)
	}
	if !strings.Contains(row, "h ago") {
		t.Fatalf("expected '%dh ago' style badge for 3-hour-old session. Got: %q", 3, row)
	}
}

// Pins the max() across CreatedAt / LastStartedAt — the newest persisted
// lifecycle touchpoint should win. LastAccessedAt is intentionally NOT in
// the formula: attaching to a quiet session isn't an "update".
func TestSessionTimestamp_PicksMostRecentLifecycleTimestamp(t *testing.T) {
	now := time.Now()
	inst := &session.Instance{
		ID:            "sess-ts-recent",
		Title:         "recently-started",
		CreatedAt:     now.Add(-5 * 24 * time.Hour), // 5d ago
		LastStartedAt: now.Add(-90 * time.Second),   // 1m ago — newest
	}

	row := renderRowWithTimestamps(t, inst, true)

	if !strings.Contains(row, "1m ago") {
		t.Fatalf("badge should pick the newest of CreatedAt/LastStartedAt = 1m ago. Got: %q", row)
	}
}

// Regression pin: an Instance with nil tmuxSession (e.g. just loaded
// from disk, never started in this process) must not crash the badge
// path. Calling LastObservedActivity returns observed=false, so the
// badge falls back to the lifecycle floor — the original "all sessions
// show just now" bug surfaced exactly here, because the old code consumed
// the time value without checking observed.
func TestSessionTimestamp_HandlesNilTmuxSessionWithoutCrash(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	inst := &session.Instance{
		ID:        "sess-no-tmux",
		Title:     "loaded-from-disk",
		CreatedAt: old,
		// tmuxSession is unexported and stays nil — this is the realistic
		// state for any Instance that has just been loaded but not started.
	}

	row := renderRowWithTimestamps(t, inst, true)

	if strings.Contains(row, "just now") {
		t.Fatalf("an instance with no live tmux session must not be rendered as 'just now' "+
			"just because the tracker default leaked into the badge. Got: %q", row)
	}
	if !strings.Contains(row, "h ago") {
		t.Fatalf("expected '2h ago' fallback from CreatedAt. Got: %q", row)
	}
}

// Regression pin: LastAccessedAt must NOT influence the badge. A session
// created 5 days ago that the user just attached to is still "5d ago" by
// the badge's definition of update — opening the session isn't itself
// an update event.
func TestSessionTimestamp_LastAccessedAtIgnored(t *testing.T) {
	now := time.Now()
	inst := &session.Instance{
		ID:             "sess-ts-attached",
		Title:          "just-peeked",
		CreatedAt:      now.Add(-5 * 24 * time.Hour),
		LastAccessedAt: now.Add(-30 * time.Second), // would say "just now" if included
	}

	row := renderRowWithTimestamps(t, inst, true)

	if strings.Contains(row, "just now") {
		t.Fatalf("attaching to a stale session must not reset the badge to 'just now'. "+
			"LastAccessedAt was deliberately removed from the formula. Got: %q", row)
	}
	if !strings.Contains(row, "5d ago") {
		t.Fatalf("expected 5d ago (CreatedAt floor). Got: %q", row)
	}
}
