package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// readJSONLines parses debug.log into records. Missing file → empty slice.
func readJSONLines(t *testing.T, dir string) []map[string]any {
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

func findRec(records []map[string]any, msg string) map[string]any {
	for _, r := range records {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

func filterRec(records []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, r := range records {
		if r["msg"] == msg {
			out = append(out, r)
		}
	}
	return out
}

// initLogging is a test helper that resets logging into a fresh tmpdir
// and returns the dir path so tests can read debug.log directly.
func initLogging(t *testing.T, level string) string {
	t.Helper()
	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: level})
	t.Cleanup(func() { logging.Shutdown() })
	return dir
}

// TestTransitionTracker_StatusChangedEnriched closes logging-review G4/G5/G6.
// status_changed must be INFO and carry instance_id + tool + prev_for_ms.
func TestTransitionTracker_StatusChangedEnriched(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	now := time.Now()

	// First transition for an instance has prev_for_ms=0 (no prior).
	tr.recordAt("inst-A", "tui-a", "claude", "idle", "running", now)

	// Second transition 75ms later. prev_for_ms should reflect that gap.
	tr.recordAt("inst-A", "tui-a", "claude", "running", "waiting", now.Add(75*time.Millisecond))

	records := readJSONLines(t, dir)
	transitions := filterRec(records, "status_changed")
	if len(transitions) != 2 {
		t.Fatalf("want 2 status_changed records, got %d (records=%v)", len(transitions), records)
	}
	for _, r := range transitions {
		if r["level"] != "INFO" {
			t.Errorf("level = %v, want INFO", r["level"])
		}
		if r["instance_id"] != "inst-A" {
			t.Errorf("instance_id = %v, want inst-A", r["instance_id"])
		}
		if r["tool"] != "claude" {
			t.Errorf("tool = %v, want claude", r["tool"])
		}
		if r["title"] != "tui-a" {
			t.Errorf("title = %v, want tui-a", r["title"])
		}
	}

	// First record: no prior, prev_for_ms=0.
	if got := transitions[0]["prev_for_ms"]; got == nil {
		t.Error("first record: prev_for_ms missing")
	} else if got.(float64) != 0 {
		t.Errorf("first record prev_for_ms = %v, want 0", got)
	}
	// Second record: ~75ms in prior state.
	if got := transitions[1]["prev_for_ms"]; got == nil {
		t.Error("second record: prev_for_ms missing")
	} else if v := got.(float64); v < 70 || v > 80 {
		t.Errorf("second record prev_for_ms = %v, want ~75 (±5)", v)
	}
}

// TestTransitionTracker_FlickerDetected closes logging-review G6.
// 4 transitions in 60s for the same instance → one flicker_detected WARN.
func TestTransitionTracker_FlickerDetected(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	base := time.Now()
	// 4 oscillations in 4 seconds for inst-B
	tr.recordAt("inst-B", "flickerer", "claude", "running", "waiting", base.Add(0))
	tr.recordAt("inst-B", "flickerer", "claude", "waiting", "running", base.Add(1*time.Second))
	tr.recordAt("inst-B", "flickerer", "claude", "running", "waiting", base.Add(2*time.Second))
	tr.recordAt("inst-B", "flickerer", "claude", "waiting", "running", base.Add(3*time.Second))

	records := readJSONLines(t, dir)
	flickers := filterRec(records, "flicker_detected")
	if len(flickers) == 0 {
		t.Fatalf("want at least 1 flicker_detected WARN; got 0 (records=%v)", records)
	}
	w := flickers[0]
	if w["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", w["level"])
	}
	if w["instance_id"] != "inst-B" {
		t.Errorf("instance_id = %v, want inst-B", w["instance_id"])
	}
	if c, ok := w["count"].(float64); !ok || c < 3 {
		t.Errorf("count = %v, want >=3", w["count"])
	}
}

