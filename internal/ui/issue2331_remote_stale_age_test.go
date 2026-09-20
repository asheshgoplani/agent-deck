// Issue #2331: a remote row that already answered (already replied, already
// idle) kept showing its stale status with no indication of how old it was
// — remotePollUnavailable only flags a poll that failed outright, not one
// that succeeded slowly. These tests pin the age-marker gap directly: how
// long a remote's rows have been live (remoteRowAge/remoteRowStale), how
// that renders (formatRemoteAge), and that a live fetch — poll or pushed
// talkback alike — resets it via saveRemoteSessionsCache, the same map
// write both delivery paths already went through for the on-disk cache.
package ui

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteRowStale_AgeThreshold(t *testing.T) {
	h := NewHome()
	h.remoteFetchedAt = map[string]time.Time{"agentbox": time.Now().Add(-47 * time.Second)}

	age, stale := h.remoteRowStale("agentbox")
	if !stale {
		t.Fatal("47s-old row must be reported stale (remoteRowStaleAge is 30s)")
	}
	if got := formatRemoteAge(age); got != "47s" {
		t.Fatalf("formatRemoteAge(47s) = %q, want %q", got, "47s")
	}
}

func TestRemoteRowStale_FreshNotStale(t *testing.T) {
	h := NewHome()
	h.remoteFetchedAt = map[string]time.Time{"agentbox": time.Now().Add(-2 * time.Second)}

	if _, stale := h.remoteRowStale("agentbox"); stale {
		t.Fatal("a row fetched 2s ago must not be reported stale")
	}
}

func TestRemoteRowStale_UnknownAgeNotStale(t *testing.T) {
	h := NewHome()
	// No entry at all: a remote never yet fetched (or one whose age this
	// process has no record of) falls through to the "unavailable"/"last
	// known" path in the caller, not this one — this must not claim staleness
	// it cannot back with a number.
	if _, stale := h.remoteRowStale("never-seen"); stale {
		t.Fatal("a remote with no recorded fetch time must not be reported stale by age")
	}
}

// TestSaveRemoteSessionsCache_ResetsAgeForLiveFetchedOnly pins that a live
// fetch — a poll or a pushed talkback event, both funnel through
// applyRemoteFetch -> saveRemoteSessionsCache with the remote's name in
// liveFetched — resets that remote's age to ~0, while a remote NOT in this
// round (failed/timed out, or simply not part of this fetch) keeps its old
// stamp, so it drifts into "stale" on its own instead of being silently
// refreshed by another remote's successful poll.
func TestSaveRemoteSessionsCache_ResetsAgeForLiveFetchedOnly(t *testing.T) {
	origHome := os.Getenv("HOME")
	tmp := t.TempDir()
	os.Setenv("HOME", tmp)
	os.Setenv("XDG_CONFIG_HOME", tmp)
	os.Setenv("XDG_DATA_HOME", tmp)
	os.Setenv("XDG_CACHE_HOME", tmp)
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	storage, err := session.NewStorageWithProfile("_i2331stale")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	h := NewHome()
	h.storage = storage
	old := time.Now().Add(-time.Hour)
	h.remoteFetchedAt = map[string]time.Time{
		"agentbox": old, // this round's live fetch: must reset
		"g14":      old, // not part of this round: must NOT reset
	}

	h.saveRemoteSessionsCache(map[string][]session.RemoteSessionInfo{
		"agentbox": {{ID: "s1", RemoteName: "agentbox", Status: "waiting"}},
	})

	if age, stale := h.remoteRowStale("agentbox"); stale {
		t.Fatalf("agentbox: just live-fetched but still reported stale, age=%v", age)
	}
	h.remoteSessionsMu.RLock()
	g14 := h.remoteFetchedAt["g14"]
	h.remoteSessionsMu.RUnlock()
	if !g14.Equal(old) {
		t.Fatalf("g14: stamp moved from %v to %v despite not being in this fetch round", old, g14)
	}
	if age, stale := h.remoteRowStale("g14"); !stale || age < 59*time.Minute {
		t.Fatalf("g14: expected to remain stale at ~1h old, got stale=%v age=%v", stale, age)
	}
}
