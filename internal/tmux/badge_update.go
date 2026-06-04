//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/fsnotify/fsnotify"
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
//     drops a per-session file under ~/.agent-deck/badge-updates/.
//
//   - Attach side → WatchBadgeUpdates(ctx, tmuxSessionName, w, configEnabled, ready)
//     watches the same directory via fsnotify (already a project dep
//     for ~/.agent-deck/events/), and when the file for THIS session
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
// same fsnotify machinery as the existing status event channel — zero
// new dependencies, same operational shape for ops.

// badgeUpdatesDirEnv lets tests redirect the signal directory away from
// the real ~/.agent-deck/badge-updates path so parallel runs do not
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
		return filepath.Join(os.TempDir(), ".agent-deck", "badge-updates")
	}
	return dir
}

// WriteBadgeUpdate atomically writes title under tmuxSessionName so the
// attach-side watcher can pick it up. Called by the Claude rename hook
// (which has no controlling tty) instead of the doomed
// EmitITermBadgeViaTty path.
//
// Atomic via tmp + rename — same idiom as WriteStatusEvent — so the
// fsnotify CREATE/WRITE the watcher sees never points at a partial file.
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
// ready, if non-nil, is closed once the fsnotify watch is registered.
// Callers that race a Write against the watcher's startup (i.e. tests)
// must wait on ready before writing — fsnotify drops events that fire
// before Add() returns.
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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		if ready != nil {
			close(ready)
		}
		return
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		if ready != nil {
			close(ready)
		}
		return
	}

	// Catch-up: if the hook fired before the watcher registered, we'd
	// miss the event. Read the file once at startup so a rename that
	// completed during agent-deck's attach setup still updates the
	// badge. No-op when the file does not exist.
	emitFromFile(filepath.Join(dir, tmuxSessionName), w, configEnabled)

	if ready != nil {
		close(ready)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			// Filter strictly on filename — concurrent attaches in the
			// same iTerm2 window must not steal each other's badges.
			if filepath.Base(ev.Name) != tmuxSessionName {
				continue
			}
			emitFromFile(ev.Name, w, configEnabled)
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// fsnotify errors are non-fatal here; the next event will
			// retry. We deliberately do NOT log to stdout because that's
			// the iTerm2 tty in production — a stray log line would
			// corrupt the user's display.
		}
	}
}

// emitFromFile reads the badge title from path and writes the OSC to w,
// gated on the same iTerm2-active + configEnabled rules as the
// on-attach emitITermBadge. Missing file is a silent no-op (the user
// may have detached / cleaned up between the event and the read).
func emitFromFile(path string, w io.Writer, configEnabled bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	emitITermBadge(w, string(data), configEnabled)
}