// TestTransitionTracker_NoFlickerWhenSparse — 4 transitions but spread over
// 5 minutes must NOT emit flicker_detected. Guards against false positives.
func TestTransitionTracker_NoFlickerWhenSparse(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	base := time.Now()
	// 4 transitions, 90 seconds apart — no flicker
	for i := 0; i < 4; i++ {
		tr.recordAt("inst-C", "calm", "shell",
			"running", "waiting",
			base.Add(time.Duration(i)*90*time.Second))
	}

	records := readJSONLines(t, dir)
	if flickers := filterRec(records, "flicker_detected"); len(flickers) > 0 {
		t.Errorf("want 0 flicker_detected for sparse transitions; got %d", len(flickers))
	}
}

// TestTransitionTracker_FlickerSuppressedAfterFire — once a flicker WARN
// fires, don't spam it on every subsequent transition. Re-arm after a
// quiet period so the next storm is logged.
func TestTransitionTracker_FlickerSuppressedAfterFire(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	base := time.Now()
	// First storm: 4 transitions in 4s → one WARN.
	for i := 0; i < 4; i++ {
		tr.recordAt("inst-D", "stormy", "claude",
			"running", "waiting", base.Add(time.Duration(i)*time.Second))
	}
	// Two more transitions immediately after (1s apart) — should NOT
	// emit a 2nd flicker; we already warned.
	tr.recordAt("inst-D", "stormy", "claude", "waiting", "running", base.Add(5*time.Second))
	tr.recordAt("inst-D", "stormy", "claude", "running", "waiting", base.Add(6*time.Second))

	records := readJSONLines(t, dir)
	flickers := filterRec(records, "flicker_detected")
	if len(flickers) != 1 {
		t.Fatalf("want exactly 1 flicker_detected during continuous storm; got %d", len(flickers))
	}
}

// TestStatusCascadeLogged closes logging-review G7 / recommendation #10.
// When ≥10 transitions happen in a single tick, emit one INFO summary.
func TestStatusCascadeLogged(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	tickStart := time.Now()
	for i := 0; i < 12; i++ {
		tr.recordAt(
			"inst-cascade-"+string(rune('a'+i)),
			"name",
			"claude",
			"error", "waiting",
			tickStart.Add(time.Duration(i)*time.Millisecond),
		)
	}
	tr.tickEnd(tickStart, time.Now())

	records := readJSONLines(t, dir)
	rec := findRec(records, "session_status_cascade")
	if rec == nil {
		t.Fatalf("want session_status_cascade INFO; got records=%v",
			func() []string {
				out := make([]string, 0, len(records))
				for _, r := range records {
					out = append(out, r["msg"].(string))
				}
				return out
			}(),
		)
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if c, ok := rec["count"].(float64); !ok || c < 10 {
		t.Errorf("count = %v, want >=10", rec["count"])
	}
	// All same kind → kind="error->waiting", same_kind=true.
	if same, ok := rec["same_kind"].(bool); !ok || !same {
		t.Errorf("same_kind = %v, want true", rec["same_kind"])
	}
	if rec["kind"] != "error->waiting" {
		t.Errorf("kind = %v, want error->waiting", rec["kind"])
	}
}

// TestStatusCascadeNotLoggedForFew — <10 transitions in a tick: no cascade.
func TestStatusCascadeNotLoggedForFew(t *testing.T) {
	dir := initLogging(t, "info")

	tr := newTransitionTracker()
	tickStart := time.Now()
	for i := 0; i < 5; i++ {
		tr.recordAt(
			"inst-x-"+string(rune('a'+i)),
			"name",
			"claude",
			"running", "waiting",
			tickStart,
		)
	}
	tr.tickEnd(tickStart, time.Now())

	records := readJSONLines(t, dir)
	if rec := findRec(records, "session_status_cascade"); rec != nil {
		t.Errorf("did not expect session_status_cascade for <10 transitions; got %v", rec)
	}
}
