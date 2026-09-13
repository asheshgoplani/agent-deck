package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// In-place restart of the TUI.
//
// The maintainer's complaint: after `agent-deck update` (or the auto-update
// job) replaces the binary, the open TUI keeps running the old code and has
// to be closed and reopened by hand. The restart_deck hotkey (default
// ctrl+t) runs the normal shutdown sequence (state flushed, watchers and
// pipes closed, primary claim released) and then, once Bubble Tea has
// restored the terminal, main() replaces this process with the executable at
// the same path, with the same args and env, so the new build comes up
// exactly the way the user launched the old one.
//
// The key is only reachable from the home screen: while attached, Bubble Tea
// is parked inside tea.Exec and keystrokes go to tmux, so there is never an
// attached pane to detach from here. Modal dialogs and in-flight session
// actions block the restart with a footer message instead of losing work.

// errRestartBlocked wraps the reason a restart was refused.
var errRestartBlocked = errors.New("restart blocked")

// sessionActionInFlight reports whether a create/resume/fork/setup/remote
// restart is still running, or a tmux attach is being set up.
func (h *Home) sessionActionInFlight() bool {
	return len(h.launchingSessions) > 0 ||
		len(h.resumingSessions) > 0 ||
		len(h.forkingSessions) > 0 ||
		len(h.creatingSessions) > 0 ||
		len(h.setupRunningSessions) > 0 ||
		len(h.remoteRestarting) > 0 ||
		h.isAttaching.Load()
}

// Seams for the pre-arm check of the restart target; tests swap them so no
// real binary is stat'ed or run.
var (
	checkRestartExecutable = update.CheckExecutable
	probeRestartTarget     = update.ProbeBinaryVersion
)

// restartTargetProblem is the pre-arm check: before the TUI is torn down
// the file it is about to exec must exist, be a regular non-empty file
// with an exec bit, and answer `<exe> version` (a dry run of the new
// build). Any failure refuses the restart while the old binary is still
// running, so a half-written or broken install never leaves the user
// without a deck. Returns "" when the target is good.
func (h *Home) restartTargetProblem() string {
	exe := h.restartExecutable()
	if exe == "" {
		return "executable path unknown"
	}
	if err := checkRestartExecutable(exe); err != nil {
		return fmt.Sprintf("new binary is not runnable (%v)", err)
	}
	if _, err := probeRestartTarget(exe); err != nil {
		return fmt.Sprintf("new binary failed its dry run (%v)", err)
	}
	return ""
}

// restartBlockReason returns "" when a restart may proceed, otherwise a
// short reason for the footer. The cheap state checks come first; the
// target check (a stat and one exec of the new binary) runs only once
// nothing else blocks.
func (h *Home) restartBlockReason() string {
	switch {
	case h.restartRequested:
		return "restart already in progress"
	case h.hasModalVisible() || h.insertMode:
		return "close the open dialog first"
	case h.sessionActionInFlight():
		return "a session action is still running, try again in a moment"
	}
	return h.restartTargetProblem()
}

// tryRestartDeck is the restart_deck key handler. It either refuses with a
// footer message or arms the restart and starts the regular quit sequence.
// The MCP pool is left running (performQuit(false)) so the new process can
// reconnect to it instead of cold-starting every MCP.
func (h *Home) tryRestartDeck() (tea.Model, tea.Cmd) {
	if reason := h.restartBlockReason(); reason != "" {
		h.setError(fmt.Errorf("%w: %s", errRestartBlocked, reason))
		return h, nil
	}
	h.restartRequested = true
	h.isQuitting = true
	uiLog.Info("tui_restart_requested",
		"running", Version,
		"installed", h.installedUpdateVersion(),
		"exe", h.restartExecutable())
	return h, h.performQuit(false)
}

// autoRestartLogEvery rate-limits the "waiting for idle" log line.
const autoRestartLogEvery = time.Minute

// autoRestartRetryAfter is how long the auto path leaves a target alone
// after its pre-arm check failed (the check execs the new binary once, so
// it must not run on every 2 s tick).
const autoRestartRetryAfter = time.Minute

// maybeAutoRestart is the auto_restart path: once a newer build is on disk
// it arms the same quit-and-exec sequence as the key, without a key press,
// at the first tick where nothing blocks a restart. While a dialog is
// open, insert mode is active or a session action is in flight it stays
// quiet (the banner already says "restarting when idle") and tries again
// on the next tick. It is only ever reached from the home screen: while
// attached, Bubble Tea is parked inside tea.Exec and no tick arrives, so
// tmux sessions and servers are never touched by the hand-over.
func (h *Home) maybeAutoRestart() tea.Cmd {
	installed := h.installedUpdateVersion()
	if installed == "" || h.restartRequested || !h.autoRestartEnabled() {
		return nil
	}
	if time.Now().Before(h.autoRestartHoldUntil) {
		return nil
	}
	if reason := h.restartBlockReason(); reason != "" {
		if time.Since(h.autoRestartLoggedAt) >= autoRestartLogEvery {
			h.autoRestartLoggedAt = time.Now()
			uiLog.Info("tui_auto_restart_waiting", slog.String("installed", installed), slog.String("reason", reason))
		}
		if strings.HasPrefix(reason, "new binary") {
			// The target itself is bad: say so once, keep running the old
			// build, and do not re-run the dry probe on every tick. A later
			// change of the file resets the watch and the hold.
			h.setError(fmt.Errorf("%w: %s; still running v%s", errRestartBlocked, reason, Version))
			h.autoRestartHoldUntil = time.Now().Add(autoRestartRetryAfter)
		}
		return nil
	}
	uiLog.Info("tui_auto_restart", slog.String("running", Version), slog.String("installed", installed))
	_, cmd := h.tryRestartDeck()
	return cmd
}

