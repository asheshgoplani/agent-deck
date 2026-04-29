package tmux

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// TestTmuxExec_SetsWaitDelay locks in the bridged-stdio hang fix. Without
// Cmd.WaitDelay, agent-deck CLI subcommands (`status`, `list`, `session
// send`, ...) hang forever under Claude Code /remote-control because the
// tmux server inherits the client's stdout pipe fd for terminal
// pass-through and never closes it — leaving the I/O goroutine inside
// cmd.Output() blocked on EOF even after the tmux client process exits and
// the parent context is canceled.
//
// Cmd.WaitDelay (Go 1.20+) is the sanctioned escape hatch: after the
// deadline, Wait abandons the I/O goroutines and returns. The captured
// stdout buffer still holds whatever the subprocess wrote before the hang,
// so callers that tolerate exec.ErrWaitDelay can recover the data.
//
// Pair with TestExecWaitDelay_AbandonsLingeringChildPipe (runtime contract
// test) to catch both wrapper regressions AND Go-runtime regressions.
func TestTmuxExec_SetsWaitDelay(t *testing.T) {
	cmd := tmuxExec("", "list-sessions")
	if cmd.WaitDelay <= 0 {
		t.Fatalf("tmuxExec must set Cmd.WaitDelay > 0 to backstop the "+
			"bridged-stdio EOF hang; got %v", cmd.WaitDelay)
	}
}

// TestTmuxExecContext_SetsWaitDelay mirrors TestTmuxExec_SetsWaitDelay for
// the context-aware variant. Both wrappers must carry WaitDelay because
// neither cmd.Output() (used by RefreshSessionCache, RefreshPaneInfoCache,
// IsServerAlive) nor cmd.Run() (used by send-keys, set-option, etc.) is
// safe against the EOF hang on its own.
func TestTmuxExecContext_SetsWaitDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := tmuxExecContext(ctx, "", "list-sessions")
	if cmd.WaitDelay <= 0 {
		t.Fatalf("tmuxExecContext must set Cmd.WaitDelay > 0 to backstop "+
			"the bridged-stdio EOF hang; got %v", cmd.WaitDelay)
	}
}

// TestExecWaitDelay_AbandonsLingeringChildPipe is the runtime contract
// test: it verifies Go's Cmd.WaitDelay actually does what the wrapper fix
// relies on. Without this, a future Go release that quietly regressed
// WaitDelay semantics would let the structural tests above stay green
// while the real-world hang came back.
//
// The script forks a sleeping child that inherits stdout, then the parent
// shell exits immediately after writing "READY". The kernel keeps the
// stdout pipe alive as long as the sleeping child holds the fd, so an
// unguarded cmd.Output() blocks for the full sleep duration waiting for
// EOF. With WaitDelay set, cmd.Output() must return within
// (parent-exit + WaitDelay + grace) ≪ sleep duration.
func TestExecWaitDelay_AbandonsLingeringChildPipe(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a 30s sleeping subprocess; skipped in -short mode")
	}

	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & echo READY")
	cmd.WaitDelay = 1500 * time.Millisecond

	start := time.Now()
	out, err := cmd.Output()
	elapsed := time.Since(start)

	// Hard ceiling: 5s is well above WaitDelay+grace (~1.5s) but far
	// below the 30s sleep. Anything above 5s means WaitDelay didn't fire
	// — which is the bug coming back via a Go runtime change.
	if elapsed > 5*time.Second {
		t.Fatalf("WaitDelay=1.5s did not backstop the lingering-child EOF "+
			"hang: cmd.Output() blocked for %v (Go runtime regression?)", elapsed)
	}

	// When WaitDelay fires with non-empty stdout, exec.Cmd returns
	// errors.Is(err, exec.ErrWaitDelay). Callers that want to keep the
	// captured bytes must check for this sentinel instead of treating
	// it as a hard failure.
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected exec.ErrWaitDelay (I/O goroutines abandoned "+
			"due to lingering child fd); got %v", err)
	}

	// The captured stdout must contain the parent's pre-hang write. The
	// I/O goroutine got the bytes into the buffer before WaitDelay
	// abandoned it, so the data is preserved.
	if string(out) != "READY\n" {
		t.Fatalf("expected captured stdout to contain pre-hang output; "+
			"got %q", string(out))
	}
}
