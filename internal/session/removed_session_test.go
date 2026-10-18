package session

import (
	"reflect"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

type closedFrame struct{ Profile, ID, Reason string }

// recordSessionClosed captures macapp.session.closed publishes for the test.
func recordSessionClosed(t *testing.T) func() []closedFrame {
	t.Helper()
	var mu sync.Mutex
	var got []closedFrame
	restore := SetSessionClosedPublisherForTest(func(profile, kind, id string, data any) {
		if kind != events.KindMacappSessionClosed {
			t.Errorf("kind %q", kind)
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, closedFrame{profile, id, data.(map[string]string)["reason"]})
	})
	t.Cleanup(restore)
	return func() []closedFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]closedFrame(nil), got...)
	}
}

// Every delete path calls CleanupRemovedSession: it sweeps the inboxes and
// the notify-state record and announces the removal to a following Mac app.
func TestCleanupRemovedSessionSweepsAndPublishes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	frames := recordSessionClosed(t)
	r := CleanupRemovedSession("work", "gone-1")
	if r.InboxErr != nil || r.NotifyStateErr != nil {
		t.Fatalf("cleanup errors: %+v", r)
	}
	CleanupRemovedSession("", "gone-2")
	want := []closedFrame{{"work", "gone-1", "removed"}, {DefaultProfile, "gone-2", "removed"}}
	if got := frames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("closed frames %+v, want %+v", got, want)
	}
}

// A session moved to another profile is gone from the source profile's bus.
func TestMigratePublishesMovedSessionClosed(t *testing.T) {
	src, _ := migrateTestSetup(t, "src", "dst")
	seedSession(t, src.GetDB(), makeRow("sess-moved", "Moved", DefaultGroupPath))
	frames := recordSessionClosed(t)
	if _, err := MigrateSessionsToProfile("src", "dst", []string{"sess-moved"}, ProfileMigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []closedFrame{{"src", "sess-moved", "moved"}}
	if got := frames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("closed frames %+v, want %+v", got, want)
	}
}