// autoRestartEnabled reads [updates].auto_restart (default true) and
// refuses outright when the process is driven by a test, CI or a script
// (Home.autoUpdateSuppressedReason, issue #2251): the key still works,
// nothing happens on its own.
func (h *Home) autoRestartEnabled() bool {
	return h.autoUpdateSuppressedReason == "" && loadUpdateSettings().GetAutoRestart()
}

// restartExecutable is the path to exec: the path fingerprinted at startup
// when available (it is the file the installer replaced), else whatever
// os.Executable says now.
func (h *Home) restartExecutable() string {
	if h.binaryWatch != nil && h.binaryWatch.execPath != "" {
		return h.binaryWatch.execPath
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// RestartTarget reports whether an in-place restart was armed (by the key
// or by auto_restart) and, if so, which executable main() should exec
// after tea.Program.Run returns.
func (h *Home) RestartTarget() (string, bool) {
	if !h.restartRequested {
		return "", false
	}
	exe := h.restartExecutable()
	return exe, exe != ""
}

// Environment handed from the old process to the new one across the exec.
// The new Home reads and unsets both immediately (consumeRestartEnv) so
// they never leak into sessions it launches.
const (
	// restartSelectEnv carries the id of the session the cursor was on.
	restartSelectEnv = "AGENTDECK_RESTART_SELECT"
	// restartedFromEnv carries the version that restarted, for the
	// "restarted into vNEW (was vOLD)" notice.
	restartedFromEnv = "AGENTDECK_RESTARTED_FROM"
)

// RestartHandoff is what the new process needs to pick up where this one
// left off.
type RestartHandoff struct {
	SelectedID string
	OldVersion string
}

// RestartHandoff describes the state to carry across the exec: the
// selected session (if the cursor is on one) and the running version.
func (h *Home) RestartHandoff() RestartHandoff {
	hand := RestartHandoff{OldVersion: Version}
	if inst := h.getSelectedSession(); inst != nil {
		hand.SelectedID = inst.ID
	}
	return hand
}

// buildRestartEnv returns env with the hand-off variables set (any stale
// copies from an earlier restart removed first).
func buildRestartEnv(env []string, hand RestartHandoff) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if strings.HasPrefix(kv, restartSelectEnv+"=") || strings.HasPrefix(kv, restartedFromEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	if hand.SelectedID != "" {
		out = append(out, restartSelectEnv+"="+hand.SelectedID)
	}
	if hand.OldVersion != "" {
		out = append(out, restartedFromEnv+"="+hand.OldVersion)
	}
	return out
}

// parseRestartEnv reads the hand-off from a getenv-shaped lookup.
func parseRestartEnv(getenv func(string) string) RestartHandoff {
	return RestartHandoff{
		SelectedID: strings.TrimSpace(getenv(restartSelectEnv)),
		OldVersion: strings.TrimSpace(getenv(restartedFromEnv)),
	}
}

// consumeRestartEnv reads the hand-off from the process environment and
// unsets it at once so no child launched later inherits it.
func consumeRestartEnv() RestartHandoff {
	hand := parseRestartEnv(os.Getenv)
	_ = os.Unsetenv(restartSelectEnv)
	_ = os.Unsetenv(restartedFromEnv)
	return hand
}

// applyRestartHandoff runs once after the first session load of a process
// that was exec'd by its predecessor: it puts the cursor back on the
// session that was selected and tells the user which version they are on
// now. The notice goes through the footer as a plain message, not a
// failure.
func (h *Home) applyRestartHandoff() {
	hand := h.restartHandoff
	h.restartHandoff = RestartHandoff{}
	if hand.SelectedID != "" {
		h.SelectSessionByID(hand.SelectedID)
	}
	if hand.OldVersion != "" {
		uiLog.Info("tui_restarted", slog.String("from", hand.OldVersion), slog.String("into", Version))
		h.setError(fmt.Errorf("restarted into v%s (was v%s)", Version, hand.OldVersion))
	}
}

// ExecSelf replaces the current process with exe, keeping os.Args and the
// environment plus the hand-off variables. It only returns on failure (or
// on platforms without exec). Call it after the TUI has restored the
// terminal. The exec itself lives in internal/update so the headless
// entrypoints use the same site.
func ExecSelf(exe string, hand RestartHandoff) error {
	// A self re-exec, not a child spawn: the new image must see the exact
	// environment the user launched the old one with, so the childenv
	// filter (which strips CLAUDE_CONFIG_DIR for claude workers) does not
	// apply here.
	env := buildRestartEnv(os.Environ(), hand) //nolint:forbidigo // self re-exec, not a child launch (#1163 is about claude workers)
	return update.ExecSelf(exe, env)
}
