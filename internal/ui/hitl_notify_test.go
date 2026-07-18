package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWaitingNotifyText(t *testing.T) {
	inst := session.NewInstanceWithTool("my-proj", "/x/my-proj", "claude")
	title, body := waitingNotifyText(inst)
	if title != "agent-hopdeck" {
		t.Fatalf("title = %q, want agent-hopdeck", title)
	}
	if body == "" || body == "my-proj" {
		// body should mention the session and that it is waiting
		t.Fatalf("body = %q, want a 'waiting' message mentioning the session", body)
	}
}
