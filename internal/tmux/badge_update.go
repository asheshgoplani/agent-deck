//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// Issue #1114: Badge update signal channel from hook to attach.
//
// Background
// ----------
// `EmitITermBadgeViaTty` opens /dev/tty and writes a tmux DCS-wrapped OSC
// 1337 SetBadgeFormat sequence. That works when the caller has a
// controlling terminal — e.g. the agent-deck process itself during
// Attach. It does NOT work when the caller is a Claude Code hook
// subprocess: Claude spawns its hooks detached via setsid (no
// controlling tty), so /dev/tty returns ENXIO and the OSC is dropped
// silently.
//
// Result on main: after Claude /rename or `claude --name X` mid-attach,
// the DB title updates (#572 path) but the iTerm2 badge stays at the
// attach-time value.
//
// Fix shape
// ---------
// File writes succeed regardless of controlling-terminal state. So:
//
//   - Hook side  → WriteBadgeUpdate(tmuxSessionName, title)
//     drops a per-session file under the effective badge-updates data dir.
//
//   - Attach side → WatchBadgeUpdates(ctx, tmuxSessionName, w, configEnabled, ready)
//     polls only the file for THIS session, and when it
//     changes, reads the new title and emits the OSC through `w`. In
//     production `w` is `os.Stdout`, which the attach process owns;
//     the outer iTerm2 sees the OSC directly.
//
// Why not Option B (tmux env var)
// -------------------------------
// `tmux set-environment` requires the tmux server to accept the command
// and the attach process to poll or subscribe. Polling has latency
// (visible badge lag) and subscribing requires a control-mode socket
// that we'd then have to multiplex. The file-signal approach reuses the
// existing status event file convention with no new dependencies.

// badgeUpdatesDirEnv lets tests redirect the signal directory away from
// the real badge-updates path so parallel runs do not
// collide. The env var is intentionally undocumented in user-facing
// config — it exists for the test harness only.
const badgeUpdatesDirEnv = "AGENTDECK_BADGE_UPDATES_DIR"

// BadgeUpdatesDir returns the directory where rename-hook signals live.
// Uses the XDG data directory, falling back to a legacy badge-updates dir.
func BadgeUpdatesDir() string {
	if v := strings.TrimSpace(os.Getenv(badgeUpdatesDirEnv)); v != "" {
		return v
	}
	dir, err := agentpaths.EffectiveDataPath("badge-updates", "badge-updates")
	if err != nil {
		return filepath.Join(os.TempDir(), "agent-deck", "badge-updates")
	}
	return dir
}

// WriteBadgeUpdate atomically writes title under tmuxSessionName so the
// attach-side watcher can pick it up. Called by the Claude rename hook
// (which has no controlling tty) instead of the doomed
// EmitITermBadgeViaTty path.
//
// Atomic via tmp + rename — same idiom as WriteStatusEvent — so the
// reader never sees a partial file.
// tmuxSessionName is used verbatim as the filename; tmux session names
// are constrained enough (no slashes) that the path stays single-segment.
func WriteBadgeUpdate(tmuxSessionName, title string) error {
	if tmuxSessionName == "" {
		return fmt.Errorf("badge update: empty tmux session name")
	}
	dir := BadgeUpdatesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("badge update: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, tmuxSessionName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(title), 0o644); err != nil {
		return fmt.Errorf("badge update: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("badge update: rename: %w", err)
	}
	return nil
}

// WatchBadgeUpdates blocks until ctx is cancelled. While running, it
// emits an iTerm2 SetBadgeFormat OSC to w every time the badge-update
// file for tmuxSessionName changes. The same two gates as
// emitITermBadge apply (iTerm2 detection + configEnabled), so the
// watcher cannot bypass the user's opt-out.
//
// ready, if non-nil, is closed after the initial file read.
//
// Called from Attach() in its own goroutine; the ctx cancel that runs
// on detach is what stops it.
func WatchBadgeUpdates(ctx context.Context, tmuxSessionName string, w io.Writer, configEnabled bool, ready chan<- struct{}) {
	if tmuxSessionName == "" {
		if ready != nil {
			close(ready)
		}
		return
	}
	dir := BadgeUpdatesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		if ready != nil {
			close(ready)
		}
		return
	}

	targetPath := filepath.Join(dir, tmuxSessionName)
	var lastTitle string
	var haveLastTitle bool
	emitCurrent := func() {
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return
		}
		title := string(data)
		if haveLastTitle && title == lastTitle {
			return
		}
		lastTitle = title
		haveLastTitle = true
		emitITermBadge(w, title, configEnabled)
	}

	// Catch-up: if the hook fired before the watcher registered, we'd
	// miss the event. Read the file once at startup so a rename that
	// completed during agent-deck's attach setup still updates the
	// badge. No-op when the file does not exist.
	emitCurrent()

	if ready != nil {
		close(ready)
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			// Poll only this session so retained badge files do not cost
			// a descriptor each on kqueue. Emit only when content changes.
			emitCurrent()
		}
	}
}
