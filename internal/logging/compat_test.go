package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBridgeWriterParsesCategory(t *testing.T) {
	Shutdown()

	dir := t.TempDir()
	Init(Config{
		Debug:  true,
		LogDir: dir,
	})
	defer Shutdown()

	bw := NewBridgeWriter("legacy")

	type testCase struct {
		input    string
		wantComp string
		wantMsg  string
	}

	tests := []testCase{
		{"[STATUS] state changed to running\n", CompStatus, "state changed to running"},
		{"[MCP-DEBUG] attaching server\n", CompMCP, "attaching server"},
		{"[POOL-SIMPLE] health check passed\n", CompPool, "health check passed"},
		{"[PERF] slow refresh 200ms\n", CompPerf, "slow refresh 200ms"},
		{"plain message without category\n", "legacy", "plain message without category"},
		{"[NOTIF-BG] updating bar\n", CompNotif, "updating bar"},
		{"[SESSION-DATA] loading instance\n", CompSession, "loading instance"},
	}

	// Write all inputs
	for _, tt := range tests {
		_, _ = bw.Write([]byte(tt.input))
	}

	// Read all records
	logPath := filepath.Join(dir, "debug.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

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

	if len(records) != len(tests) {
		t.Fatalf("expected %d records, got %d", len(tests), len(records))
	}

	for i, tt := range tests {
		r := records[i]
		if r["component"] != tt.wantComp {
			t.Errorf("input %q: expected component=%s, got %v", tt.input, tt.wantComp, r["component"])
		}
		if r["msg"] != tt.wantMsg {
			t.Errorf("input %q: expected msg=%q, got %v", tt.input, tt.wantMsg, r["msg"])
		}
	}
}

func TestBridgeWriterStripsTimestamp(t *testing.T) {
	Shutdown()

	dir := t.TempDir()
	Init(Config{
		Debug:  true,
		LogDir: dir,
	})
	defer Shutdown()

	bw := NewBridgeWriter("legacy")

	// Simulate log.Ltime|log.Lmicroseconds prefix
	_, _ = bw.Write([]byte("15:04:05.000000 [STATUS] test message\n"))

	logPath := filepath.Join(dir, "debug.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

	var record map[string]any
	for i, b := range data {
		if b == '\n' {
			if err := json.Unmarshal(data[:i], &record); err == nil {
				break
			}
		}
	}

	if record == nil {
		t.Fatal("no valid JSON record found")
	}

	if record["msg"] != "test message" {
		t.Errorf("expected msg='test message', got %v", record["msg"])
	}
	if record["component"] != CompStatus {
		t.Errorf("expected component=%s, got %v", CompStatus, record["component"])
	}
}

func TestBridgeWriterEmptyInput(t *testing.T) {
	Shutdown()

	dir := t.TempDir()
	Init(Config{
		Debug:  true,
		LogDir: dir,
	})
	defer Shutdown()

	bw := NewBridgeWriter("legacy")
	n, err := bw.Write([]byte("   \n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 4 {
		t.Errorf("expected n=4, got %d", n)
	}

	// Log file should be empty (whitespace-only input is skipped)
	logPath := filepath.Join(dir, "debug.log")
	data, _ := os.ReadFile(logPath)
	if len(data) > 0 {
		t.Errorf("expected empty log for whitespace input, got %q", string(data))
	}
}

func TestStripLogTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"15:04:05.000000 hello", "hello"},
		{"15:04:05 hello", "hello"},
		{"no timestamp here", "no timestamp here"},
		{"12:34:56.789012 [STATUS] msg", "[STATUS] msg"},
	}

	for _, tt := range tests {
		got := stripLogTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("stripLogTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCanonicalComponent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"status", CompStatus},
		{"status-debug", CompStatus},
		{"mcp", CompMCP},
		{"mcp-debug", CompMCP},
		{"pool", CompPool},
		{"socket-proxy", CompPool},
		{"perf", CompPerf},
		{"session-data", CompSession},
		{"restart-debug", CompSession},
		{"respawn", CompSession},
		{"opencode", CompSession},
		{"codex", CompSession},
		{"unknown-category", "unknown-category"},
	}

	for _, tt := range tests {
		got := canonicalComponent(tt.input)
		if got != tt.want {
			t.Errorf("canonicalComponent(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
