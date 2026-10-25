// Package optiming records how long user-facing operations take, locally and
// only when asked to (ASK-106).
//
// Off by default. AGENTDECK_TIMINGS=1 (or `[logs] timings = true` in
// config.toml) turns it on for every agent-deck process: the CLI, the TUI,
// the web server and the notify daemon. Each finished operation then appends
// one JSON line to <cache>/timings.jsonl (next to debug.log) and, in
// processes that opened the debug log, a matching `op_timing` debug line.
// `agent-deck perf` summarises the file per operation against its target.
//
// Nothing here is sent anywhere. Lines carry the operation name, its
// duration, whether it failed, the pid and the time: no titles, paths,
// prompts or output.
//
// When off, Start is one atomic load returning a shared no-op closure
// (BenchmarkOp_TimingHookOff proves zero allocations), so hooks are safe on hot paths.
package optiming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// FileName is the timings file inside the agent-deck cache directory.
const FileName = "timings.jsonl"

// maxFileBytes rotates timings.jsonl to timings.jsonl.1 once it grows past
// this size, so an always-on daemon cannot fill the disk.
const maxFileBytes = 8 << 20

// Entry is one finished operation, as stored in timings.jsonl.
type Entry struct {
	Time time.Time `json:"t"`
	Op   string    `json:"op"`
	MS   float64   `json:"ms"`
	Fail bool      `json:"fail,omitempty"`
	PID  int       `json:"pid"`
}

var (
	enabled atomic.Bool

	mu      sync.Mutex
	dirFunc = agentpaths.CacheDir // replaced by tests
	file    *os.File

	perfLog = logging.ForComponent(logging.CompPerf)
)

func init() {
	enabled.Store(envOn(os.Getenv("AGENTDECK_TIMINGS")))
}

func envOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Enabled reports whether operation timing is on in this process.
func Enabled() bool { return enabled.Load() }

// Enable turns timing on (the `[logs] timings` config switch). It never turns
// it off: the environment variable alone can enable it.
func Enable() { enabled.Store(true) }

var noop = func() {}

// Start begins timing op and returns the function that records it. Use a
// fixed, content-free name such as "tui.attach".
//
//	defer optiming.Start("update.check")()
func Start(op string) func() {
	if !enabled.Load() {
		return noop
	}
	start := time.Now()
	return func() { record(op, time.Since(start), false) }
}

// StartResult is Start for operations that can fail: the returned function
// takes whether the operation failed.
func StartResult(op string) func(failed bool) {
	if !enabled.Load() {
		return noopResult
	}
	start := time.Now()
	return func(failed bool) { record(op, time.Since(start), failed) }
}

var noopResult = func(bool) {}

// Since records op as having started at start. For spans whose start and end
// live in different places (a stamp taken in one message, read in another).
func Since(op string, start time.Time) {
	if !enabled.Load() || start.IsZero() {
		return
	}
	record(op, time.Since(start), false)
}

func record(op string, d time.Duration, failed bool) {
	e := Entry{Time: time.Now().UTC(), Op: op, MS: float64(d.Microseconds()) / 1000, Fail: failed, PID: os.Getpid()}
	perfLog.Debug("op_timing", "op", op, "elapsed", d, "fail", failed)
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')

	mu.Lock()
	defer mu.Unlock()
	if file != nil && needsReopen() {
		_ = file.Close()
		file = nil
	}
	if file == nil && !openLocked() {
		return
	}
	// One write per line with O_APPEND: lines from concurrent processes never
	// interleave (each is far below PIPE_BUF).
	if _, err := file.Write(line); err != nil {
		_ = file.Close()
		file = nil
	}
}

// needsReopen reports whether the open file must be reopened before the next
// write: another process rotated it away (follow it to the new file instead
// of writing into the rotated copy), or it is full, counting every process's
// lines (openLocked then rotates it). Two stats per record, paid only while
// timings are on.
func needsReopen() bool {
	cur, err := os.Stat(file.Name())
	if err != nil {
		return true
	}
	open, err := file.Stat()
	return err != nil || !os.SameFile(cur, open) || open.Size() > maxFileBytes
}

func openLocked() bool {
	dir, err := dirFunc()
	if err != nil {
		return false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	path := filepath.Join(dir, FileName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxFileBytes {
		// No cross-process lock: two processes that see the full file at the
		// same moment can both rename, and the second replaces the first's
		// .1, losing up to one file of old timings. Acceptable for a local
		// diagnostic log; nothing else depends on it.
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	file = f
	return true
}

// Path returns the timings file of this user.
func Path() (string, error) {
	mu.Lock()
	df := dirFunc
	mu.Unlock()
	dir, err := df()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Close flushes nothing (writes are unbuffered) and releases the file.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
}

// SetForTest points the timings file at dir and switches timing on or off,
// returning the function that restores the previous state. Tests only: it
// lets other packages' tests assert that their hooks land in timings.jsonl.
func SetForTest(dir string, on bool) (restore func()) {
	mu.Lock()
	prevDir, prevOn := dirFunc, enabled.Load()
	mu.Unlock()
	Close()
	mu.Lock()
	dirFunc = func() (string, error) { return dir, nil }
	mu.Unlock()
	enabled.Store(on)
	return func() {
		Close()
		mu.Lock()
		dirFunc = prevDir
		mu.Unlock()
		enabled.Store(prevOn)
	}
}
