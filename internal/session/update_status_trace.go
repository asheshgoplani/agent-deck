package session

import (
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var statusPerfLog = logging.ForComponent(logging.CompPerf)

// updateStatusSlowThreshold matches the per-session threshold the TUI's
// background sweep uses to build its slow_sessions list, so a session named
// there always has a matching attribution line.
const updateStatusSlowThreshold = 50 * time.Millisecond

// updateStatusTrace attributes a single UpdateStatus call to its blocking
// phases. The background sweep routinely reported individual sessions at
// 500-650ms with no attribution at all: the one probe that could have answered
// it (logging.TraceOp around GetStatus) compiles to a no-op unless debug
// tracing is enabled, so those calls were silent even in a DEBUG log.
//
// The cost is four time.Now calls on a path that already spawns tmux
// subprocesses, and a log line only when the call was genuinely slow.
type updateStatusTrace struct {
	start time.Time

	exists     time.Duration // tmuxSession.Exists() — has-session probe
	terminated time.Duration // applyTerminatedPaneStatus + auth-hold death probe
	bgWork     time.Duration // BackgroundWorkPending — captures the pane
	getStatus  time.Duration // tmuxSession.GetStatus() — capture + classify
	gateway    time.Duration // Hermes gateway reachability check
	hookFile   time.Duration // readHookStatusFile — cold-load disk read
	persist    time.Duration // persistLastActivity — SQLite write
}

func newUpdateStatusTrace() *updateStatusTrace {
	return &updateStatusTrace{start: time.Now()}
}

// phase times one blocking call: `defer t.phase(&t.getStatus)()`.
func (t *updateStatusTrace) phase(into *time.Duration) func() {
	if t == nil {
		return func() {}
	}
	start := time.Now()
	return func() { *into += time.Since(start) }
}

// finish emits the attribution when the call exceeded the slow threshold.
// It reads only fields that are fixed for an instance's lifetime, so it is
// safe to run as a deferred call after i.mu has been released.
func (t *updateStatusTrace) finish(id, title string) {
	if t == nil {
		return
	}
	total := time.Since(t.start)
	if total < updateStatusSlowThreshold {
		return
	}
	statusPerfLog.Warn("slow_update_status",
		slog.String("id", id),
		slog.String("title", title),
		slog.Duration("total", total),
		slog.Duration("exists", t.exists),
		slog.Duration("terminated", t.terminated),
		slog.Duration("bg_work", t.bgWork),
		slog.Duration("get_status", t.getStatus),
		slog.Duration("gateway", t.gateway),
		slog.Duration("hook_file", t.hookFile),
		slog.Duration("persist", t.persist),
		slog.Duration("other", total-t.exists-t.terminated-t.bgWork-t.getStatus-t.gateway-t.hookFile-t.persist),
	)
}

// traceExists is tmuxSession.Exists() with its cost attributed. Exists is the
// one blocking probe UpdateStatus makes before it can decide anything, and on a
// cache miss it spawns `tmux has-session` against a server that serializes
// every client — so it is a first-class suspect whenever a sweep runs long.
func (i *Instance) traceExists(t *updateStatusTrace) bool {
	done := t.phase(&t.exists)
	defer done()
	return i.tmuxSession.Exists()
}
