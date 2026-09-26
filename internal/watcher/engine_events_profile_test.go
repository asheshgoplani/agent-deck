package watcher

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

func TestEngineWritesToSelectedProcessProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	events.SetProfile("work")
	t.Cleanup(func() { events.SetProfile("default") })

	engine, _ := newTestEngine(t, nil)
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	engine.bus.Publish("watcher.health", "s", nil)
	engine.Stop()

	work := events.OpenProfile("work")
	defer work.Close()
	if got := work.Cursor(); got != 1 {
		t.Fatalf("selected profile cursor = %d, want watcher frame 1", got)
	}
	other := events.OpenProfile("default")
	defer other.Close()
	if got := other.Cursor(); got != 0 {
		t.Fatalf("default profile cursor = %d, want no watcher frame", got)
	}
}
