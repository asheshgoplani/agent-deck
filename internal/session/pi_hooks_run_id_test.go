package session

import "testing"

// runIDs returns the run_id each turn_start, turn_end and agent_settled sent,
// in order.
func runIDs(t *testing.T, sent []piSent) []string {
	t.Helper()
	var ids []string
	for _, s := range sent {
		switch s.Event {
		case "turn_start", "turn_end", "agent_settled":
			for _, p := range s.Payloads {
				ids = append(ids, str(p, "run_id"))
			}
		}
	}
	return ids
}

// One pi run (_startRun) can call agent.continue() several times: on
// auto-retry, on overflow compaction and when agent_before_settle asks to
// continue. Each continue fires agent_start again, but the run settles once.
// Every sub-run must carry the same run_id, or hook-handler carries the
// earlier sub-run's turn_end as an orphan run and the parent is woken twice.
func TestPiHookExtension_ContinueKeepsTheRunID(t *testing.T) {
	h := newPiHarness(t)
	steps := []piStep{
		{Event: "input", Payload: map[string]any{"text": "do it", "source": "interactive"}},
		{Event: "before_agent_start", Payload: map[string]any{}},
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
		{Event: "turn_end", Payload: withStopReason(piTurnEndPayload("Overloaded.", "entry-1", "error"), "error")},
		// auto-retry: agent.continue()
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
		{Event: "turn_end", Payload: piTurnEndPayload("Half done.", "entry-2", "completed")},
		// agent_before_settle asked to continue: agent.continue()
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
		{Event: "turn_end", Payload: piTurnEndPayload("All done.", "entry-3", "completed")},
		{Event: "agent_settled", Payload: map[string]any{"aborted": false}},
		// The next run gets a new token.
		{Event: "input", Payload: map[string]any{"text": "next", "source": "interactive"}},
		{Event: "before_agent_start", Payload: map[string]any{}},
		{Event: "agent_start", Payload: map[string]any{}},
		{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
		{Event: "turn_end", Payload: piTurnEndPayload("Next answer.", "entry-4", "completed")},
		{Event: "agent_settled", Payload: map[string]any{"aborted": false}},
	}
	sent := h.run(steps)
	ids := runIDs(t, sent)
	if len(ids) != 10 {
		t.Fatalf("want 10 run-carrying payloads, got %d: %v", len(ids), ids)
	}
	first, second := ids[:7], ids[7:]
	for i, id := range first {
		if id == "" || id != first[0] {
			t.Fatalf("payload %d of the first run carries run_id %q, want %q for every sub-run: %v", i, id, first[0], ids)
		}
	}
	for _, id := range second {
		if id == "" || id != second[0] {
			t.Fatalf("second run's run_ids differ: %v", ids)
		}
	}
	if second[0] == first[0] {
		t.Fatalf("a new run must get a new run_id after the previous one settled: %v", ids)
	}
	settle := settlePayloads(t, sent)
	if len(settle) != 1 || str(settle[0], "turn_id") != "entry-4" {
		t.Fatalf("settle: %v", settle)
	}
}

// A pi without agent_settled still starts each prompt with before_agent_start,
// so each prompt still gets its own run_id.
func TestPiHookExtension_NewPromptGetsANewRunIDWithoutSettle(t *testing.T) {
	h := newPiHarness(t)
	run := func(prompt, entry string) []piStep {
		return []piStep{
			{Event: "input", Payload: map[string]any{"text": prompt, "source": "interactive"}},
			{Event: "before_agent_start", Payload: map[string]any{}},
			{Event: "agent_start", Payload: map[string]any{}},
			{Event: "turn_start", Payload: map[string]any{"turnIndex": 0}},
			{Event: "turn_end", Payload: piTurnEndPayload("Answer.", entry, "completed")},
		}
	}
	ids := runIDs(t, h.run(append(run("one", "entry-1"), run("two", "entry-2")...)))
	if len(ids) != 4 || ids[0] == "" || ids[0] != ids[1] || ids[2] != ids[3] || ids[0] == ids[2] {
		t.Fatalf("each prompt must carry its own run_id: %v", ids)
	}
}
