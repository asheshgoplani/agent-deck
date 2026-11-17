package todos

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func TestPublishWritesTodoChangedOnTheProfileBus(t *testing.T) {
	const profile = "todo-notify-test"
	bus := events.OpenProfile(profile)
	defer bus.Close()
	if !bus.Stats().Enabled {
		t.Skip("events bus disabled in this environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := bus.Subscribe(ctx, bus.Cursor())
	if err != nil {
		t.Fatal(err)
	}
	c := Change{Action: ActionAdd, Todo: Item{ID: "4", Text: "call the bank", Source: "voice", SessionID: "sess-1"}, Open: 2, By: "voice"}
	got := Publish(c, profile)
	found := false
	for _, p := range got {
		found = found || p == profile
	}
	if !found {
		t.Fatalf("Publish returned %v, want it to include %q", got, profile)
	}
	for f := range sub.Frames() {
		if f.Kind != KindChanged {
			continue
		}
		if f.SessionID != "sess-1" {
			t.Fatalf("frame session_id %q", f.SessionID)
		}
		var data Change
		if err := json.Unmarshal(f.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Action != "add" || data.Todo.ID != "4" || data.Open != 2 || data.By != "voice" {
			t.Fatalf("frame data %s", f.Data)
		}
		return
	}
	t.Fatal("no todo.changed frame before the timeout")
}
