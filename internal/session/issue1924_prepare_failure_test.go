package session

import (
	"errors"
	"strings"
	"testing"
)

// A start that fails before tmux is ever invoked must still leave a reason
// behind. #1924: the session sat on StatusError with no tmux session, no
// spawn-failure record, and nothing in `session show` — an error state with no
// reason, which the issue rightly calls a guess rendered as fact.
func TestIssue1924_PrepareFailureLeavesARecord(t *testing.T) {
	withTempHome(t)

	inst := &Instance{ID: "prep-1924", Title: "t", Tool: "codex", Command: "codex"}
	t.Cleanup(func() { clearSpawnFailureRecord(inst.ID) })

	if rec := inst.SpawnFailure(); rec != nil {
		t.Fatalf("precondition: expected no record, got %+v", rec)
	}

	inst.recordPrepareFailure("env BROKEN {command}", errors.New("sandbox image pull failed"))

	rec := inst.SpawnFailure()
	if rec == nil {
		t.Fatal("no spawn-failure record written for a pre-tmux start failure")
	}
	if rec.Reason != "prepare_failed" {
		t.Errorf("Reason = %q, want %q", rec.Reason, "prepare_failed")
	}
	if !strings.Contains(rec.DyingOutput, "sandbox image pull failed") {
		t.Errorf("record does not carry the error text: %q", rec.DyingOutput)
	}
	if rec.Command != "env BROKEN {command}" {
		t.Errorf("Command = %q, want the command that failed to prepare", rec.Command)
	}
}

// The rendered text must not claim something ran. The default arm says the
// session "ended unexpectedly during startup", which is wrong when nothing was
// ever launched — and a confidently wrong explanation is worse than a vague one
// for someone trying to diagnose a failure.
func TestIssue1924_PrepareFailureDisplayDoesNotClaimItRan(t *testing.T) {
	rec := &SpawnFailureRecord{
		InstanceID: "x", Tool: "codex",
		Command: "env BROKEN {command}", Reason: "prepare_failed",
		DyingOutput: "sandbox image pull failed",
	}
	out := rec.FormatForDisplay()

	if !strings.Contains(out, "could not be prepared") {
		t.Errorf("display text should say the command was never launched:\n%s", out)
	}
	if strings.Contains(out, "ended unexpectedly during startup") {
		t.Errorf("display text claims the session ran, but nothing was launched:\n%s", out)
	}
	if !strings.Contains(out, "sandbox image pull failed") {
		t.Errorf("display text drops the actual reason:\n%s", out)
	}
}

// A nil error must not write a record — a start that did not fail has nothing
// to explain, and a stray record would mask the next real one.
func TestIssue1924_NoRecordWithoutAnError(t *testing.T) {
	withTempHome(t)
	inst := &Instance{ID: "prep-1924-nil", Title: "t", Tool: "codex"}
	t.Cleanup(func() { clearSpawnFailureRecord(inst.ID) })

	inst.recordPrepareFailure("cmd", nil)

	if rec := inst.SpawnFailure(); rec != nil {
		t.Errorf("record written for a nil error: %+v", rec)
	}
}
