package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
)

// A queued send to a Pi session is confirmed from its session document
// once Pi reads rows directly. When the document cannot be resolved the
// worker settles at once instead of holding the queue for the land window.
func TestWatchLandedPi(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "30s")
	sentAt := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)

	t.Run("unresolvable document settles at once", func(t *testing.T) {
		rec := &sendqueue.Record{SessionID: "pi-not-in-this-profile", Tool: "pi", Message: "hello pi", SentAt: sentAt}
		set := func(f func(*sendqueue.Record)) error { f(rec); return nil }
		start := time.Now()
		watchLanded("", rec, set)
		if took := time.Since(start); took > 5*time.Second {
			t.Fatalf("watchLanded took %s, want an immediate settle", took)
		}
		if !rec.Settled || rec.State == sendqueue.StateLanded || rec.Reason != "no Pi session document to confirm landing" {
			t.Fatalf("record = %+v", rec)
		}
	})

	t.Run("known document confirms the landing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pi.jsonl")
		doc := `{"type":"session","version":3,"id":"pi-q","cwd":"/p"}` + "\n" +
			`{"type":"message","id":"m1","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"user","content":[{"type":"text","text":"hello pi"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &sendqueue.Record{SessionID: "pi-q", Tool: "pi", Message: "hello pi", SentAt: sentAt, TranscriptPath: path}
		set := func(f func(*sendqueue.Record)) error { f(rec); return nil }
		watchLanded("", rec, set)
		if rec.State != sendqueue.StateLanded || rec.LandedRowID != "m1" || rec.Verdict != "delivered" {
			t.Fatalf("record = %+v", rec)
		}
	})
}
