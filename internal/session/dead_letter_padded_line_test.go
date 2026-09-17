package session

// PR #2230 review finding: removeDeadLetterEntry hashed the TRIMMED scanner
// line when matching entry.record.ID, but readDeadLetterEntries hashes the
// RAW (untrimmed) scanner bytes when first assigning that ID. A record whose
// on-disk line carries incidental leading/trailing whitespace therefore gets
// an ID at list time that removeDeadLetterEntry can never reproduce, making
// it permanently unmatchable for retry/purge. The same rewrite path also
// stored every KEPT line trimmed, silently re-writing (and re-IDing) sibling
// records that were never touched by the requested removal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeadLetter_PurgeMatchesPaddedLine_PreservesSiblingBytes(t *testing.T) {
	inboxTestHome(t)
	child := "child-dl-padded"

	path := DeadLetterPathFor(child)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The first record's line carries incidental padding (e.g. from a prior
	// partial write or an editor); the second is untouched and must survive
	// the purge byte-for-byte.
	paddedLine := `  {"kind":"finished","child_session_id":"` + child + `","done_status":"success"}  `
	siblingLine := `{"kind":"finished","child_session_id":"` + child + `","done_status":"error"}`
	content := paddedLine + "\n" + siblingLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write dead-letter: %v", err)
	}

	records, err := ListDeadLetters()
	if err != nil {
		t.Fatalf("ListDeadLetters: %v", err)
	}
	var paddedID string
	for _, rec := range records {
		if rec.ChildSessionID == child && strings.Contains(rec.PayloadSummary, "status=success") {
			paddedID = rec.ID
		}
	}
	if paddedID == "" {
		t.Fatalf("expected to find the padded record among listed dead letters: %+v", records)
	}

	if err := PurgeDeadLetter(paddedID); err != nil {
		t.Fatalf("PurgeDeadLetter(%q) on a whitespace-padded record must succeed, got: %v", paddedID, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dead-letter file after purge: %v", err)
	}
	remaining := strings.TrimRight(string(raw), "\n")
	if remaining != siblingLine {
		t.Fatalf("purge must leave the untouched sibling line's original bytes unchanged:\n got:  %q\n want: %q", remaining, siblingLine)
	}
}
