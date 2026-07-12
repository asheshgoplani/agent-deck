package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// alwaysDead / neverDead are injectable stand-ins for the tmux-liveness probe
// so the predicate can be tested without a real tmux server.
func alwaysDead(*session.Instance) bool { return true }
func neverDead(*session.Instance) bool  { return false }

func TestIsCleanupCandidate(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * 24 * time.Hour
	old := now.Add(-45 * 24 * time.Hour)   // 45 days ago (past cutoff)
	recent := now.Add(-2 * 24 * time.Hour) // 2 days ago (within cutoff)

	cases := []struct {
		name            string
		inst            *session.Instance
		includeArchived bool
		force           bool
		isDead          func(*session.Instance) bool
		want            bool
	}{
		{
			name:   "dead and old -> candidate",
			inst:   &session.Instance{ID: "a", Status: session.StatusError, CreatedAt: old},
			isDead: alwaysDead,
			want:   true,
		},
		{
			name:   "alive -> not a candidate",
			inst:   &session.Instance{ID: "b", Status: session.StatusError, CreatedAt: old},
			isDead: neverDead,
			want:   false,
		},
		{
			name:   "dead but recent -> not a candidate",
			inst:   &session.Instance{ID: "c", Status: session.StatusError, CreatedAt: recent},
			isDead: alwaysDead,
			want:   false,
		},
		{
			name:   "recent attach protects an old-created dead session",
			inst:   &session.Instance{ID: "d", Status: session.StatusError, CreatedAt: old, LastAccessedAt: recent},
			isDead: alwaysDead,
			want:   false,
		},
		{
			name:            "archived excluded by default",
			inst:            &session.Instance{ID: "e", Status: session.StatusStopped, CreatedAt: old, ArchivedAt: old},
			includeArchived: false,
			isDead:          alwaysDead,
			want:            false,
		},
		{
			name:            "archived included with flag",
			inst:            &session.Instance{ID: "f", Status: session.StatusStopped, CreatedAt: old, ArchivedAt: old},
			includeArchived: true,
			isDead:          alwaysDead,
			want:            true,
		},
		{
			name:   "starting session within startup grace is never a candidate",
			inst:   &session.Instance{ID: "g", Status: session.StatusStarting, CreatedAt: now.Add(-time.Minute)},
			isDead: alwaysDead,
			want:   false,
		},
		{
			name:   "queued session within startup grace is never a candidate",
			inst:   &session.Instance{ID: "h", Status: session.StatusQueued, CreatedAt: now.Add(-time.Minute)},
			isDead: alwaysDead,
			want:   false,
		},
		{
			// A crash mid-start leaves status='starting' in the DB forever.
			// Past the startup grace it must be judged on liveness like any
			// other session, or it becomes an unpurgeable ghost.
			name:   "stale starting ghost past grace is a candidate",
			inst:   &session.Instance{ID: "i", Status: session.StatusStarting, CreatedAt: old},
			isDead: alwaysDead,
			want:   true,
		},
		{
			name:   "stale queued ghost past grace is a candidate",
			inst:   &session.Instance{ID: "j", Status: session.StatusQueued, CreatedAt: old},
			isDead: alwaysDead,
			want:   true,
		},
		{
			// A still-booting session past the grace window answers the probe
			// as alive, so liveness (not status) keeps it safe.
			name:   "live starting session past grace is protected by liveness",
			inst:   &session.Instance{ID: "k", Status: session.StatusStarting, CreatedAt: old},
			isDead: neverDead,
			want:   false,
		},
		{
			// pin-protects-from-stop, matching `session remove --all-errored`.
			name:   "pinned session retained by default",
			inst:   &session.Instance{ID: "l", Status: session.StatusError, CreatedAt: old, Pin: session.PinTop},
			isDead: alwaysDead,
			want:   false,
		},
		{
			name:   "pinned session included with force",
			inst:   &session.Instance{ID: "m", Status: session.StatusError, CreatedAt: old, Pin: session.PinTop},
			force:  true,
			isDead: alwaysDead,
			want:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isCleanupCandidate(tc.inst, now, maxAge, tc.includeArchived, tc.force, tc.isDead)
			if got != tc.want {
				t.Fatalf("isCleanupCandidate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectCleanupCandidates_SubsetAndOrder(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * 24 * time.Hour
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)

	instances := []*session.Instance{
		{ID: "keep-recent", Status: session.StatusError, CreatedAt: recent},
		{ID: "purge-1", Status: session.StatusError, CreatedAt: old},
		{ID: "keep-starting", Status: session.StatusStarting, CreatedAt: now.Add(-time.Minute)},
		{ID: "purge-2", Status: session.StatusStopped, CreatedAt: old},
	}

	got, pinned := selectCleanupCandidates(instances, now, maxAge, false, false, alwaysDead)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if got[0].ID != "purge-1" || got[1].ID != "purge-2" {
		t.Fatalf("candidates in wrong order/subset: %s, %s", got[0].ID, got[1].ID)
	}
	if pinned != 0 {
		t.Fatalf("expected 0 pinned skips, got %d", pinned)
	}
}

// A pinned session that would otherwise be purged is retained and counted, so
// the CLI can tell the user something was deliberately kept. --force includes it.
func TestSelectCleanupCandidates_PinnedSkippedAndCounted(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	maxAge := 30 * 24 * time.Hour
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)

	instances := []*session.Instance{
		{ID: "purge-1", Status: session.StatusError, CreatedAt: old},
		{ID: "pinned-old", Status: session.StatusError, CreatedAt: old, Pin: session.PinTop},
		// Pinned but too recent to purge: must NOT inflate the skip count,
		// which means "retained because pinned", not "is pinned".
		{ID: "pinned-recent", Status: session.StatusError, CreatedAt: recent, Pin: session.PinTop},
	}

	got, pinned := selectCleanupCandidates(instances, now, maxAge, false, false, alwaysDead)
	if len(got) != 1 || got[0].ID != "purge-1" {
		t.Fatalf("pinned session must be skipped; got %v", ids(got))
	}
	if pinned != 1 {
		t.Fatalf("expected 1 pinned skip (only the otherwise-purgeable one), got %d", pinned)
	}

	forced, pinnedForced := selectCleanupCandidates(instances, now, maxAge, false, true, alwaysDead)
	if len(forced) != 2 {
		t.Fatalf("--force must include the pinned old session; got %v", ids(forced))
	}
	if pinnedForced != 0 {
		t.Fatalf("--force must report 0 pinned skips, got %d", pinnedForced)
	}
}

func ids(instances []*session.Instance) []string {
	out := make([]string, 0, len(instances))
	for _, i := range instances {
		out = append(out, i.ID)
	}
	return out
}

func TestHumanizeAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{47 * 24 * time.Hour, "47d"},
		{5 * time.Hour, "5h"},
		{12 * time.Minute, "12m"},
		{30 * time.Second, "<1m"},
		{-5 * time.Hour, "<1m"},
	}
	for _, tc := range cases {
		if got := humanizeAge(tc.d); got != tc.want {
			t.Fatalf("humanizeAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("e33b5e95-1783547273"); got != "e33b5e95" {
		t.Fatalf("shortID long = %q, want %q", got, "e33b5e95")
	}
	if got := shortID("abc"); got != "abc" {
		t.Fatalf("shortID short = %q, want %q", got, "abc")
	}
}
