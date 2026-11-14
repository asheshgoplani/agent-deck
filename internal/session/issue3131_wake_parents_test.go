package session

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Issue #3131: [inbox] wake_parents, the opt-in wake for any Claude parent.

// [inbox] wake_parents (off by default) lets an urgent record wake an idle
// Claude parent that is not a conductor. Nothing else changes: a busy parent,
// a non-Claude parent and the info digest keep the old gate.
func TestIssue3131_WakeParentsOptInGate(t *testing.T) {
	withNoopStatusProbe(t)
	t.Cleanup(func() { inboxConfigOverride = nil })
	worker := &Instance{ID: "w", Title: "coordinator", Tool: "claude", Status: StatusIdle}
	shell := &Instance{ID: "s", Title: "coordinator", Tool: "shell", Status: StatusIdle}
	busy := &Instance{ID: "b", Title: "coordinator", Tool: "claude", Status: StatusRunning}
	conductor := &Instance{ID: "c", Title: "conductor-ops", Tool: "claude", Status: StatusIdle}

	inboxConfigOverride = &InboxConfig{}
	if parentIsNudgeableIdle(worker, "parent") {
		t.Fatal("default: a non-conductor parent is not woken")
	}
	if !parentIsNudgeableIdle(conductor, inboxWakeKindDigest) {
		t.Fatal("default: the digest still wakes a conductor")
	}

	inboxConfigOverride = &InboxConfig{WakeParents: boolPtr(true)}
	if !parentIsNudgeableIdle(worker, "parent") {
		t.Fatal("wake_parents = true: an idle Claude parent is woken")
	}
	if parentIsNudgeableIdle(shell, "parent") {
		t.Fatal("wake_parents never types into a non-Claude pane")
	}
	if parentIsNudgeableIdle(busy, "parent") {
		t.Fatal("wake_parents never types into a busy pane")
	}
	if parentIsNudgeableIdle(worker, inboxWakeKindDigest) {
		t.Fatal("wake_parents covers urgent records only, not the info digest")
	}
	if !parentIsNudgeableIdle(conductor, inboxWakeKindDigest) {
		t.Fatal("wake_parents leaves the conductor digest as it was")
	}

	inboxConfigOverride = &InboxConfig{WakeParents: boolPtr(false)}
	if parentIsNudgeableIdle(worker, "parent") {
		t.Fatal("wake_parents = false: a non-conductor parent is not woken")
	}
}

// End to end: an untagged child turn (a human prompt, a task notification)
// that ends urgent wakes its idle non-conductor parent only with the opt-in.
func TestIssue3131_WakeParentsOptInEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       *InboxConfig
		wantWoken int
	}{
		{"default", &InboxConfig{}, 0},
		{"wake_parents", &InboxConfig{WakeParents: boolPtr(true)}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR5Fixture(t)
			nonConductorParent(t, f)
			inboxConfigOverride = tc.cfg
			f.appendTurn(t, fxHuman("u0", "continue with step 3"), fxAssistantText("a0", issue3131Done))
			f.pollTurns(t)
			if got := f.inboxRecords(t); len(got) != 1 || got[0].Tier != TurnTierUrgent {
				t.Fatalf("one urgent record: %+v", got)
			}
			if f.woken[f.parent.ID] != tc.wantWoken {
				t.Fatalf("woken=%v, want %d", f.woken, tc.wantWoken)
			}
		})
	}
}

func TestIssue3131_WakeParentsConfigMerge(t *testing.T) {
	base := InboxConfig{WakeParents: boolPtr(true)}
	if !base.merged(&InboxConfig{}).GetWakeParents() {
		t.Fatal("an override without the key keeps the base value")
	}
	if base.merged(&InboxConfig{WakeParents: boolPtr(false)}).GetWakeParents() {
		t.Fatal("an override can turn it off")
	}
	if (InboxConfig{}).GetWakeParents() {
		t.Fatal("default is off")
	}
}

