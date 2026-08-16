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

// Review round 2, finding 2: `[]` is the contract for "nothing pending", so a
// remote that says NOTHING has failed the read. Returning no-records here would
// be the same silent zero the corrupt-ledger path forbids.
func TestIssue1948R2_FetchPendingRecords_EmptyStdoutIsAnError(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return []byte("   \n"), nil
	})

	records, err := r.FetchPendingRecords(context.Background())
	if err == nil {
		t.Fatalf("silence must not read as an empty host, got %d records", len(records))
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Fatalf("the error should say the remote produced nothing, got: %v", err)
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

// Review round 2, finding 1: a genuinely old remote does NOT answer with a
// usage screen on stdout — its flag parser rejects --json and it exits 1, so
// the failure arrives as an error from Run. (The previous test pinned an
// exit-0-with-usage response the real old binary never emits, which is why the
// version pointer never fired for the case it was written for.) Diagnosing the
// version belongs to the caller, which can probe it; see
// TestIssue1948R2_StaleRemoteBinaryHint in the cmd package.
func TestIssue1948R2_FetchPendingRecords_OldRemoteExitsNonZero(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return nil, errors.New("ssh command failed: exit status 1: flag provided but not defined: -json")
	})

	_, err := r.FetchPendingRecords(context.Background())
	if err == nil {
		t.Fatalf("an old remote's non-zero exit must surface as an error")
	}
	if !strings.Contains(err.Error(), "not defined: -json") {
		t.Fatalf("the remote's own complaint must survive: %v", err)
	}
}

// Any other non-array answer (a shell banner ahead of the JSON, say) is an
// error too — but it must NOT be diagnosed as an old binary, which is what the
// stdout-shape guess used to do to healthy remotes.
func TestIssue1948R2_FetchPendingRecords_NonArrayIsAnErrorWithoutGuessingWhy(t *testing.T) {
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: "agent-deck"}
	SetSSHRunnerRunFnForTest(r, func(args ...string) ([]byte, error) {
		return []byte("Welcome to box-b\n[]\n"), nil
	})

	_, err := r.FetchPendingRecords(context.Background())
	if err == nil {
		t.Fatalf("a non-array answer must be an error")
	}
	if strings.Contains(err.Error(), "older") || strings.Contains(err.Error(), "remote update") {
		t.Fatalf("the transport must not guess at the cause: %v", err)
	}
}
