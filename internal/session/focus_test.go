package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestDecodeFocusRequest(t *testing.T) {
	now := int64(1_000_000_000_000) // arbitrary fixed "now" in unix nanos
	ttl := 10 * time.Second

	fresh, err := EncodeFocusRequest("abc123", now)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	tests := []struct {
		name     string
		val      string
		now      int64
		wantID   string
		wantFresh bool
	}{
		{"fresh", fresh, now, "abc123", true},
		{"within ttl", fresh, now + int64(9*time.Second), "abc123", true},
		{"stale beyond ttl", fresh, now + int64(11*time.Second), "abc123", false},
		{"empty", "", now, "", false},
		{"malformed json", "{not json", now, "", false},
		{"empty id", `{"id":"","ts":1}`, now, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := DecodeFocusRequest(tc.val, tc.now, ttl)
			if id != tc.wantID || ok != tc.wantFresh {
				t.Fatalf("DecodeFocusRequest(%q) = (%q, %v), want (%q, %v)",
					tc.val, id, ok, tc.wantID, tc.wantFresh)
			}
		})
	}
}

func TestFocusRequestDBRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// No request yet.
	if got, err := ReadFocusRequest(db); err != nil || got != "" {
		t.Fatalf("ReadFocusRequest empty = (%q, %v), want (\"\", nil)", got, err)
	}

	// Write, then read back and decode.
	now := time.Now().UnixNano()
	if err := WriteFocusRequest(db, "sess-1", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := ReadFocusRequest(db)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	id, fresh := DecodeFocusRequest(raw, now, FocusRequestTTL)
	if id != "sess-1" || !fresh {
		t.Fatalf("decode after write = (%q, %v), want (sess-1, true)", id, fresh)
	}

	// Clear (consume-once).
	if err := ClearFocusRequest(db); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, err := ReadFocusRequest(db); err != nil || got != "" {
		t.Fatalf("ReadFocusRequest after clear = (%q, %v), want (\"\", nil)", got, err)
	}
}
