package session

import (
	"path/filepath"
	"testing"
	"time"
)

// Issue #2397: the bound Claude turn carries its native identity out to the
// --wait receipt: the user record's UUID and the conversation (transcript)
// it landed in, the Claude counterpart of Codex's accepted_turn.
func TestIssue2397_TurnResponseCarriesNativeIdentity(t *testing.T) {
	path := filepath.Join("testdata", "claude-receipt-turns.jsonl")
	q := TurnQuery{Path: path, Prompt: "Second turn: summarise notes.md in three bullets."}
	id, err := AwaitTurnIdentity(q, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "- one\n- two\n- three" {
		t.Fatalf("reply %q", resp.Content)
	}
	if resp.ClaudeTurnUUID != "u-turn2" {
		t.Errorf("ClaudeTurnUUID = %q, want the bound user record u-turn2", resp.ClaudeTurnUUID)
	}
	if resp.SessionID != "7c1f0a52-3d4e-4b8a-9f61-2a5b8c9d0e13" {
		t.Errorf("SessionID = %q, want the transcript's own sessionId", resp.SessionID)
	}
}
