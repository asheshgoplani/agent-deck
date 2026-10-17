package tmux

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var subprocessStarts atomic.Int64

// SubprocessStarts counts native tmux commands started through this package.
// Output calls are counted on return; Run and asynchronous calls at Start.
// Deltas across a status pass include concurrent work in the same process
// unless that work ran inside RunUncounted.
func SubprocessStarts() int64 { return subprocessStarts.Load() }

// startSnapshot is a point-in-time pair of total starts and starts that must
// not be charged to the per-session status budget.
type startSnapshot struct {
	total, uncounted int64
}

var (
	budgetMu        sync.Mutex
	budgetUncounted int64
	uncountedDepth  sync.Map // goroutine id -> *atomic.Int32
)

// SnapshotStarts captures the counters a status pass subtracts at the end.
// Both fields move together under budgetMu, so a concurrent RunUncounted
// cannot be half-counted.
func SnapshotStarts() startSnapshot {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	return startSnapshot{total: subprocessStarts.Load(), uncounted: budgetUncounted}
}

// Charged returns tmux starts since s that count toward the per-session
// status budget. Starts inside RunUncounted are omitted. A negative result
// (a counter reset, or a sample that raced a test) is reported as zero.
func (s startSnapshot) Charged() int64 {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	calls := (subprocessStarts.Load() - s.total) - (budgetUncounted - s.uncounted)
	if calls < 0 {
		return 0
	}
	return calls
}

// RunUncounted runs fn without charging the tmux commands it starts to the
// per-session status budget. The commands are still counted by
// SubprocessStarts. Nested calls stay uncounted. The exclusion is
// goroutine-local: a status pass on another goroutine still charges its own
// commands, and only subtracts the uncounted delta that landed in its window.
//
// Use it for work that scales with viewers or with a detach, not with the
// session count: refreshing the status bar once per attached client (a web
// terminal is one more client), and the cache refresh that reconciles the
// session Ctrl+Q just left. Charging those to "calls per session" is what
// flashed the footer warning on the way back to the menu.
func RunUncounted(fn func()) {
	if fn == nil {
		return
	}
	id := currentGoroutineID()
	depth := uncountedCounter(id)
	depth.Add(1)
	defer func() {
		if depth.Add(-1) == 0 {
			uncountedDepth.Delete(id)
		}
	}()
	fn()
}

func uncountedCounter(id int64) *atomic.Int32 {
	v, _ := uncountedDepth.LoadOrStore(id, &atomic.Int32{})
	return v.(*atomic.Int32)
}

func startIsUncounted() bool {
	v, ok := uncountedDepth.Load(currentGoroutineID())
	return ok && v.(*atomic.Int32).Load() > 0
}

func currentGoroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	line = strings.TrimPrefix(line, "goroutine ")
	idText, _, _ := strings.Cut(line, " ")
	id, _ := strconv.ParseInt(idText, 10, 64)
	return id
}

func noteStart(uncounted bool) {
	budgetMu.Lock()
	subprocessStarts.Add(1)
	if uncounted {
		budgetUncounted++
	}
	budgetMu.Unlock()
}

func observeCommand(cmd *exec.Cmd) {
	if cmd.Process != nil && filepath.Base(cmd.Path) == "tmux" {
		noteStart(startIsUncounted())
	}
}

func commandRun(cmd *exec.Cmd) error {
	if err := commandStart(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}
func commandStart(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err == nil {
		observeCommand(cmd)
	}
	return err
}
func commandOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Process == nil {
		defer observeCommand(cmd)
	}
	return cmd.Output()
}
func commandCombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Process == nil {
		defer observeCommand(cmd)
	}
	return cmd.CombinedOutput()
}
