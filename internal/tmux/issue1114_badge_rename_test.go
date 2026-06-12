//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Issue #1114 — iTerm2 badge stays stale after Claude /rename or
// `claude --name`. Root cause: the Claude rename hook subprocess is
// spawned detached (setsid) and has NO controlling tty, so the original
// `EmitITermBadgeViaTty` silently no-ops (open /dev/tty returns ENXIO).
//
// Fix design (Option A): the hook (which CAN write files even without a
// tty) writes the new badge title to ~/.agent-deck/badge-updates/<tmux>.
// The agent-deck Attach process — which owns the outer iTerm2 tty via
// os.Stdout — watches the directory and re-emits the OSC when the file
// changes.
//
// These tests exercise the file-signal contract directly so the bug
// stays fixed without needing a real Claude subprocess in CI.

// lockedWriter serializes writes so the asserting goroutine and the
// watcher goroutine don't race on the underlying buffer.
type lockedWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func newLockedWriter() *lockedWriter {
	return &lockedWriter{buf: &bytes.Buffer{}}
}

// useTestBadgeDir redirects BadgeUpdatesDir() to a per-test temp path so
// concurrent test runs don't fight over ~/.agent-deck/badge-updates.
func useTestBadgeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENTDECK_BADGE_UPDATES_DIR", dir)
	return dir
}

func useSimulatedITerm(t *testing.T) {
	t.Helper()
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "")
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")
}

// waitForOSC blocks until w contains an OSC SetBadgeFormat for title, or
// the deadline expires.
func waitForOSC(t *testing.T, w *lockedWriter, title string, timeout time.Duration) {
	t.Helper()
	want := formatITermBadgeOSC(title)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(w.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited %v for badge OSC for %q; writer contained: %q", timeout, title, w.String())
}

// TestIssue1114_WriteBadgeUpdate_AtomicFileWrite is the bottom of the
// stack: the hook subprocess (which has no tty) must be able to drop a
// signal that the attach process will pick up. Even when /dev/tty is
// unavailable, file writes work.
func TestIssue1114_WriteBadgeUpdate_AtomicFileWrite(t *testing.T) {
	dir := useTestBadgeDir(t)

	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", "session-renamed"))

	got, err := os.ReadFile(filepath.Join(dir, "ad-rename-fixture"))
	require.NoError(t, err)
	require.Equal(t, "session-renamed", string(got),
		"WriteBadgeUpdate must persist the new title verbatim under the tmux-session-name key")
}

// TestIssue1114_AttachWatcher_EmitsOSCOnFileChange is the happy path:
// the attach process's watcher picks up a file written by the hook side
// and writes the iTerm2 OSC to the writer it was handed (os.Stdout in
// production, a buffer in this test). This is the structural inverse of
// the pre-fix bug where EmitITermBadgeViaTty silently dropped the OSC.
func TestIssue1114_AttachWatcher_EmitsOSCOnFileChange(t *testing.T) {
	useTestBadgeDir(t)
	useSimulatedITerm(t)

	w := newLockedWriter()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchBadgeUpdates(ctx, "ad-rename-fixture", w, true, ready)
	}()

	// Wait for the watcher to register its fsnotify subscription before
	// writing — fsnotify drops events from before Add() returns.
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never signaled ready")
	}

	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", "fresh-title"))
	waitForOSC(t, w, "fresh-title", 3*time.Second)

	// And a second rename mid-attach must also propagate — the watcher
	// keeps running across multiple updates, not a one-shot.
	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", "fresh-title-2"))
	waitForOSC(t, w, "fresh-title-2", 3*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit after ctx cancel")
	}
}

// TestIssue1114_Watcher_IgnoresOtherSessions is the boundary case: a
// concurrent attach to session "other" must not steal session "mine"'s
// badge update. The filename-based filter is the only thing standing
// between two attached agent-deck users in the same iTerm2 window.
func TestIssue1114_Watcher_IgnoresOtherSessions(t *testing.T) {
	useTestBadgeDir(t)
	useSimulatedITerm(t)

	w := newLockedWriter()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchBadgeUpdates(ctx, "session-mine", w, true, ready)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never signaled ready")
	}

	// Write for an unrelated session — must NOT emit.
	require.NoError(t, WriteBadgeUpdate("session-other", "not-for-me"))
	time.Sleep(300 * time.Millisecond) // give fsnotify time to misfire if buggy
	require.NotContains(t, w.String(), formatITermBadgeOSC("not-for-me"),
		"watcher for session-mine must ignore updates for session-other; got %q", w.String())

	// Now write for the real session — must emit, proving the watcher is
	// alive and only filtering on filename match.
	require.NoError(t, WriteBadgeUpdate("session-mine", "for-me"))
	waitForOSC(t, w, "for-me", 3*time.Second)

	cancel()
	<-done
}