// wakeExecSpy swaps wakeNudgeExec for a spy that reports each exec's args.
func wakeExecSpy(t *testing.T) <-chan []string {
	t.Helper()
	orig := wakeNudgeExec
	t.Cleanup(func() { wakeNudgeExec = orig })
	ch := make(chan []string, 8)
	wakeNudgeExec = func(_ context.Context, _ string, args ...string) error {
		ch <- append([]string(nil), args...)
		return nil
	}
	return ch
}

// Owner condition: a parent with typed text in its input box is never written
// to. Both new wake paths must end in the guarded `session send` pipeline
// (its composer-draft guard refuses rather than type over a draft), never a
// raw keystroke route, and an ordinary parent's wake also refuses an open
// menu (--require-input-prompt). This pins the production sender for both.
func TestIssue3131_NewWakePathsUseTheGuardedSend(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    *InboxConfig
		prompt func(f *pr5Fixture) string
	}{
		{"kickoff reply", &InboxConfig{}, func(f *pr5Fixture) string { return SendEnvelope(f.parent.ID) + "\nkickoff task" }},
		{"wake_parents", &InboxConfig{WakeParents: boolPtr(true)}, func(*pr5Fixture) string { return "continue with step 3" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR5Fixture(t)
			nonConductorParent(t, f)
			inboxConfigOverride = tc.cfg
			execs := wakeExecSpy(t)
			f.d.notifier.wake.send = sendWakeNudge // the production sender

			f.appendTurn(t, fxHuman("u0", tc.prompt(f)), fxAssistantText("a0", issue3131Done))
			f.pollTurns(t)

			var args []string
			select {
			case args = <-execs:
			case <-time.After(5 * time.Second):
				t.Fatal("no wake send was dispatched")
			}
			select {
			case extra := <-execs:
				t.Fatalf("exactly one wake send, got another: %q", extra)
			case <-time.After(100 * time.Millisecond):
			}
			i := slices.Index(args, "session")
			if i < 0 || len(args) < i+8 {
				t.Fatalf("wake args = %q", args)
			}
			got := args[i:]
			want := []string{"session", "send", f.parent.ID, got[3], "--no-wait", "--no-tag", "--require-input-prompt", "-q"}
			if !slices.Equal(got, want) || !strings.HasPrefix(got[3], "[INBOX]") {
				t.Fatalf("wake args = %q, want %q with an [INBOX] line", got, want)
			}
			for _, bad := range []string{"--force", "--queue", "--draft"} {
				if slices.Contains(args, bad) {
					t.Fatalf("wake args must not carry %s: %q", bad, args)
				}
			}
		})
	}
}

// The conductor wake, and a non-Claude typed fallback, keep the send they
// always had: no --require-input-prompt.
func TestIssue3131_ConductorWakeArgsUnchanged(t *testing.T) {
	conductor := &Instance{ID: "c", Title: "conductor-ops", Tool: "claude"}
	worker := &Instance{ID: "w", Title: "coordinator", Tool: "claude"}
	if wakeNeedsInputPrompt(conductor) {
		t.Fatal("a conductor wake must not change")
	}
	if !wakeNeedsInputPrompt(worker) {
		t.Fatal("an ordinary session's wake refuses an open menu")
	}
	// #3024's typed fallback for a pi doorbell (no extension listening) keeps
	// its send: the input-prompt guard reads Claude's composer.
	if wakeNeedsInputPrompt(&Instance{ID: "p", Title: "coordinator", Tool: "pi"}) {
		t.Fatal("a non-Claude typed wake must keep the send it always had")
	}
	want := []string{"-p", "work", "session", "send", "c", "line", "--no-wait", "--no-tag", "-q"}
	if got := wakeNudgeSendArgs("work", "c", "line", false); !slices.Equal(got, want) {
		t.Fatalf("conductor wake args = %q, want %q", got, want)
	}
}
