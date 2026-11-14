package session

import "testing"

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
