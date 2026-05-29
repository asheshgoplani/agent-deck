package session

// Audit B3 — ReadDeadLetter must skip corrupt lines (matching ReadAndTruncateInbox)
// rather than abort on the first bad one. Dead-letter is the operator's last-resort
// forensic trail; one truncated/garbled line must not blind them to everything after.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestB3_ReadDeadLetter_SkipsCorruptLinesKeepsRest(t *testing.T) {
	inboxTestHome(t)
	child := "child-dl-1777300000"

	path := DeadLetterPathFor(child)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Two valid records sandwiching a corrupt line.
	content := `{"child_session_id":"` + child + `","done_status":"success","fp":"a"}
this-is-not-json{{{
{"child_session_id":"` + child + `","done_status":"error","fp":"b"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write dead-letter: %v", err)
	}

	recs, err := ReadDeadLetter(child)
	if err != nil {
		t.Fatalf("ReadDeadLetter must not error on a corrupt line: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected the 2 valid records (corrupt line skipped), got %d: %+v", len(recs), recs)
	}
	if recs[0].DoneStatus != "success" || recs[1].DoneStatus != "error" {
		t.Fatalf("valid records out of order or wrong: %+v", recs)
	}
}
