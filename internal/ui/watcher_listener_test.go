package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// TestListenForWatcherEvent_DeliversMsg verifies that listenForWatcherEvent's
// tea.Cmd returns a watcherEventMsg carrying the event it received on the
// channel. This guards against the listener-cmd-loss class of bug where a
// dispatch path silently stops firing.
func TestListenForWatcherEvent_DeliversMsg(t *testing.T) {
	ch := make(chan watcher.Event, 1)
	want := watcher.Event{
		Source:   "github",
		Sender:   "octocat@github.com",
		Subject:  "[PR opened] #1: hello",
		RoutedTo: "demo",
	}
	ch <- want

	cmd := listenForWatcherEvent(ch)
	if cmd == nil {
		t.Fatal("listenForWatcherEvent returned nil cmd")
	}

	done := make(chan struct{})
	var got any
	go func() {
		got = cmd()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listener cmd did not return within 2s")
	}

	msg, ok := got.(watcherEventMsg)
	if !ok {
		t.Fatalf("got msg type %T, want watcherEventMsg", got)
	}
	if msg.event.Sender != want.Sender || msg.event.RoutedTo != want.RoutedTo {
		t.Errorf("event mismatch: got %+v, want %+v", msg.event, want)
	}
}

// TestListenForWatcherEvent_ReturnsNilOnClosedChannel verifies that the
// listener exits cleanly when the engine closes the event channel (Stop).
func TestListenForWatcherEvent_ReturnsNilOnClosedChannel(t *testing.T) {
	ch := make(chan watcher.Event)
	close(ch)

	got := listenForWatcherEvent(ch)()
	if got != nil {
		t.Errorf("got %T (%v); want nil after closed channel", got, got)
	}
}
