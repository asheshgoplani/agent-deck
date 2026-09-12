package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// These failure-injection tests are intentionally source-only in this task.
// They use no tmux, process, credential, or real-transcript fixture.
func TestCrossHarnessFailure_NoSourceMutationForDistinctTarget(t *testing.T) {
	source := &Instance{ID: "source", Tool: "claude", Account: "personal", ClaudeSessionID: "source-native", ProjectPath: "/source", Title: "source"}
	before := identityForInstance(source)
	plan := &FreshTargetLaunchPlan{Target: FreshTargetIdentity{InstanceID: "target", Tool: "codex", Account: "work", ProjectPath: "/source", Title: "source", GroupPath: "group"}, NativeCommand: "codex"}
	target := newCrossHarnessTarget(plan)
	if target.ID == source.ID || target.Tool == source.Tool {
		t.Fatal("cross-harness target reused source identity")
	}
	if got := identityForInstance(source); got != before {
		t.Fatal("creating a target mutated the source identity")
	}
}

func TestCrossHarnessFailure_WrongOrStaleNativeEvidenceIsRejected(t *testing.T) {
	plan := &FreshTargetLaunchPlan{Source: ContextSourceIdentity{SessionID: "source-native"}, Target: FreshTargetIdentity{InstanceID: "target", Tool: "claude", SessionID: "target-native"}}
	for _, evidence := range []CrossHarnessTargetEvidence{
		{InstanceID: "stale", Tool: "claude", SessionID: "target-native", Ready: true},
		{InstanceID: "target", Tool: "claude", SessionID: "source-native", Ready: true},
		{InstanceID: "target", Tool: "claude", SessionID: "target-native", Ready: false},
	} {
		if err := validateCrossHarnessEvidence(evidence, plan); err == nil {
			t.Fatalf("stale evidence accepted: %#v", evidence)
		}
	}
}

func TestCrossHarnessFailure_TimeoutStaysPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancelled readiness deadline was not observable")
	}
	journal := &crossHarnessJournal{Version: 1, OperationID: "op", State: crossHarnessStarted}
	result := &CrossHarnessSwitchResult{}
	path := filepath.Join(t.TempDir(), "pending.json")
	got, err := pendingCrossHarness(path, journal, result, "missing target-native readiness contract for codex: context canceled")
	if !errors.Is(err, ErrCrossHarnessPending) || !got.Pending || got.TargetReady {
		t.Fatalf("timeout must remain pending/not-ready: result=%#v err=%v", got, err)
	}
}

func TestCrossHarnessFailure_PendingJournalWriteFailureRetainsPendingResult(t *testing.T) {
	journal := &crossHarnessJournal{Version: 1, OperationID: "op", State: crossHarnessStarted}
	result := &CrossHarnessSwitchResult{Target: &Instance{ID: "target"}, TargetCreated: true}
	// A directory cannot be atomically replaced with the journal file, which
	// injects a journal write failure without a process or filesystem fixture.
	got, err := pendingCrossHarness(t.TempDir(), journal, result, "missing target-native readiness contract")
	if got != result || !got.Pending || got.TargetReady || !errors.Is(err, ErrCrossHarnessPending) || !errors.Is(err, ErrCrossHarnessRecoveryRequired) {
		t.Fatalf("pending journal failure lost recovery state: result=%#v err=%v", got, err)
	}
	if !strings.Contains(got.MissingContract, "recovery required") || got.Target == nil || !got.TargetCreated {
		t.Fatalf("pending journal failure lost committed target metadata: %#v", got)
	}
}

func TestCrossHarnessFailure_RetryRecoveryRequiresExactJournalIdentity(t *testing.T) {
	plan := &FreshTargetLaunchPlan{
		Source: ContextSourceIdentity{InstanceID: "source", Tool: "claude", SessionID: "source-native", Account: "personal",
			ProjectPath: "/workspace/repo", WorkingDir: "/workspace/repo/subdir", Title: "source", GroupPath: "group"},
		SourceArtifact: ContextArtifact{SourceSHA256: "hash"},
		Target:         FreshTargetIdentity{InstanceID: "target", Tool: "codex", Account: "work", ProjectPath: "/workspace/repo", Title: "source", GroupPath: "group"},
		TargetHome:     "/tmp/codex-work",
		NativeCommand:  "codex",
		NativeArgs:     []string{"-C", "/workspace/repo"},
	}
	journal := &crossHarnessJournal{Version: 4, OperationID: crossHarnessOperationID(plan), State: crossHarnessStarted, Source: plan.Source, Target: plan.Target, Launch: launchBindingForPlan(plan), SourceSHA256: "hash"}
	if !crossHarnessJournalMatches(journal, plan, false) {
		t.Fatal("retry should recover only the exact pending operation")
	}
	journal.Target.InstanceID = "other-target"
	if crossHarnessJournalMatches(journal, plan, false) {
		t.Fatal("retry accepted a stale target journal")
	}
	journal.Target = plan.Target
	journal.Source.WorkingDir = "/workspace/other"
	if crossHarnessJournalMatches(journal, plan, false) {
		t.Fatal("retry accepted a journal without the complete source identity")
	}
}
