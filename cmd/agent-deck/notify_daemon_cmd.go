package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// versionCheckInterval is how often the always-on daemon re-reads the on-disk
// binary version to decide whether it should recycle (issue #1214 STEP 1).
const versionCheckInterval = 60 * time.Second

// handleNotifyDaemon runs the always-on transition notifier daemon.
func handleNotifyDaemon(args []string) {
	fs := flag.NewFlagSet("notify-daemon", flag.ExitOnError)
	once := fs.Bool("once", false, "Run one sync pass and exit")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck notify-daemon [--once]")
		fmt.Println()
		fmt.Println("Run status-driven transition notification daemon.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	daemon := session.NewTransitionDaemon()
	if *once {
		daemon.SyncOnce(context.Background())
		// Ensure async dispatches started during SyncOnce land on disk
		// before the process exits; otherwise logs/queue state written by
		// the watcher/sender goroutines would race with the CLI shutdown
		// and leave the operator staring at an empty log.
		daemon.Flush()
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// STEP 1 (issue #1214): never run stale code. The transition-notifier unit
	// is Restart=always, so cleanly exiting on a binary upgrade guarantees the
	// supervisor brings the daemon back on the current binary — the 20-day
	// stale window becomes impossible. RuntimeMaxSec in the unit file is the
	// belt-and-suspenders backstop for environments without this watcher.
	go watchBinaryVersion(ctx, cancel)

	if err := daemon.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "notify-daemon error: %v\n", err)
		os.Exit(1)
	}
}

// watchBinaryVersion periodically compares the running binary's compiled-in
// version against the version of the binary currently on disk; on a mismatch it
// cancels the daemon context so it exits cleanly and the supervisor restarts it
// fresh. Recycling only on a definite mismatch (ShouldRecycleForVersion ignores
// empty/unknown versions) means a transient read failure never flaps the daemon.
func watchBinaryVersion(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(versionCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			onDisk := readOnDiskVersion()
			if session.ShouldRecycleForVersion(Version, onDisk) {
				fmt.Fprintf(os.Stderr, "notify-daemon: binary upgraded (running %s, on-disk %s); recycling\n", Version, onDisk)
				cancel()
				return
			}
		}
	}
}

// readOnDiskVersion runs the current executable path with `version` and parses
// the semver token. After an in-place upgrade the path holds the new bytes while
// this process still runs the old inode, so this reads the NEW version. Returns
// "" on any failure (treated as "unknown" -> no recycle).
func readOnDiskVersion() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		return ""
	}
	return parseAgentDeckVersion(string(out))
}

// parseAgentDeckVersion extracts the version token from a writeVersionOutput
// line, e.g. "Agent Deck v1.9.42 (update available: v1.9.43)" -> "1.9.42".
func parseAgentDeckVersion(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	const marker = "Agent Deck v"
	_, rest, ok := strings.Cut(s, marker)
	if !ok {
		return ""
	}
	end := len(rest)
	for i, r := range rest {
		if r == ' ' || r == '(' {
			end = i
			break
		}
	}
	return strings.TrimSpace(rest[:end])
}
