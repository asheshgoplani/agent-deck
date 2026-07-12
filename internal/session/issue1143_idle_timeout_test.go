// Issue #1143: --idle-timeout flag auto-stops dormant child sessions.
//
// Self-improvement report 2026-05-21 found 4 workers (adeck-tdd-1031 x17,
// adeck-tdd-1020 x12, adeck-feat-1029 x10, adeck-tdd-973 x9) that completed
// work but never got cleaned up — they continued firing EVENT notifications
// and consuming registry tracking forever. A --idle-timeout 30m flag would
// auto-stop these after 30 min of no tmux pane output change.
//
// These tests cover the watcher tick logic, the lifecycle log entry, the
// "0 = disabled" boundary, and the mid-flight timeout change.
package session

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock returns a deterministic now() that advances via Advance.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeCapture returns whatever the test sets per instance ID.
type fakeCapture struct {
	mu       sync.Mutex
	contents map[string]string
}

func newFakeCapture() *fakeCapture { return &fakeCapture{contents: map[string]string{}} }

func (f *fakeCapture) Set(id, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contents[id] = content
}

func (f *fakeCapture) Capture(inst *Instance) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.contents[inst.ID], nil
}

// recordingStopper captures Stop() invocations.
type recordingStopper struct {
	mu      sync.Mutex
	stopped []string
}

func (s *recordingStopper) Stop(inst *Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, inst.ID)
	return nil
}

func (s *recordingStopper) StoppedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.stopped))
	copy(out, s.stopped)
	return out
}

func newRunningInstance(id, title string, idleTimeoutSecs int64) *Instance {
	inst := NewInstance(title, "/tmp")
	inst.ID = id
	inst.Status = StatusRunning
	inst.IdleTimeoutSecs = idleTimeoutSecs
	return inst
}

// Test 1: idle_timeout=10s, no output change for 11s → Stop called + lifecycle entry.
func TestIssue1143_IdleTimeoutWatcher_TriggersStopAfterInactivity(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}

	// Route lifecycle log to a temp dir so we can verify the entry.
	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-idle-1", "worker-1", 10)
	capture.Set(inst.ID, "static pane content")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now:     clock.Now,
		Capture: capture.Capture,
		Stop:    stopper.Stop,
	})

	// First tick at t=0: records last-seen, doesn't stop yet.
	w.Tick([]*Instance{inst})
	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("first tick should not stop session yet (no elapsed time): got %v", got)
	}

	// Advance 11s with same content → idle.
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst})

	stopped := stopper.StoppedIDs()
	if len(stopped) != 1 || stopped[0] != inst.ID {
		t.Fatalf("expected Stop called for %q after idle, got %v", inst.ID, stopped)
	}

	// Lifecycle log must contain idle-timeout-expired entry for this instance.
	logPath := GetSessionLifecycleLogPath()
	data, err := readFileQuiet(logPath)
	if err != nil {
		t.Fatalf("read lifecycle log %s: %v", logPath, err)
	}
	body := string(data)
	if !strings.Contains(body, inst.ID) {
		t.Fatalf("lifecycle log missing instance id %q: %s", inst.ID, body)
	}
	if !strings.Contains(body, "idle-timeout-expired") {
		t.Fatalf("lifecycle log missing reason idle-timeout-expired: %s", body)
	}
}

// Test 2: idle_timeout=10s, output changes every 5s → session stays running.
func TestIssue1143_IdleTimeoutWatcher_OutputChangeResetsTimer(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}

	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-active", "worker-active", 10)

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now:     clock.Now,
		Capture: capture.Capture,
		Stop:    stopper.Stop,
	})

	// Simulate 12 ticks at 5s apart, each with new output content.
	for i := 0; i < 12; i++ {
		capture.Set(inst.ID, "line-")
		capture.Set(inst.ID, "line-"+itoa(i))
		w.Tick([]*Instance{inst})
		clock.Advance(5 * time.Second)
	}

	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("output-active session must not be stopped, got Stop for %v", got)
	}
}

// Test 3: idle_timeout=0 → watcher never stops the session regardless of inactivity.
func TestIssue1143_IdleTimeoutWatcher_ZeroDisablesAutoStop(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}

	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-zero", "worker-zero", 0) // disabled
	capture.Set(inst.ID, "frozen")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now:     clock.Now,
		Capture: capture.Capture,
		Stop:    stopper.Stop,
	})

	for i := 0; i < 100; i++ {
		w.Tick([]*Instance{inst})
		clock.Advance(60 * time.Second)
	}

	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("idle_timeout=0 must disable auto-stop; got Stop for %v", got)
	}
}

