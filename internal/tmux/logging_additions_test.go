package tmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

func tmuxReadJSONLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "debug.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read debug.log: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func tmuxFindRec(records []map[string]any, msg string) map[string]any {
	for _, r := range records {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

func tmuxFilterRec(records []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

// TestPipeDegradedAggregated closes logging-review G14.
// Each pipe-degradation occurrence is recorded via the aggregator with
// component=status, event=pipe_degraded; flush emits one event_summary
// at INFO with the count instead of one DEBUG line per occurrence.
func TestPipeDegradedAggregated(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{
		Debug:                 true,
		LogDir:                dir,
		Level:                 "info",
		AggregateIntervalSecs: 1, // small for the test
	})
	defer logging.Shutdown()

	s := &Session{Name: "ut-pipe"}
	for i := 0; i < 5; i++ {
		s.recordPipeDegraded()
	}

	// Wait for at least one flush window.
	deadline := time.Now().Add(3 * time.Second)
	var summary map[string]any
	for time.Now().Before(deadline) {
		records := tmuxReadJSONLines(t, dir)
		for _, r := range records {
			if r["msg"] == "event_summary" && r["event"] == "pipe_degraded" {
				summary = r
				break
			}
		}
		if summary != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if summary == nil {
		t.Fatal("expected event_summary record with event=pipe_degraded after flush window")
	}
	if c, ok := summary["count"].(float64); !ok || c < 5 {
		t.Errorf("count = %v, want >=5", summary["count"])
	}
	if summary["component"] != logging.CompStatus {
		t.Errorf("component = %v, want %s", summary["component"], logging.CompStatus)
	}
}

// TestHashFallbackUsedLoggedOnce closes logging-review G8.
// Entering the hash-based fallback emits hash_fallback_used WARN exactly
// once per Session (sync.Once gated). The fallback path historically
// caused flickering — a landmark log is the diagnostic anchor.
func TestHashFallbackUsedLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	s := &Session{Name: "ut-hash"}
	for i := 0; i < 4; i++ {
		s.recordHashFallbackUsed()
	}

	records := tmuxReadJSONLines(t, dir)
	hits := tmuxFilterRec(records, "hash_fallback_used")
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 hash_fallback_used WARN; got %d (records=%v)", len(hits), records)
	}
	rec := hits[0]
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if rec["session"] != "ut-hash" {
		t.Errorf("session = %v, want ut-hash", rec["session"])
	}
}

// TestHashFallbackUsedPerSession — a separate Session has its own once,
// so the second session also emits one WARN.
func TestHashFallbackUsedPerSession(t *testing.T) {
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "info"})
	defer logging.Shutdown()

	s1 := &Session{Name: "alpha"}
	s2 := &Session{Name: "beta"}
	s1.recordHashFallbackUsed()
	s1.recordHashFallbackUsed()
	s2.recordHashFallbackUsed()

	records := tmuxReadJSONLines(t, dir)
	hits := tmuxFilterRec(records, "hash_fallback_used")
	if len(hits) != 2 {
		t.Fatalf("want 2 hash_fallback_used (one per session); got %d", len(hits))
	}
	names := []string{hits[0]["session"].(string), hits[1]["session"].(string)}
	hasAlpha, hasBeta := false, false
	for _, n := range names {
		if n == "alpha" {
			hasAlpha = true
		}
		if n == "beta" {
			hasBeta = true
		}
	}
	if !hasAlpha || !hasBeta {
		t.Errorf("expected both alpha and beta sessions logged; got %v", names)
	}

	// Sanity: aggregator findRec helper isn't pulling in the wrong record.
	if tmuxFindRec(records, "hash_fallback_used") == nil {
		t.Error("findRec returned nil despite hits")
	}
}
