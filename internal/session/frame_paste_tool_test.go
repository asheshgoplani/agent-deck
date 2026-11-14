package session

import "testing"

// Codex is the one tool whose composer needs the body framed: an unframed
// `send-keys -l` burst is still being ingested when the submitting Enter
// arrives, so Codex inserts that Enter as a newline instead of submitting.
// Every other tool keeps its historical transport, so this mapping must stay
// exactly {codex: true, everything else: false}.
func TestFramePasteForTool(t *testing.T) {
	if !framePasteForTool("codex") {
		t.Fatal("codex must use the framed paste transport")
	}
	for _, tool := range []string{"claude", "pi", "gemini", "opencode", "devin", "shell", ""} {
		if framePasteForTool(tool) {
			t.Fatalf("%q must keep its historical transport", tool)
		}
	}
}
