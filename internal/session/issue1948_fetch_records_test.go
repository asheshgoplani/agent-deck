package session

// Issue #1948 — the transport half: SSHRunner.FetchPendingRecords runs the
// remote's read-only `inbox export` over the SAME ssh path every other remote
// fetch uses, and reads its answer honestly.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIssue1948_FetchPendingRecords_UsesExistingSSHPathAndParsesRecords(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}

	var gotArgs []string
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`[{"child_session_id":"w1","kind":"finished","done_status":"ok",` +
			`"done_summary":"built","timestamp":"2026-08-16T10:00:00Z"}]`), nil
	})

	records, err := r.FetchPendingRecords(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Join(gotArgs, " ") != "inbox export --json" {
		t.Fatalf("must call the remote's read-only export, called: %v", gotArgs)
	}
	if len(records) != 1 || records[0].ChildSessionID != "w1" || records[0].DoneStatus != "ok" {
		t.Fatalf("records not parsed: %+v", records)
	}
	if !records[0].Timestamp.Equal(time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("timestamp not parsed: %v", records[0].Timestamp)
	}
}

func TestIssue1948_FetchPendingRecords_EmptyArrayIsNoRecords(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return []byte("[]\n"), nil
	})

	records, err := r.FetchPendingRecords(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("want no records, got %+v", records)
	}
}

// An ssh failure must surface as an error, never as an empty result — the
// difference between "the fleet is quiet" and "I could not ask".
func TestIssue1948_FetchPendingRecords_SSHFailureIsAnError(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return nil, errors.New("ssh command failed: exit status 255: connection refused")
	})

	if _, err := r.FetchPendingRecords(context.Background()); err == nil {
		t.Fatalf("ssh failure must not read as an empty drain")
	}
}

// A remote too old to know `inbox export` answers with a usage message, not a
// JSON array. That must be an explicit version error, not silence.
func TestIssue1948_FetchPendingRecords_OldRemoteBinaryIsReported(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return []byte("Usage: agent-deck inbox <session-id>\n"), nil
	})

	_, err := r.FetchPendingRecords(context.Background())
	if err == nil {
		t.Fatalf("a non-array answer must be an error")
	}
	if !strings.Contains(err.Error(), "remote update") {
		t.Fatalf("error should point at the fix, got: %v", err)
	}
}
