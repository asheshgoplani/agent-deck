package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// T10: removing a session publishes macapp.session.closed {reason:"removed"}
// on the session's profile bus so a following Mac app can forget its tabs.
func TestSessionRemovePublishesSessionClosed(t *testing.T) {
	home := t.TempDir()
	first := addOpenTestSession(t, home, "Closing one")
	second := addOpenTestSession(t, home, "Closing two")
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", first, "--force", "--json"); code != 0 {
		t.Fatalf("session remove: %d %s %s", code, stdout, stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "remove", second, "--json"); code != 0 {
		t.Fatalf("remove: %d %s %s", code, stdout, stderr)
	}
	stats := openTestBusDir(t, home)
	if got := stats.Kinds[events.KindMacappSessionClosed]; got != 2 {
		t.Fatalf("session.closed frames: %d (%+v)", got, stats.Kinds)
	}
	reader, err := events.Open(stats.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := reader.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := map[string]bool{}
	for frame := range sub.Frames() {
		if frame.Kind != events.KindMacappSessionClosed {
			continue
		}
		var data map[string]string
		if err := json.Unmarshal(frame.Data, &data); err != nil || data["reason"] != "removed" || len(data) != 1 {
			t.Fatalf("frame data: %s %v", frame.Data, err)
		}
		closed[frame.SessionID] = true
		if len(closed) == 2 {
			break
		}
	}
	if !closed[first] || !closed[second] {
		t.Fatalf("closed sessions: %v, want %s and %s", closed, first, second)
	}
}
