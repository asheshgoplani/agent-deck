package session

import (
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
	for _, profile := range []string{"alpha", "beta"} {
		b := events.OpenProfile(profile)
		if got := b.Stats().Cursor; got != 1 {
			t.Errorf("%s cursor = %d, want one transition and no status tap", profile, got)
		}
		_ = b.Close()
	}
}
