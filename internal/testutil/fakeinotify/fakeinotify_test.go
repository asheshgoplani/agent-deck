package fakeinotify_test

import (
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/testutil/fakeinotify"
)

func TestPublish_DeliversEvent(t *testing.T) {
	f := fakeinotify.New(t)
	defer f.Close()

	f.Publish("/tmp/h/abc.json", fsnotify.Create)

	select {
	case ev := <-f.Events():
		if ev.Name != "/tmp/h/abc.json" {
			t.Fatalf("Name=%q", ev.Name)
		}
		if ev.Op&fsnotify.Create == 0 {
			t.Fatalf("Op=%v missing Create", ev.Op)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
}

func TestDropAfter_SilentlyDropsOverflow(t *testing.T) {
	f := fakeinotify.New(t)
	defer f.Close()

	// Allow only 3 events; subsequent must be dropped.
	f.DropAfter(3)
	for range 5 {
		f.Publish("x.json", fsnotify.Write)
	}

	got := drain(f.Events(), 50*time.Millisecond)
	if len(got) != 3 {
		t.Fatalf("got %d events; want 3 (overflow dropped)", len(got))
	}
	if dropped := f.Dropped(); dropped != 2 {
		t.Fatalf("Dropped()=%d want 2", dropped)
	}
}

func TestPublishError_DeliversToErrors(t *testing.T) {
	f := fakeinotify.New(t)
	defer f.Close()

	f.PublishError(fsnotify.ErrEventOverflow)

	select {
	case err := <-f.Errors():
		if err == nil {
			t.Fatal("nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("error not delivered")
	}
}

// TestSimulateOverflow models the J2 regression scenario: kernel inotify
// queue overflows, fsnotify emits ErrEventOverflow, and N events are lost.
// The harness exposes both signals so tests can assert the watcher
// activates its disk-poll fallback within the timeout.
func TestSimulateOverflow(t *testing.T) {
	f := fakeinotify.New(t)
	defer f.Close()

	f.SimulateOverflow(10)

	// Should see the overflow error first.
	select {
	case err := <-f.Errors():
		if err != fsnotify.ErrEventOverflow {
			t.Fatalf("err=%v want ErrEventOverflow", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no overflow error")
	}
	if dropped := f.Dropped(); dropped != 10 {
		t.Fatalf("Dropped()=%d want 10", dropped)
	}
}

func TestClose_DrainsAndShutsDown(t *testing.T) {
	f := fakeinotify.New(t)
	f.Publish("a.json", fsnotify.Create)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After close, Events channel is closed.
	select {
	case _, ok := <-f.Events():
		// ok=true with the buffered event is fine; the next recv must be ok=false.
		_ = ok
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Events did not drain after Close")
	}
	// Drain the close signal.
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case _, ok := <-f.Events():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("Events channel never closed")
		}
	}
}

func drain(ch <-chan fsnotify.Event, wait time.Duration) []fsnotify.Event {
	var out []fsnotify.Event
	deadline := time.After(wait)
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}
