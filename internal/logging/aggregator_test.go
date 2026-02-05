package logging

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAggregatorRecord(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agg.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	logger := slog.New(slog.NewJSONHandler(f, nil))
	agg := NewAggregator(logger, 1) // 1 second interval for fast test
	agg.Start()

	// Record events
	agg.Record(CompPool, "client_connect", slog.String("mcp", "exa"))
	agg.Record(CompPool, "client_connect", slog.String("mcp", "exa"))
	agg.Record(CompPool, "client_connect", slog.String("mcp", "exa"))
	agg.Record(CompPool, "client_disconnect")

	// Wait for flush
	time.Sleep(1500 * time.Millisecond)
	agg.Stop()
	_ = f.Sync()

	// Read and parse output
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("aggregator produced no output")
	}

	// Parse lines
	var records []map[string]any
	start := 0
	for i, b := range data {
		if b == '\n' {
			var r map[string]any
			if err := json.Unmarshal(data[start:i], &r); err == nil {
				records = append(records, r)
			}
			start = i + 1
		}
	}

	if len(records) < 2 {
		t.Fatalf("expected at least 2 summary records, got %d", len(records))
	}

	// Find the client_connect summary
	found := false
	for _, r := range records {
		if r["event"] == "client_connect" && r["msg"] == "event_summary" {
			count, ok := r["count"].(float64) // JSON numbers are float64
			if !ok || count != 3 {
				t.Errorf("expected count=3, got %v", r["count"])
			}
			found = true
		}
	}
	if !found {
		t.Error("client_connect summary not found in output")
	}
}

func TestAggregatorNilLogger(t *testing.T) {
	agg := NewAggregator(nil, 1)
	agg.Start()

	// Should not panic
	agg.Record(CompPool, "test_event")

	time.Sleep(1200 * time.Millisecond)
	agg.Stop()
}

func TestAggregatorStopFlushes(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agg.log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	logger := slog.New(slog.NewJSONHandler(f, nil))
	agg := NewAggregator(logger, 60) // Long interval, won't auto-flush
	agg.Start()

	agg.Record(CompStatus, "state_change")

	// Stop should trigger final flush
	agg.Stop()
	_ = f.Sync()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("expected final flush on Stop, got empty output")
	}
}
