package costs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCostEventWatcher_ProcessFileAfterStopDoesNotPanic simulates the
// 100ms debounce timer firing after Start has returned and closed eventCh.
// processFile must not send on the closed channel (which panics the whole
// process) and must leave the event file on disk for the next run.
func TestCostEventWatcher_ProcessFileAfterStopDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	w, err := NewCostEventWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { w.Start(); close(done) }()
	w.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	for range w.EventCh() {
	}

	p := filepath.Join(dir, "ev.json")
	b, _ := json.Marshal(RawCostEvent{InstanceID: "i", Model: "m", Timestamp: 1})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("processFile after Stop panicked: %v", r)
			}
		}()
		w.processFile(p)
	}()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("event file should remain on disk for the next run: %v", err)
	}
}
