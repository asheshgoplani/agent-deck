// Issue #1704 dual-review blocker: Instance.LastStartedAt claimed to be a
// "persisted stamp" but was never wired into InstanceData/SQLite, so every
// out-of-process reader (status --stale) saw it as always-zero. These tests
// guard the fix (last_started_persist.go) at the layer the original PR's
// tests never touched: round-tripping through the actual save/load path,
// not constructing Instance literals by hand.
package session

import (
	"strings"
	"testing"
	"time"
)

// TestLastStartedAt_ToolDataPersistenceRoundTrip mirrors
// TestIssue1143_IdleTimeout_PersistenceRoundTrip: exercises the extras-zone
// helpers directly, including the "unrelated fields survive" and "zero
// clears the key" cases.
func TestLastStartedAt_ToolDataPersistenceRoundTrip(t *testing.T) {
	started := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)

	td := WriteLastStartedAtToToolData(nil, started)
	got := ReadLastStartedAtFromToolData(td)
	if !got.Equal(started) {
		t.Fatalf("ReadLastStartedAtFromToolData after Write = %v, want %v", got, started)
	}

	// Zero time removes the key (old-record/never-started stay indistinguishable).
	cleared := WriteLastStartedAtToToolData(td, time.Time{})
	if got := ReadLastStartedAtFromToolData(cleared); !got.IsZero() {
		t.Fatalf("Write(td, zero) should clear, got %v", got)
	}

	// Round-trip preserves unrelated fields.
	mixed := []byte(`{"color":"#ff00aa","claude_session_id":"abc"}`)
	out := WriteLastStartedAtToToolData(mixed, started)
	if got := ReadLastStartedAtFromToolData(out); !got.Equal(started) {
		t.Fatalf("round-trip with extras lost last_started_at: got %v, want %v", got, started)
	}
	if !strings.Contains(string(out), `"color":"#ff00aa"`) {
		t.Fatalf("round-trip dropped color: %s", string(out))
	}
	if !strings.Contains(string(out), `"claude_session_id":"abc"`) {
		t.Fatalf("round-trip dropped claude_session_id: %s", string(out))
	}
}

// TestLastStartedAt_SQLiteRoundTrip is the boundary test finding #1/#2 of
// the #1704 review demanded: prove LastStartedAt survives an actual
// SaveWithGroups -> LoadWithGroups cycle through SQLite — the exact process
// boundary status_stale.go crosses — rather than being set directly on an
// in-memory Instance literal the way the original heuristic tests did.
func TestLastStartedAt_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("last-started-roundtrip", "/tmp")
	inst.Tool = "shell"
	started := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	inst.LastStartedAt = started

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(loaded))
	}
	if !loaded[0].LastStartedAt.Equal(started) {
		t.Fatalf("LastStartedAt not preserved across SQLite round-trip: got %v, want %v", loaded[0].LastStartedAt, started)
	}

	// A session that was never started must keep reading as zero — the
	// heuristic in status_stale.go depends on this staying a reliable
	// "unknown/never" signal, not silently defaulting to "just started".
	fresh := NewInstance("never-started-roundtrip", "/tmp")
	fresh.Tool = "shell"
	groupTree2 := NewGroupTreeWithGroups([]*Instance{fresh}, nil)
	if err := storage.SaveWithGroups([]*Instance{fresh}, groupTree2); err != nil {
		t.Fatalf("SaveWithGroups (never-started): %v", err)
	}
	loaded2, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups (never-started): %v", err)
	}
	var freshLoaded *Instance
	for _, i := range loaded2 {
		if i.ID == fresh.ID {
			freshLoaded = i
		}
	}
	if freshLoaded == nil {
		t.Fatalf("never-started instance missing after reload")
	}
	if !freshLoaded.LastStartedAt.IsZero() {
		t.Fatalf("never-started instance got a non-zero LastStartedAt after round-trip: %v", freshLoaded.LastStartedAt)
	}
}