// Test 4: changing IdleTimeoutSecs via SetField must apply on next watcher tick.
func TestIssue1143_IdleTimeoutWatcher_ChangingTimeoutAppliesNextTick(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}

	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-flex", "worker-flex", 0) // start disabled
	capture.Set(inst.ID, "no change")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now:     clock.Now,
		Capture: capture.Capture,
		Stop:    stopper.Stop,
	})

	// 10 minutes of no output, idle disabled → no stop.
	for i := 0; i < 10; i++ {
		w.Tick([]*Instance{inst})
		clock.Advance(60 * time.Second)
	}
	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("disabled timer must not stop session; got %v", got)
	}

	// Enable idle timeout via SetField (5s window).
	if _, _, err := SetField(inst, FieldIdleTimeout, "5s", nil); err != nil {
		t.Fatalf("SetField idle-timeout: %v", err)
	}
	if inst.IdleTimeoutSecs != 5 {
		t.Fatalf("expected IdleTimeoutSecs=5 after SetField, got %d", inst.IdleTimeoutSecs)
	}

	// First tick after enabling records baseline; second tick after 6s triggers stop.
	w.Tick([]*Instance{inst})
	clock.Advance(6 * time.Second)
	w.Tick([]*Instance{inst})

	stopped := stopper.StoppedIDs()
	if len(stopped) != 1 || stopped[0] != inst.ID {
		t.Fatalf("expected Stop after timer enabled + elapsed, got %v", stopped)
	}
}

// Test 5a (CLI): ParseIdleTimeout accepts Go-style durations.
func TestIssue1143_ParseIdleTimeout_Valid(t *testing.T) {
	cases := map[string]int64{
		"":     0,
		"0":    0,
		"30m":  int64((30 * time.Minute).Seconds()),
		"1h":   int64((1 * time.Hour).Seconds()),
		"24h":  int64((24 * time.Hour).Seconds()),
		"500s": 500,
	}
	for input, want := range cases {
		got, err := ParseIdleTimeoutFlag(input)
		if err != nil {
			t.Fatalf("ParseIdleTimeoutFlag(%q) error: %v", input, err)
		}
		if got != want {
			t.Errorf("ParseIdleTimeoutFlag(%q) = %d, want %d", input, got, want)
		}
	}
}

// Test 5b (CLI): ParseIdleTimeout rejects negative and malformed input.
func TestIssue1143_ParseIdleTimeout_RejectsNegative(t *testing.T) {
	bad := []string{"-1s", "-30m", "garbage", "30x"}
	for _, in := range bad {
		if _, err := ParseIdleTimeoutFlag(in); err == nil {
			t.Errorf("ParseIdleTimeoutFlag(%q) should error; got nil", in)
		}
	}
}

// Boundary: persistence round-trip preserves IdleTimeoutSecs.
func TestIssue1143_IdleTimeout_PersistenceRoundTrip(t *testing.T) {
	td := WriteIdleTimeoutSecsToToolData(nil, 1800)
	got := ReadIdleTimeoutSecsFromToolData(td)
	if got != 1800 {
		t.Fatalf("ReadIdleTimeoutSecsFromToolData after Write = %d, want 1800", got)
	}

	// Setting back to 0 removes the key (forward-compat with legacy rows).
	cleared := WriteIdleTimeoutSecsToToolData(td, 0)
	if got := ReadIdleTimeoutSecsFromToolData(cleared); got != 0 {
		t.Fatalf("Write(td, 0) should clear, got %d", got)
	}

	// Round-trip preserves unrelated fields.
	mixed := []byte(`{"color":"#ff00aa","claude_session_id":"abc"}`)
	out := WriteIdleTimeoutSecsToToolData(mixed, 600)
	if got := ReadIdleTimeoutSecsFromToolData(out); got != 600 {
		t.Fatalf("round-trip with extras lost idle_timeout_secs: got %d", got)
	}
	if !strings.Contains(string(out), `"color":"#ff00aa"`) {
		t.Fatalf("round-trip dropped color: %s", string(out))
	}
	if !strings.Contains(string(out), `"claude_session_id":"abc"`) {
		t.Fatalf("round-trip dropped claude_session_id: %s", string(out))
	}
}

// Boundary: SQLite round-trip preserves IdleTimeoutSecs across save/load.
// Verifies the storage path actually persists the field (not just the helpers).
func TestIssue1143_IdleTimeout_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("idle-roundtrip", "/tmp")
	inst.Tool = "shell"
	inst.IdleTimeoutSecs = 1800

	groupTree := NewGroupTreeWithGroups([]*Instance{inst}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	// Reload from the same SQLite DB through the public load path.
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(loaded))
	}
	if loaded[0].IdleTimeoutSecs != 1800 {
		t.Fatalf("IdleTimeoutSecs not preserved across SQLite round-trip: got %d, want 1800", loaded[0].IdleTimeoutSecs)
	}
}

