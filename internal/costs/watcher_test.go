package costs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
)

func TestCostEventWatcher(t *testing.T) {
	dir := t.TempDir()

	w, err := costs.NewCostEventWatcher(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	go w.Start()

	// Small delay for watcher to start
	time.Sleep(50 * time.Millisecond)

	cf := costs.RawCostEvent{
		InstanceID:   "inst-1",
		Model:        "claude-sonnet-4-6",
		InputTokens:  1000,
		OutputTokens: 500,
		Timestamp:    time.Now().UnixNano(),
	}
	data, _ := json.Marshal(cf)
	tmpPath := filepath.Join(dir, "inst-1_123.json.tmp")
	finalPath := filepath.Join(dir, "inst-1_123.json")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.EventCh():
		if ev.InstanceID != "inst-1" {
			t.Errorf("instance = %q, want inst-1", ev.InstanceID)
		}
		if ev.InputTokens != 1000 {
			t.Errorf("input = %d, want 1000", ev.InputTokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for cost event")
	}
}
