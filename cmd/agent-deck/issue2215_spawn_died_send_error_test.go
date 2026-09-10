package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2215: when a pane dies before the -m message is delivered, launch
// reported the paste-buffer transport fault ("delivery is indeterminate and
// must not be retried") instead of the spawn death that is already recorded in
// the spawn-failure sidecar.
//
// sendErrOrSpawnDied selects the right error based on pane state and the
// presence of a spawn-failure record. These tests cover all three branches.

var pasteBufferErr = errors.New("paste-buffer failed after writing to the pane, delivery is indeterminate and must not be retried: exit status 1")

// TestIssue2215_PaneAliveKeepsOriginalError: when the pane is still alive the
// paste-buffer error is the correct diagnosis; leave it unchanged.
func TestIssue2215_PaneAliveKeepsOriginalError(t *testing.T) {
	result := sendErrOrSpawnDied(pasteBufferErr, false /* paneGone */, nil)
	if result != pasteBufferErr {
		t.Fatalf("pane alive: want original error unchanged, got %v", result)
	}
}

// TestIssue2215_PaneGoneNoRecordKeepsOriginalError: if the pane is gone but
// there is no spawn-failure record, keep the original error so the operator
// still has something actionable.
func TestIssue2215_PaneGoneNoRecordKeepsOriginalError(t *testing.T) {
	result := sendErrOrSpawnDied(pasteBufferErr, true /* paneGone */, nil)
	if result != pasteBufferErr {
		t.Fatalf("pane gone but no record: want original error, got %v", result)
	}
}

// TestIssue2215_PaneGoneWithRecordReportsSpawnDeath: the main regression case.
// When the pane died and a spawn-failure record exists, the error must name
// the spawn death (with reason and elapsed_ms) and must NOT say "indeterminate"
// or "must not be retried" -- delivery definitively did not happen.
func TestIssue2215_PaneGoneWithRecordReportsSpawnDeath(t *testing.T) {
	rec := &session.SpawnFailureRecord{
		Reason:    "spawn_died_fast",
		ElapsedMs: 256,
	}
	result := sendErrOrSpawnDied(pasteBufferErr, true /* paneGone */, rec)

	if result == nil {
		t.Fatal("expected non-nil error")
	}
	msg := result.Error()

	if !strings.Contains(msg, "spawn_died_fast") {
		t.Errorf("error must include spawn reason; got: %q", msg)
	}
	if !strings.Contains(msg, "256") {
		t.Errorf("error must include elapsed_ms; got: %q", msg)
	}
	if !strings.Contains(msg, "did not happen") {
		t.Errorf("error must say delivery did not happen; got: %q", msg)
	}
	if strings.Contains(msg, "indeterminate") {
		t.Errorf("error must not say 'indeterminate' when pane died; got: %q", msg)
	}
	if strings.Contains(msg, "must not be retried") {
		t.Errorf("error must not block relaunch with 'must not be retried'; got: %q", msg)
	}
}
