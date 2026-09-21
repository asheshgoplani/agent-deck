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

	"github.com/asheshgoplani/agent-deck/internal/logging"
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
	// notify-daemon takes no positional operands (only --once), so a bare
	// "help" can never collide with a legitimate data value; treat it the
	// same as --help/-h (issue #2025).
	if hooksHelpRequested(args) {
		fs.Usage()
		return
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// handleNotifyDaemon is dispatched from main()'s early command switch and
	// returns before main() runs logging.Init, so without this the daemon's
	// globalLogger stays nil and every commsLog record — including
	// wake_nudge_send_failed / wake_nudge_dispatch_failed — goes to io.Discard.
	// That blind spot is exactly why the 2026-06-18 wake regression could not be
	// diagnosed from the logs. See initDaemonLogging.
	defer initDaemonLogging()()

	// One unconditional startup line so an operator can confirm the daemon is
	// alive AND that its logging pipeline works (the absence of which hid the
	// 2026-06-18 regression). Subsequent comms records share this CompNotif
	// stream in ~/.cache/agent-deck/debug.log.
	logging.ForComponent(logging.CompNotif).Info("notify_daemon_started",
		"once", *once,
		"version", Version,
		"debug", os.Getenv("AGENTDECK_DEBUG") != "",
	)

	// Review round 2 (P1-A/P1-B): the hook half of the spine must not stay
	// broken until an operator runs `hooks install`. Repair a dangling,
	// stale-version or marker-less install before the first sync pass.
	healClaudeHooksAtDaemonStart()

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

	defer startRuntimeHealth("", "notify-daemon")()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// STEP 1 (issue #1214): never run stale code. The transition-notifier unit
	// is Restart=always, so cleanly exiting on a binary upgrade guarantees the
	// supervisor brings the daemon back on the current binary — the 20-day
	// stale window becomes impossible. RuntimeMaxSec in the unit file is the
	// belt-and-suspenders backstop for environments without this watcher.
	go watchBinaryVersion(ctx, cancel)

	// A headless machine running only notify-daemon (no `web --no-tui`, no
	// open TUI) previously had nothing polling GitHub between runs of the
	// daily update timer: near-event-driven updates need every long-running
	// process to poll, not just the ones with a UI. Same installer, same
	// gates (auto_install, suppression, Homebrew) as `web --no-tui` uses.
	startHeadlessAutoInstall(ctx)

	if err := daemon.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "notify-daemon error: %v\n", err)
		os.Exit(1)
	}
}

// initDaemonLogging wires up structured file logging for the always-on daemon
// and returns a shutdown func for the caller to defer. Unlike the TUI/CLI, the
// daemon has no terminal to corrupt, so it always logs to the XDG cache
// debug.log at info level (bumped to debug under AGENTDECK_DEBUG) — capturing
// the Warn-level comms failures that previously vanished into io.Discard.
// lumberjack rotation (size/backup/age) bounds growth. On cache-dir failure it
// degrades to a no-op so the daemon still runs.
func initDaemonLogging() func() {
	cacheDir, err := ensureEffectiveCacheDir()
	if err != nil {
		return func() {}
	}
	level := "info"
	if os.Getenv("AGENTDECK_DEBUG") != "" {
		level = "debug"
	}
	logCfg := logging.Config{
		Debug:                 true, // daemon has no TUI to interfere with; always write
		LogDir:                cacheDir,
		Level:                 level,
		Format:                "json",
		MaxSizeMB:             10,
		MaxBackups:            5,
		MaxAgeDays:            10,
		Compress:              true,
		RingBufferSize:        10 * 1024 * 1024,
		AggregateIntervalSecs: 30,
	}
	// Honor the same user-config log overrides main() applies, so an operator
	// who tuned rotation/format in config.toml gets it for the daemon too.
	if userCfg, err := session.LoadUserConfig(); err == nil {
		ls := userCfg.Logs
		if os.Getenv("AGENTDECK_DEBUG") != "" && ls.DebugLevel != "" {
			logCfg.Level = ls.DebugLevel
		}
		if ls.DebugFormat != "" {
			logCfg.Format = ls.DebugFormat
		}
		if ls.DebugMaxMB > 0 {
			logCfg.MaxSizeMB = ls.DebugMaxMB
		}
		if ls.DebugBackups > 0 {
			logCfg.MaxBackups = ls.DebugBackups
		}
		if ls.DebugRetentionDays > 0 {
			logCfg.MaxAgeDays = ls.DebugRetentionDays
		}
		logCfg.Compress = ls.GetDebugCompress()
	}
	logging.Init(logCfg)
	session.LogStoreRootSelection()
	return logging.Shutdown
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

// healClaudeHooksAtDaemonStart runs session.HealClaudeHooks for the daemon's
// Claude config dir when hooks are enabled. Best-effort: a failure is logged
// and the daemon still runs.
func healClaudeHooksAtDaemonStart() {
	cfg, _ := session.LoadUserConfig()
	if cfg != nil && !cfg.Claude.GetHooksEnabled() {
		return
	}
	configDir := getClaudeConfigDirForHooks()
	res, err := session.HealClaudeHooks(configDir, Version)
	log := logging.ForComponent(logging.CompNotif)
	// The accounts usage feed exists by construction: every configured slot's
	// statusLine is wrapped with the ingester here too, so a remote whose
	// binary was just updated starts feeding without an operator visit.
	for _, r := range session.HealUsageFeeds(cfg) {
		switch {
		case r.Err != nil:
			log.Warn("claude_usage_feed_heal_failed", "slot", r.Slot, "error", r.Err.Error())
		case r.Changed:
			log.Info("claude_usage_feed_wired", "slot", r.Slot, "command", r.Feed.Command)
		}
	}
	switch {
	case err != nil:
		log.Warn("claude_hooks_heal_failed", "config_dir", configDir, "error", err.Error())
	case res.Healed:
		log.Info("claude_hooks_healed", "config_dir", configDir, "reasons", strings.Join(res.Reasons, "; "))
	case res.Skipped != "":
		// An unpinnable dev build, or a hook binary newer than this one:
		// reported, never written (review round 3, finding 2).
		log.Info("claude_hooks_heal_skipped", "config_dir", configDir, "reason", res.Skipped)
	}
}
