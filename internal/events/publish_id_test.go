package events

import (
	"context"
	"testing"
	"time"
)

func TestPublishWithIDMatchesCommittedFrame(t *testing.T) {
	bus, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	first := bus.PublishWithID("macapp.open", "s", map[string]string{"path": "/report.html"})
	second := bus.PublishWithID("macapp.open", "s", map[string]string{"path": "/report.html"})
	if first == "" || second == "" || first == second {
		t.Fatalf("ids: %q %q", first, second)
	}
	if !bus.Flush(2 * time.Second) {
		t.Fatal("flush failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first, second} {
		frame, ok := <-sub.Frames()
		if !ok || frame.EventID != want || frame.SessionID != "s" || frame.Kind != "macapp.open" {
			t.Fatalf("frame %+v, want id %s", frame, want)
		}
	}
	if got := bus.PublishWithID("bad", "s", make(chan int)); got != "" {
		t.Fatalf("unserializable payload admitted: %s", got)
	}
	_ = bus.Close()
	if got := bus.PublishWithID("closed", "s", nil); got != "" {
		t.Fatalf("closed bus admitted: %s", got)
	}
	var absent *Bus
	if got := absent.PublishWithID("nil", "s", nil); got != "" {
		t.Fatalf("nil bus admitted: %s", got)
	}
}