// TestIssue1114_Watcher_NoOpOutsideITerm2 is the failure mode: when the
// outer terminal is not iTerm2, we must not blast OSC 1337 bytes at it
// (other terminals render the payload as literal garbage in the user's
// pane). Same gate as the original emitITermBadge — but the gate must
// also live in the watcher path, not just the on-attach emit.
func TestIssue1114_Watcher_NoOpOutsideITerm2(t *testing.T) {
	useTestBadgeDir(t)
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "")
	t.Setenv("ITERM_SESSION_ID", "")
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("AGENTDECK_ITERM_BADGE", "")

	w := newLockedWriter()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchBadgeUpdates(ctx, "ad-rename-fixture", w, true, ready)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never signaled ready")
	}

	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", "any-title"))
	time.Sleep(400 * time.Millisecond)

	require.Empty(t, w.String(),
		"watcher must be a strict no-op when the outer terminal is not iTerm2; got %q", w.String())

	cancel()
	<-done
}

// TestIssue1114_Watcher_ConfigDisabledSuppresses pins the second gate:
// even on iTerm2, if [terminal].iterm_badge=false (and AGENTDECK_ITERM_BADGE
// doesn't force-enable), the watcher must not emit. This matches the
// existing emitITermBadge gating exactly so the via-watch path can't
// silently bypass the user's opt-out.
func TestIssue1114_Watcher_ConfigDisabledSuppresses(t *testing.T) {
	useTestBadgeDir(t)
	useSimulatedITerm(t)

	w := newLockedWriter()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchBadgeUpdates(ctx, "ad-rename-fixture", w, false /* configEnabled */, ready)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never signaled ready")
	}

	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", "any-title"))
	time.Sleep(400 * time.Millisecond)

	require.Empty(t, w.String(),
		"watcher must respect configEnabled=false even on iTerm2; got %q", w.String())

	cancel()
	<-done
}

// TestIssue1114_OSCMatchesAttachEmit asserts that the OSC byte sequence
// emitted by the watcher is byte-identical to the on-attach emit.
// Drift here would cause iTerm2 to render two different badges for the
// same DisplayName depending on whether the attach was fresh or post-rename.
func TestIssue1114_OSCMatchesAttachEmit(t *testing.T) {
	useTestBadgeDir(t)
	useSimulatedITerm(t)

	w := newLockedWriter()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		WatchBadgeUpdates(ctx, "ad-rename-fixture", w, true, ready)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never signaled ready")
	}

	const title = "renamed-mid-attach"
	require.NoError(t, WriteBadgeUpdate("ad-rename-fixture", title))
	waitForOSC(t, w, title, 3*time.Second)

	// The exact byte sequence iTerm2 parses on the wire.
	wantOSC := "\x1b]1337;SetBadgeFormat=" + base64.StdEncoding.EncodeToString([]byte(title)) + "\a"
	require.Contains(t, w.String(), wantOSC,
		"watcher OSC must be byte-identical to the on-attach emitITermBadge format")

	cancel()
	<-done
}

// TestIssue1114_AttachLaunchesWatcherWithCancelableCtx is the structural
// regression test for the badge-watcher resource leak: Attach() used to
// launch `go WatchBadgeUpdates(ctx, ...)` BEFORE the
// `ctx, cancel := context.WithCancel(ctx)` line, so the goroutine
// captured the caller's context — context.Background() on the TUI path
// (attachCmd.Run) — and was never stopped. Every attach leaked one
// goroutine (250 ms poll ticker) plus one fsnotify watcher (an inotify
// fd + an epoll fd on Linux), all watching the same badge-updates
// directory inode. A day of deck hopping accumulated ~400 watchers and
// double-digit sustained CPU.
//
// Attach itself needs a live tmux server, so — same pattern as the
// PERF-E / mobile-input structural specs — this asserts on the source:
// inside Attach(), the WithCancel call must precede the
// WatchBadgeUpdates launch so the watcher receives the context that the
// deferred cancel actually cancels on detach.
func TestIssue1114_AttachLaunchesWatcherWithCancelableCtx(t *testing.T) {
	src, err := os.ReadFile("pty.go")
	require.NoError(t, err)

	attachIdx := strings.Index(string(src), "func (s *Session) Attach(")
	require.GreaterOrEqual(t, attachIdx, 0, "Attach() not found in pty.go")
	body := string(src)[attachIdx:]

	withCancelIdx := strings.Index(body, "context.WithCancel(")
	watchIdx := strings.Index(body, "go WatchBadgeUpdates(")
	require.GreaterOrEqual(t, withCancelIdx, 0, "context.WithCancel not found in Attach()")
	require.GreaterOrEqual(t, watchIdx, 0, "go WatchBadgeUpdates not found in Attach()")

	require.Less(t, withCancelIdx, watchIdx,
		"WatchBadgeUpdates must be launched AFTER context.WithCancel in Attach(); "+
			"launching it before hands it the caller's non-cancellable context "+
			"(context.Background() on the TUI attach path) and leaks one goroutine + "+
			"one fsnotify watcher per attach")
}
