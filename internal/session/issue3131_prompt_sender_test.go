package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #3131, left open by #3132: a child turn longer than the transcript
// tail window is attributed from the prompt the hook recorded.

// writeHookPromptRecord writes the child's hook status file as the hook
// handler leaves it after the Stop that ended a tagged turn: turn_seq is the
// count after the Stop, prompt_turn_seq the count the prompt edge wrote.
func writeHookPromptRecord(t *testing.T, childID string, fields map[string]any) {
	t.Helper()
	dir := GetHooksDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{"status": "waiting", "event": "Stop", "ts": 1}
	for k, v := range fields {
		rec[k] = v
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, childID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// longTaggedTurn appends a tagged prompt from fromID, then more tool records
// than the transcript tail window holds, then the final text. The tail scan
// never reaches the prompt, so on its own it classifies the turn as unknown.
func longTaggedTurn(t *testing.T, f *pr5Fixture, fromID, final string) {
	t.Helper()
	lines := []string{fxHuman("u0", SendEnvelope(fromID)+"\nfix round 2: address the review")}
	for i := 0; i < turnScanTailLines; i++ {
		lines = append(lines, fxAssistantToolUse("t"), fxToolResult("r"))
	}
	lines = append(lines, fxAssistantText("a-final", final))
	f.appendTurn(t, lines...)
}

func nonConductorParent(t *testing.T, f *pr5Fixture) {
	t.Helper()
	f.parent.Title = "coordinator"
	f.parent.Tool = "claude"
	f.saveRegistry(t)
}

const issue3131Done = "All review points addressed.\n===AGENTDECK_DONE=== status=ok summary=fix round 2 done"

// A child turn longer than the tail window answers its parent's tagged send.
// The hook saw the prompt, and the turn that just ended is exactly the one
// that prompt started, so the turn is a send from the parent: its urgent
// record wakes the idle (non-conductor) parent as a reply target.
func TestIssue3131_LongTurnIsAttributedFromThePromptRecord(t *testing.T) {
	f := newPR5Fixture(t)
	nonConductorParent(t, f)
	longTaggedTurn(t, f, f.parent.ID, issue3131Done)
	writeHookPromptRecord(t, f.child.ID, map[string]any{
		"turn_seq": 6, "prompt_from_id": f.parent.ID, "prompt_turn_seq": 5,
	})
	f.pollTurns(t)

	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Trigger != TurnTriggerSend || got[0].FromID != f.parent.ID || got[0].Tier != TurnTierUrgent {
		t.Fatalf("the long turn must be a send from the parent (urgent): %+v", got)
	}
	if f.woken[f.parent.ID] != 1 || f.kinds[f.parent.ID] != InboxTargetKindReply {
		t.Fatalf("the parent that asked must be woken once as a reply target: woken=%v kinds=%v", f.woken, f.kinds)
	}
}

// The prompt record is used only when nothing else could have started the
// turn: exactly one turn edge (the Stop) since the prompt it names. Any other
// count, a missing record or a record with no sender leaves the turn unknown,
// as before.
func TestIssue3131_PromptRecordIsIgnoredWhenItIsNotThisTurns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"another turn ended since the prompt", map[string]any{"turn_seq": 8, "prompt_from_id": "parent-2469", "prompt_turn_seq": 5}},
		{"the turn has not ended yet", map[string]any{"turn_seq": 5, "prompt_from_id": "parent-2469", "prompt_turn_seq": 5}},
		{"the last prompt was not tagged", map[string]any{"turn_seq": 6}},
		{"no hook file", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR5Fixture(t)
			nonConductorParent(t, f)
			longTaggedTurn(t, f, f.parent.ID, issue3131Done)
			if tc.fields != nil {
				writeHookPromptRecord(t, f.child.ID, tc.fields)
			}
			f.pollTurns(t)

			got := f.inboxRecords(t)
			if len(got) != 1 || got[0].Trigger != TurnTriggerUnknown || got[0].FromID != "" {
				t.Fatalf("the turn must stay unknown: %+v", got)
			}
			if f.woken[f.parent.ID] != 0 {
				t.Fatalf("a non-conductor parent is not woken for an unattributed turn: %v", f.woken)
			}
		})
	}
}

// A turn whose prompt is inside the tail window keeps the transcript's answer
// even when the hook record names someone else.
func TestIssue3131_TranscriptTriggerWinsOverThePromptRecord(t *testing.T) {
	f := newPR5Fixture(t)
	nonConductorParent(t, f)
	f.appendTurn(t, fxHuman("u0", "a person typed this"), fxAssistantText("a0", issue3131Done))
	writeHookPromptRecord(t, f.child.ID, map[string]any{
		"turn_seq": 6, "prompt_from_id": f.parent.ID, "prompt_turn_seq": 5,
	})
	f.pollTurns(t)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Trigger != TurnTriggerHuman || got[0].FromID != "" {
		t.Fatalf("a prompt the transcript shows decides the trigger: %+v", got)
	}
}
