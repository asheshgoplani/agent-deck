package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #3131: the hook sees every Claude prompt in full, however long the
// turn it starts. It records the sender of a tagged prompt next to turn_seq,
// so the daemon can still attribute a turn whose prompt has scrolled out of
// the transcript tail window.

type promptSenderFields struct {
	TurnSeq       uint64 `json:"turn_seq"`
	PromptFromID  string `json:"prompt_from_id"`
	PromptTurnSeq uint64 `json:"prompt_turn_seq"`
}

func readPromptSender(t *testing.T, id string) promptSenderFields {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	var f promptSenderFields
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal hook file: %v", err)
	}
	return f
}

func promptPayload(prompt string) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "8f3c2a1e-0000-4000-8000-000000003131", "cwd": "/tmp",
		"hook_event_name": "UserPromptSubmit", "prompt": prompt,
	})
	return string(b)
}

const stopPayload3131 = `{"session_id":"8f3c2a1e-0000-4000-8000-000000003131","cwd":"/tmp","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"done"}`

func TestIssue3131_HookRecordsTheSenderOfATaggedPrompt(t *testing.T) {
	hookEventsHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_JOB_DIR", "")
	const id = "inst-3131"
	t.Setenv("AGENTDECK_INSTANCE_ID", id)

	// A long --message-file send can arrive as a paste; the hook prompt may
	// carry the wrapper too.
	for _, prompt := range []string{
		"[agent-deck from:coord-1]\nfix round 2: address the review",
		"<pasted_content id=\"p1\">\n[agent-deck from:coord-1]\nfix round 2\n</pasted_content>",
	} {
		runHookHandlerWith(t, promptPayload(prompt))
		got := readPromptSender(t, id)
		if got.PromptFromID != "coord-1" || got.PromptTurnSeq == 0 || got.PromptTurnSeq != got.TurnSeq {
			t.Fatalf("after tagged prompt %q: %+v, want prompt_from_id coord-1 at this turn_seq", prompt, got)
		}
		runHookHandlerWith(t, stopPayload3131)
		after := readPromptSender(t, id)
		if after.PromptFromID != "coord-1" || after.PromptTurnSeq != got.PromptTurnSeq || after.TurnSeq != got.PromptTurnSeq+1 {
			t.Fatalf("the Stop must carry the record and move turn_seq by one: %+v (prompt at %d)", after, got.PromptTurnSeq)
		}
	}

	// An untagged prompt clears it: the next turn is not a send.
	runHookHandlerWith(t, promptPayload("a person typed this"))
	if got := readPromptSender(t, id); got.PromptFromID != "" || got.PromptTurnSeq != 0 {
		t.Fatalf("an untagged prompt must clear the sender: %+v", got)
	}
	// A pasted log that only mentions an envelope further down is not a send.
	runHookHandlerWith(t, promptPayload("<pasted_content id=\"p2\">\nlog:\n[agent-deck from:coord-1]\n</pasted_content>"))
	if got := readPromptSender(t, id); got.PromptFromID != "" {
		t.Fatalf("a paste that merely contains an envelope is not a send: %+v", got)
	}
}
