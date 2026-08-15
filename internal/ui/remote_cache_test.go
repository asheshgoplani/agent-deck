package ui

import (
	"encoding/json"
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

func TestRemoteSessionsCache_ExpiredRemoteIgnored(t *testing.T) {
	h := &Home{}
	snap := remoteSessionsCache{
		SavedAt: time.Now(), // snapshot stamp is fresh — must NOT rescue stale remotes
		Sessions: map[string][]session.RemoteSessionInfo{
			"stale": {{ID: "x"}},
			"fresh": {{ID: "y"}},
		},
		FetchedAt: map[string]time.Time{
			"stale": time.Now().Add(-25 * time.Hour),
			"fresh": time.Now().Add(-1 * time.Hour),
		},
	}
	h.applyRemoteSessionsSnapshot(snap)
	if _, ok := h.remoteSessions["stale"]; ok {
		t.Fatalf("remote with a 25h-old live fetch must not be applied")
	}
	if _, ok := h.remoteSessions["fresh"]; !ok {
		t.Fatalf("fresh remote should have been applied")
	}
}

func TestRemoteSessionsCache_LegacySnapshotUsesSavedAt(t *testing.T) {
	h := &Home{}
	snap := remoteSessionsCache{
		SavedAt:  time.Now().Add(-25 * time.Hour),
		Sessions: map[string][]session.RemoteSessionInfo{"g14": {{ID: "x"}}},
	}
	h.applyRemoteSessionsSnapshot(snap)
	if _, ok := h.remoteSessions["g14"]; ok {
		t.Fatalf("expired legacy snapshot must not be applied")
	}
}

// The tests above exercise applyRemoteSessionsSnapshot in isolation. The
// on-disk half — save on fetch completion, load on the next startup — is
// gated off for the rest of the package by TestMain (see the comment there),
// so this test owns its only coverage. It re-enables the gate for its own
// scope and clears the key on the way out, so no later NewHome() inherits
// these remotes and miscounts them.
func TestRemoteSessionsCache_SaveLoadRoundTrip(t *testing.T) {
	remoteSessionsCacheEnabled = true
	t.Cleanup(func() { remoteSessionsCacheEnabled = false })

	writer := NewHome()
	if writer.storage == nil || writer.storage.GetDB() == nil {
		t.Skip("no storage backend in this environment")
	}
	db := writer.storage.GetDB()
	t.Cleanup(func() { _ = db.SetMeta(remoteSessionsCacheKey, "") })

	live := map[string][]session.RemoteSessionInfo{
		"g14": {{ID: "r1", Title: "build", Tool: "claude", Status: "running", RemoteName: "g14"}},
	}
	writer.remoteSessionsMu.Lock()
	writer.remoteSessions = live
	writer.remoteSessionsMu.Unlock()
	writer.saveRemoteSessionsCache(live)

	// Read it back through the real startup constructor, not a hand-built
	// Home: this has to fail if NewHome ever stops loading the cache.
	reader := NewHome()

	got := reader.remoteSessions["g14"]
	if len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("save/load round trip lost the snapshot: %+v", reader.remoteSessions)
	}
	if !reader.remoteFromCache["g14"] {
		t.Errorf("a remote seeded from disk must be flagged as cached")
	}
	// RemoteName is `json:"-"`, so it does not survive the snapshot on its
	// own and has to be restored from the map key. An empty one here means
	// cached sessions render and route nameless until the first live fetch.
	if got[0].RemoteName != "g14" {
		t.Errorf("RemoteName = %q, want \"g14\" — it is json:\"-\" and must be restored on load", got[0].RemoteName)
	}
}

// The expiry tests above call applyRemoteSessionsSnapshot directly, so they
// would still pass if loadRemoteSessionsCache dropped its half of the
// contract. This one drives expiry through the production startup path.
func TestRemoteSessionsCache_ExpiredSnapshotIgnoredOnStartup(t *testing.T) {
	remoteSessionsCacheEnabled = true
	t.Cleanup(func() { remoteSessionsCacheEnabled = false })

	seed := NewHome()
	if seed.storage == nil || seed.storage.GetDB() == nil {
		t.Skip("no storage backend in this environment")
	}
	db := seed.storage.GetDB()
	t.Cleanup(func() { _ = db.SetMeta(remoteSessionsCacheKey, "") })

	stale := remoteSessionsCache{
		SavedAt:   time.Now(),
		Sessions:  map[string][]session.RemoteSessionInfo{"g14": {{ID: "old"}}},
		FetchedAt: map[string]time.Time{"g14": time.Now().Add(-25 * time.Hour)},
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal seed snapshot: %v", err)
	}
	if err := db.SetMeta(remoteSessionsCacheKey, string(data)); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if got := NewHome().remoteSessions["g14"]; len(got) != 0 {
		t.Errorf("startup applied a remote whose last live fetch was 25h ago: %+v", got)
	}
}
