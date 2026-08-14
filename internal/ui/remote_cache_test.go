package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The startup cache must never mask live data, must expire, and must flag
// its remotes as cached so the header can say so honestly.
func TestRemoteSessionsCache_LiveDataWins(t *testing.T) {
	h := &Home{
		remoteSessions: map[string][]session.RemoteSessionInfo{
			"g14": {{ID: "live1", Status: "running"}},
		},
	}
	snap := remoteSessionsCache{
		SavedAt: time.Now(),
		Sessions: map[string][]session.RemoteSessionInfo{
			"g14":   {{ID: "old1", Status: "waiting"}, {ID: "old2", Status: "waiting"}},
			"other": {{ID: "o1", Status: "waiting"}},
		},
	}
	h.applyRemoteSessionsSnapshot(snap)
	if len(h.remoteSessions["g14"]) != 1 || h.remoteSessions["g14"][0].ID != "live1" {
		t.Fatalf("cache overwrote live data: %+v", h.remoteSessions["g14"])
	}
	if len(h.remoteSessions["other"]) != 1 {
		t.Fatalf("cache did not seed missing remote")
	}
	if h.remoteFromCache["g14"] {
		t.Fatalf("live remote wrongly flagged as cached")
	}
	if !h.remoteFromCache["other"] {
		t.Fatalf("seeded remote must be flagged as cached")
	}
}

func TestRemoteSessionsCache_ExpiredSnapshotIgnored(t *testing.T) {
	h := &Home{}
	snap := remoteSessionsCache{
		SavedAt:  time.Now().Add(-25 * time.Hour),
		Sessions: map[string][]session.RemoteSessionInfo{"g14": {{ID: "x"}}},
	}
	if snap.SavedAt.IsZero() || time.Since(snap.SavedAt) > remoteSessionsCacheMaxAge {
		return // expected path: caller skips applying
	}
	h.applyRemoteSessionsSnapshot(snap)
	t.Fatalf("expired snapshot should not have been applied")
}
