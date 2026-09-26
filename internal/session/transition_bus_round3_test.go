package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func TestTransitionBusUsesEventProfileAndStatusIsReserved(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	events.SetProfile("alpha")
	t.Cleanup(func() { events.SetProfile("default") })
	publishTransitionEvent("session.transition", TransitionNotificationEvent{Profile: "alpha", ChildSessionID: "a"})
	publishTransitionEvent("session.transition", TransitionNotificationEvent{Profile: "beta", ChildSessionID: "b"})
	if err := WriteStatusEvent(StatusEvent{InstanceID: "status-only", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	if err := events.CloseDefault(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ profile, session string }{{"alpha", "a"}, {"beta", "b"}} {
		b := events.OpenProfile(tc.profile)
		if got := b.Stats().Cursor; got != 1 {
			t.Errorf("%s cursor = %d, want one transition and no status tap", tc.profile, got)
		}
		line, err := os.ReadFile(filepath.Join(b.Stats().Dir, "active.ndjson"))
		if err != nil {
			t.Fatal(err)
		}
		if len(line) == 0 {
			t.Fatalf("%s has no transition frame", tc.profile)
		}
		frame, err := events.ParseFrameLine(line[:len(line)-1])
		if err != nil {
			t.Fatal(err)
		}
		if frame.SessionID != tc.session {
			t.Errorf("%s contains session %q, want %q", tc.profile, frame.SessionID, tc.session)
		}
		_ = b.Close()
	}
}
