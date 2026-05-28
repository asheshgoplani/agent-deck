package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestShouldConcludeSessionGone_RetriesEarlyMisses guards the reconnect cascade
// fix: when a pipe dies and the reconnect loop probes whether the session still
// exists, a single failed probe must NOT immediately conclude the session is
// gone. During tmux-server contention (e.g. another session being torn down) a
// probe can transiently report absent for a session that is actually alive;
// concluding "gone" on the first miss deletes the pipe and flips a live session
// to error. Only a probe that is still absent on the final attempt — after the
// retry/backoff window lets contention clear — means the session is really gone.
func TestShouldConcludeSessionGone_RetriesEarlyMisses(t *testing.T) {
	const maxRetries = 5

	// A probe that finds the session is never "gone".
	if shouldConcludeSessionGone(true, 0, maxRetries) {
		t.Fatal("a probe that found the session must never conclude it is gone")
	}

	// A miss on any non-final attempt is possibly-transient: retry, don't give up.
	for attempt := 0; attempt < maxRetries-1; attempt++ {
		if shouldConcludeSessionGone(false, attempt, maxRetries) {
			t.Fatalf("attempt %d of %d: a single early probe miss must be retried, "+
				"not treated as a dead session", attempt+1, maxRetries)
		}
	}

	// Still absent on the final attempt: now we conclude it is gone.
	if !shouldConcludeSessionGone(false, maxRetries-1, maxRetries) {
		t.Fatal("a probe still absent on the final attempt must conclude the session is gone")
	}
}

// TestTmuxSessionExistsOnSocket_ProbeTimeoutIsNotTreatedAsAbsent guards the
// reconnect-path companion to the Session.Exists() timeout fix. watchPipe()
// probes existence via tmuxSessionExistsOnSocket(); if tmux stalls, an
// unbounded probe blocks the reconnect goroutine before the retry/backoff
// logic can run. The probe must be bounded and treat a timeout as
// indeterminate — report the session as still present, not gone.
func TestTmuxSessionExistsOnSocket_ProbeTimeoutIsNotTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tmux")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 1\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	restore := hasSessionProbeTimeout
	hasSessionProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { hasSessionProbeTimeout = restore })

	if !tmuxSessionExistsOnSocket("agent-deck-reconnect-timeout-test", "busy-session") {
		t.Fatal("tmuxSessionExistsOnSocket returned false when the has-session probe timed out; " +
			"a stalled probe is indeterminate and must not be treated as a dead session")
	}
}
