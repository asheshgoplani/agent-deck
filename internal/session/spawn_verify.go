package session

import (
	"fmt"
	"strings"
	"time"
)

// SpawnFailedError is returned by VerifySpawned when the tmux session is not
// there after a Start()/Restart() that reported success. Record is the
// spawn-failure sidecar left by the fast-death watcher / tmux start path, or
// nil when the session vanished without anything being recorded.
type SpawnFailedError struct {
	TmuxName string
	Record   *SpawnFailureRecord
}

func (e *SpawnFailedError) Error() string {
	if e.Record == nil {
		return fmt.Sprintf("tmux session %q does not exist after spawn (the process exited before it could be observed)", e.TmuxName)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tmux session %q is gone: %s", e.TmuxName, e.Record.Reason)
	if e.Record.Reason == "spawn_died_fast" && e.Record.ElapsedMs > 0 {
		fmt.Fprintf(&b, " (exited after %dms)", e.Record.ElapsedMs)
	}
	if out := strings.TrimSpace(e.Record.DyingOutput); out != "" {
		fmt.Fprintf(&b, ": %s", out)
	}
	return b.String()
}

// spawnVerifyTick paces the read-back loop. It is finer than
// spawnFastDeathTick so a record written by the watcher is picked up within
// one watcher tick of the death rather than a full extra tick later.
const spawnVerifyTick = 100 * time.Millisecond

// VerifySpawned confirms that the tmux session actually exists after a
// Start()/Restart() returned nil (#2099). A live session returns nil at once.
// When the session is missing it keeps looking, for at most maxWait, for
// either the session to appear or a spawn-failure record to be written (the
// fast-death watcher records a death on its next 250ms tick), and then
// returns a *SpawnFailedError carrying whatever was recorded.
//
// This is the read-back the CLI was missing: Start() can return nil while
// the pane is already gone (the initial process died before the first
// watcher tick, or an in-flight sibling spawn short-circuited the call), and
// the success message was printed unconditionally.
//
// A tool documented to answer once and exit (expectsFastExit) is exempt, as
// it is from the fast-death watcher: a short life is its success.
func (i *Instance) VerifySpawned(maxWait time.Duration) error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if i.expectsFastExit() {
		return nil
	}
	deadline := time.Now().Add(maxWait)
	for {
		// Liveness wins: a stale sidecar from an earlier attempt must never
		// fail a session that is demonstrably up.
		if i.tmuxSession.Exists() {
			return nil
		}
		rec := i.SpawnFailure()
		if rec != nil || time.Now().After(deadline) {
			return &SpawnFailedError{TmuxName: i.tmuxSession.Name, Record: rec}
		}
		time.Sleep(spawnVerifyTick)
	}
}