// Failure mode: ParseIdleTimeoutFlag must reject huge negative durations that
// could overflow int64 seconds when coerced. We pin a representative cluster.
func TestIssue1143_ParseIdleTimeout_RejectsBogusUnits(t *testing.T) {
	bad := []string{"5", "5 m", "five minutes"}
	for _, in := range bad {
		if _, err := ParseIdleTimeoutFlag(in); err == nil {
			t.Errorf("ParseIdleTimeoutFlag(%q) should reject (no unit / malformed); got nil", in)
		}
	}
}

// readFileQuiet returns file content; if the file doesn't exist returns empty
// instead of error (the watcher writes on demand).
func readFileQuiet(path string) ([]byte, error) {
	return readFileOrEmpty(path)
}

func itoa(i int) string {
	// Minimal int-to-string to avoid importing strconv in test fixtures
	// (keeps test imports tight and lint-clean).
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	idx := len(buf)
	for i > 0 {
		idx--
		buf[idx] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}

// keep filepath import alive for any future expansion.
var _ = filepath.Join

// Pinned sessions are immune to idle auto-stop (pin-protects-from-stop).
func TestIdleTimeoutWatcher_PinnedSessionNeverStopped(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}
	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-pinned", "worker-pinned", 10)
	inst.Pin = PinTop
	capture.Set(inst.ID, "static pane content")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: capture.Capture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst})
	clock.Advance(120 * time.Second)
	w.Tick([]*Instance{inst})

	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("pinned session must not be auto-stopped, got Stop for %v", got)
	}
	// No lifecycle event should have been logged for a skipped session.
	logPath := GetSessionLifecycleLogPath()
	if data, err := readFileQuiet(logPath); err == nil && strings.Contains(string(data), inst.ID) {
		t.Fatalf("pinned skip must not emit a lifecycle event, log had: %s", string(data))
	}
}

// Unpinning re-arms idle tracking: a session pinned then unpinned auto-stops.
func TestIdleTimeoutWatcher_UnpinReArmsAutoStop(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}
	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-rearm", "worker-rearm", 10)
	inst.Pin = PinTop
	capture.Set(inst.ID, "static")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: capture.Capture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst}) // pinned: skipped
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst}) // still pinned: skipped
	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("expected no stop while pinned, got %v", got)
	}

	inst.Pin = PinNone
	w.Tick([]*Instance{inst}) // re-arms: records last-seen
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst}) // idle elapsed: stop
	if got := stopper.StoppedIDs(); len(got) != 1 || got[0] != inst.ID {
		t.Fatalf("expected stop after unpin+idle, got %v", got)
	}
}

// An UNARMED session (IdleTimeoutSecs == 0) is skipped before any tmux Capture,
// archived or not. This is what actually bounds the watcher's cost across a
// large archive backlog: idle timeout is opt-in, so the archived thousands are
// unarmed and cost one field read each — no tmux subprocess.
func TestIdleTimeoutWatcher_SkipsUnarmedArchivedBeforeCapture(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	stopper := &recordingStopper{}

	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-archived-unarmed", "worker-archived", 0) // unarmed
	inst.ArchivedAt = clock.Now()

	failingCapture := func(*Instance) (string, error) {
		t.Fatalf("Capture called for unarmed session %q — should have been skipped", inst.ID)
		return "", nil
	}

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: failingCapture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst})
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst})

	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("unarmed session must never be idle-stopped, got %v", got)
	}
}

// Safety net: an ARMED archived session whose tmux Kill silently failed keeps a
// live pane and a frozen live-ish Status. The watcher is the only mechanism
// that will ever stop that orphan, so being archived must NOT exempt it.
// Regression test for the archived-skip that removed this net.
func TestIdleTimeoutWatcher_ArmedArchivedOrphanIsStopped(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}

	t.Setenv("HOME", t.TempDir())

	// Archived, but Kill failed: status frozen at running, idle timeout armed.
	inst := newRunningInstance("inst-archived-orphan", "worker-orphan", 10)
	inst.ArchivedAt = clock.Now()
	capture.Set(inst.ID, "orphaned agent, no output change")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: capture.Capture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst}) // baseline
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst}) // idle threshold exceeded → stop the orphan

	stopped := stopper.StoppedIDs()
	if len(stopped) != 1 || stopped[0] != inst.ID {
		t.Fatalf("armed archived orphan must be idle-stopped, got %v", stopped)
	}
}
