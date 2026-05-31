package session

import (
	"context"
	"log/slog"
	"os/exec"
	"time"
)

// Issue #1225 Tier-2 WIRING. inbox_nudge.go holds the platform-independent
// policy core (WakeNudger: debounced, idle-only, best-effort). This file wires
// it into the live producer commit path so an IDLE conductor is woken to drain
// the MOMENT a completion durably lands — instead of waiting up to ~14 min for
// its next heartbeat. The trigger is event-driven (fired synchronously from the
// commit chokepoint, commitEventToInbox), NOT a poll loop.
//
// Everything here is best-effort: a nil wiring, a busy/non-conductor parent, or
// a failed send is harmless because the durable record is still drained on the
// parent's next Stop/heartbeat. Wake ≠ deliver.

// defaultWakeNudgeDebounce bounds how often a single idle parent is woken by a
// write-triggered nudge. A burst of N children completing near-simultaneously
// collapses to ONE wake; the parent's Stop-hook drain then consumes every
// pending record in that single woken turn, so a suppressed nudge loses no
// delivery. The window NEVER delays the FIRST completion (Nudge sends the first
// and only debounces subsequent ones), so it adds zero latency to the common
// single-completion case. ~500ms is the audit-fleet sweet spot: long enough to
// kill a thundering-herd of send-keys, short enough to be imperceptible — and
// well under the ~100-300ms Claude pane-pickup cost that dominates real latency.
const defaultWakeNudgeDebounce = 500 * time.Millisecond

// wakeNudgeMessage is the prompt fired into an idle conductor's pane to wake it.
// The content does not affect delivery — taking ANY turn runs the conductor's
// Stop-hook drain, which is what actually consumes the durable inbox. The text
// only tells the conductor WHY it woke so it acts on the queue immediately.
const wakeNudgeMessage = "[INBOX] A child just committed a completion to your inbox — drain it and act on each item now."

// wakeNudgeWiring carries the platform hooks the Tier-2 wake-nudge needs. It is
// kept out of the WakeNudger policy core so the idle-only/debounced policy stays
// testable without tmux: tests inject spy probes, production wires the live
// status probe + a best-effort no-wait pane send.
type wakeNudgeWiring struct {
	nudger *WakeNudger
	now    func() time.Time
	isIdle func(parent *Instance) bool
	send   func(parent *Instance, profile string) error
}

// defaultWakeNudgeWiring is the production wiring: a debounced nudger, the wall
// clock, the conductor-scoped idle probe, and a best-effort non-blocking pane
// send.
func defaultWakeNudgeWiring() *wakeNudgeWiring {
	return &wakeNudgeWiring{
		nudger: NewWakeNudger(defaultWakeNudgeDebounce),
		now:    time.Now,
		isIdle: parentIsNudgeableIdle,
		send:   sendWakeNudge,
	}
}

// fireWakeNudge invokes the Tier-2 wake-nudge for a parent that just had a
// completion durably committed. It is best-effort and MUST NOT affect the commit
// result: a nil wiring, a non-conductor/busy parent, or a send error are all
// swallowed (the durable record still drains on the next turn/heartbeat). A
// panic in the injected probe/send is recovered so a wake bug can never take
// down the producer.
func (n *TransitionNotifier) fireWakeNudge(parent *Instance, event TransitionNotificationEvent) {
	w := n.wake
	if w == nil || w.nudger == nil || parent == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			commsLog.Warn("wake_nudge_panic_recovered",
				slog.String("parent", parent.ID), slog.Any("panic", r))
		}
	}()

	now := time.Now()
	if w.now != nil {
		now = w.now()
	}
	profile := event.Profile
	isIdle := func() bool { return w.isIdle != nil && w.isIdle(parent) }
	send := func() error {
		if w.send == nil {
			return nil
		}
		return w.send(parent, profile)
	}
	if _, err := w.nudger.Nudge(parent.ID, now, isIdle, send); err != nil {
		// Best-effort: a failed wake is harmless. Log once at debug-ish level so
		// the operator can see WHY a pane wasn't woken without it being an error.
		commsLog.Warn("wake_nudge_send_failed",
			slog.String("parent", parent.ID), slog.String("error", err.Error()))
	}
}

// parentIsNudgeableIdle reports whether parent is safe to wake with a send-keys
// nudge: it must be a conductor (only conductors drain an inbox on Stop, so a
// nudge to a non-conductor leaf would be pure noise) AND currently idle/waiting,
// NOT mid-turn. A send-keys into a RUNNING pane only queues the keystroke
// (issue #36326) — the exact failure the pull model was built to avoid — so a
// busy conductor is left to drain at its own turn boundary.
//
// parent.Status is read directly (not re-probed): the conductor branch of
// resolveParentNotificationTarget freshly UpdateStatus()'d this same instance
// moments earlier on the commit path, so a second tmux round-trip would add cost
// without adding freshness.
func parentIsNudgeableIdle(parent *Instance) bool {
	if parent == nil || !isConductorSessionTitle(parent.Title) {
		return false
	}
	switch parent.Status {
	case StatusIdle, StatusWaiting:
		return true
	default:
		return false
	}
}

// sendWakeNudge fires one best-effort wake into the parent conductor's pane and
// returns immediately. The actual `session send --no-wait` runs detached so a
// slow/stuck send never blocks the producer commit path; the gate that this is
// only reached for an IDLE pane means the send won't sit in tmux's busy-queue.
// A failed send is harmless (the record still drains on the next turn).
func sendWakeNudge(parent *Instance, profile string) error {
	if parent == nil {
		return nil
	}
	go func(profile, ref string) {
		if err := sendWakeNudgeNoWait(profile, ref); err != nil {
			commsLog.Warn("wake_nudge_dispatch_failed",
				slog.String("parent", ref), slog.String("error", err.Error()))
		}
	}(profile, parent.ID)
	return nil
}

// wakeNudgeSendTimeout bounds the detached wake-nudge subprocess. --no-wait
// already returns fast, so 5s is generous; the bound exists purely so a wedged
// agent-deck binary (e.g. stuck on SQLite/tmux) is reaped instead of leaking the
// dispatch goroutine indefinitely (PR #1230 audit). A timed-out send is harmless
// like any dropped nudge — the record still drains on the next turn/heartbeat.
const wakeNudgeSendTimeout = 5 * time.Second

// wakeNudgeExec runs the resolved command under ctx. It is a package var so a
// test can substitute a spy and assert the deadline/args without spawning a real
// process; production runs the real bounded subprocess.
var wakeNudgeExec = func(ctx context.Context, bin string, args ...string) error {
	return exec.CommandContext(ctx, bin, args...).Run()
}

// sendWakeNudgeNoWait shells out to `agent-deck [-p profile] session send <ref>
// <msg> --no-wait -q`. --no-wait keeps it fire-and-forget: it neither blocks for
// the agent's ready state nor waits for a reply, so it returns fast even if the
// pane is wedged. The context deadline is a belt-and-suspenders backstop for the
// case where even the subprocess itself hangs.
func sendWakeNudgeNoWait(profile, ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), wakeNudgeSendTimeout)
	defer cancel()
	bin := agentDeckBinaryPath()
	args := []string{}
	if profile != "" {
		args = append(args, "-p", profile)
	}
	args = append(args, "session", "send", ref, wakeNudgeMessage, "--no-wait", "-q")
	return wakeNudgeExec(ctx, bin, args...)
}
