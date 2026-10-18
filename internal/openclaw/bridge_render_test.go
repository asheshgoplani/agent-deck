package openclaw

import (
	"strings"
	"testing"
	"time"
)

// Everything the bridge renders except its own styling comes from the
// gateway: chat and history content, agent errors, the shutdown reason, the
// server version and the agent's name. None of it may carry a terminal
// control sequence into the view.
func TestBridgeRender_DropsGatewayControlSequences(t *testing.T) {
	hostile := "hi\x1b]52;c;aGk=\x07\x1b[2J\u202Eend"
	m := NewBridgeModel("ws://127.0.0.1:1", "", "agent"+hostile, "name"+hostile)
	m.messages = []ChatMessage{
		{Timestamp: time.Now(), Direction: "system", Content: "Gateway shutting down: " + hostile},
		{Timestamp: time.Now(), Direction: "inbound", Sender: "s" + hostile, Content: "line1\n" + hostile + "\tindent"},
		{Timestamp: time.Now(), Direction: "inbound", AgentID: "a" + hostile, Content: hostile},
		{Timestamp: time.Now(), Direction: "outbound", Content: hostile},
	}

	out := m.renderHeader() + m.renderMessages()
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\u202E"} {
		if strings.Contains(out, bad) {
			t.Fatalf("rendered view contains %q:\n%q", bad, out)
		}
	}
	if !strings.Contains(out, "hi]52;c;aGk=[2Jend") || !strings.Contains(out, "    indent") {
		t.Fatalf("message text lost or tab not expanded:\n%q", out)
	}
}
