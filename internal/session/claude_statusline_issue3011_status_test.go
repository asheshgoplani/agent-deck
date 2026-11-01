package session

import (
	"strings"
	"testing"
)

// hooks status names a nested wrapper and an unusually large settings.json
// so the operator learns before Claude Code stops loading the file.
func TestIssue3011_StatusWarnsNestedAndLarge(t *testing.T) {
	stubUsageFeedExecutable(t, "/usr/local/bin/agent-deck")
	nested := "~/.claude/statusline.sh"
	for i := 0; i < 3; i++ {
		nested = wrapStatusLine("/home/u/.local/bin/agent-deck-bin", "default", nested)
	}
	dir := t.TempDir()
	writeSettings(t, dir, `{"statusLine":{"type":"command","command":`+string(mustMarshal(nested))+`}}`)
	feed := UsageFeedStatus(dir, "default")
	if feed.Depth != 3 || feed.Inner != "~/.claude/statusline.sh" {
		t.Fatalf("status = %+v; want depth 3 around the user command", feed)
	}
	if !strings.Contains(strings.Join(feed.Warnings, "\n"), "nested 3 times") {
		t.Fatalf("warnings = %q; want the nesting named", feed.Warnings)
	}

	big := t.TempDir()
	writeSettings(t, big, `{"statusLine":{"type":"command","command":"x"},"pad":"`+strings.Repeat("a", settingsLargeBytes)+`"}`)
	feed = UsageFeedStatus(big, "default")
	if !strings.Contains(strings.Join(feed.Warnings, "\n"), "unusually large") {
		t.Fatalf("warnings = %q; want the size named", feed.Warnings)
	}
}
