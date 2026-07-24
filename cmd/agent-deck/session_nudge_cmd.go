package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session nudge` — the supervised-send verb.
//
// Why this exists as a command rather than a documented recipe: every
// supervisor session that watches children ends up hand-rolling the same loop
// in throwaway bash, and the loop has a failure mode that looks like success.
// The one that motivated this (2026-07-24) was:
//
//	agent-deck session send "$SID" "Auto-nudge..." >/dev/null 2>&1
//	nudges=$((nudges+1)); echo "NUDGED ... (nudge #$nudges)"
//
// `session send` had done its job — it detected the message sitting unsent in
// a gated composer and exited nonzero with delivery=typed_not_submitted
// (#1413) — but the script discarded stdout, stderr AND the exit code, then
// reported success unconditionally. It claimed 56 nudges against a session
// that had received 53, while the target sat wedged for an hour with three
// children idling behind it.
//
// So the loop lives here instead, where it is typed, tested, and cannot be
// silenced by a redirect:
//
//   - a nudge into a busy session is NOT an error (nothing to do — the session
//     is already working), and reports delivered=false without a nonzero exit,
//     so a polling supervisor does not treat normal operation as a failure;
//   - a nudge into a STALLED session is refused, because a gated composer
//     swallows nudges: it needs Escape+Enter or a restart, and telling the
//     supervisor that is the entire point;
//   - anything short of a confirmed submit exits nonzero with a machine-
//     checkable code, so `if agent-deck session nudge ...` is correct by
//     default and the phantom-nudge bug is unwritable.

// nudgeOutcome values reported in the `outcome` JSON field.
const (
	nudgeDelivered = "delivered"
	nudgeSkipped   = "skipped_busy"
	nudgeRefused   = "refused_stalled"
)

// ErrCodeSessionStalled marks a target whose composer is gated: it accepts
// keystrokes but does not submit them, so sending is pointless until a human
// or `session send`'s Escape+Enter recovery releases it.
const ErrCodeSessionStalled = "SESSION_STALLED"

func handleSessionNudge(profile string, args []string) {
	fs := flag.NewFlagSet("session nudge", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	force := fs.Bool("force", false, "Nudge even when the session is busy or stalled (bypasses every precondition)")
	messageFile := fs.String("message-file", "", "Read the message from a file ('-' for stdin) instead of a positional argument")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session nudge <id|title> <message> [options]")
		fmt.Println()
		fmt.Println("Send a message to a session ONLY if it can actually receive one, and")
		fmt.Println("verify it was submitted. Built for supervisor/watchdog loops.")
		fmt.Println()
		fmt.Println("Exit status is the contract:")
		fmt.Println("  0  delivered, or skipped because the session is busy (outcome tells which)")
		fmt.Println("  1  not delivered — stalled, undeliverable, or typed but never submitted")
		fmt.Println("  2  session not found")
		fmt.Println()
		fmt.Println("Never discard the exit code. `session send >/dev/null 2>&1` in a watchdog")
		fmt.Println("is how a supervisor ends up reporting nudges it never delivered.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session nudge orchestrator \"resume supervising\"")
		fmt.Println("  if agent-deck session nudge orch \"resume\" --json; then echo ok; else echo escalate; fi")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	needPositionalMessage := *messageFile == ""
	if len(remaining) < 1 || (needPositionalMessage && len(remaining) < 2) {
		fs.Usage()
		out.Error("session and message (or --message-file) are required", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	sessionRef := remaining[0]
	message, err := resolveMessageInput(strings.Join(remaining[1:], " "), *messageFile, os.Stdin)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(sessionRef, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return // unreachable, satisfies staticcheck SA5011
	}

	running := inst.Exists()
	var substate session.Substate
	if running {
		substate = inst.Substate()
	}
	gate := evaluateNudgeGate(running, string(inst.Status), substate, *force)

	switch gate.Action {
	case nudgeActionRefuse:
		out.ErrorWithData(
			fmt.Sprintf("nudge to '%s' refused: %s", inst.Title, gate.Reason),
			gate.Code,
			nudgeFields(inst, substate, nudgeRefused),
		)
		os.Exit(1)
	case nudgeActionSkip:
		fields := nudgeFields(inst, substate, nudgeSkipped)
		fields["delivered"] = false
		out.Success(fmt.Sprintf("No nudge needed for '%s' — %s", inst.Title, gate.Reason), fields)
		return
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.ErrorWithData("could not determine tmux session", ErrCodeInvalidOperation,
			nudgeFields(inst, "", nudgeRefused))
		os.Exit(1)
	}

	// Same verified pipeline as `session send` (composer-draft guard #1409,
	// submit verification #1413, gated-composer Escape+Enter recovery). A
	// nudge is a send with preconditions, never a looser one.
	sendRes, sendErr := executeSend(tmuxSess, inst.Tool, message, false, defaultSendTuning())
	fields := nudgeFields(inst, inst.Substate(), nudgeDelivered)
	for k, v := range sendRes.jsonFields() {
		fields[k] = v
	}
	if sendErr != nil {
		fields["outcome"] = nudgeRefused
		fields["delivered"] = false
		code := ErrCodeInvalidOperation
		if sendRes.delivery == deliveryTypedNotSubmitted {
			code = ErrCodeDeliveryFailed
		}
		out.ErrorWithData(
			fmt.Sprintf("nudge to '%s' was NOT delivered: %v", inst.Title, sendErr),
			code, fields,
		)
		os.Exit(1)
	}

	fields["delivered"] = true
	fields["success"] = true
	out.Success(fmt.Sprintf("Nudged '%s'", inst.Title), fields)
}

// nudgeGate actions.
const (
	nudgeActionSend   = "send"
	nudgeActionSkip   = "skip"
	nudgeActionRefuse = "refuse"
)

// nudgeGate is the precondition verdict for a nudge.
type nudgeGate struct {
	Action string // one of the nudgeAction* constants
	Code   string // CLI error code, set only when Action is refuse
	Reason string // operator-facing explanation
}

// evaluateNudgeGate decides whether a nudge should be sent, silently skipped,
// or refused. Pure so the policy is testable without a live pane — the policy
// IS the feature here, and the bug it replaces was a policy that existed only
// as unreviewed bash.
func evaluateNudgeGate(running bool, status string, substate session.Substate, force bool) nudgeGate {
	// --force is a deliberate operator override: deliver regardless of what
	// the session looks like. It cannot conjure a running pane, though.
	if !running {
		return nudgeGate{
			Action: nudgeActionRefuse,
			Code:   ErrCodeInvalidOperation,
			Reason: "the session is not running — there is no pane to nudge",
		}
	}
	if force {
		return nudgeGate{Action: nudgeActionSend}
	}

	// A gated composer accepts keystrokes and never submits them. Sending into
	// it is exactly the operation that produced hours of phantom nudges, so
	// refuse and say what actually fixes it.
	if substate == session.SubstateStalled {
		return nudgeGate{
			Action: nudgeActionRefuse,
			Code:   ErrCodeSessionStalled,
			Reason: "the session is STALLED — its composer holds unsent text and is not accepting Enter, " +
				"so a nudge cannot reach it. Recover the pane with Escape then Enter (or restart it); " +
				"re-sending only queues more text it cannot submit",
		}
	}

	// Busy is the normal case for a polling supervisor, not a failure: the
	// session is already doing the work a nudge would ask for. Reporting this
	// as an error would train supervisors to ignore the exit code, which is
	// the habit that caused the original incident.
	if isBusyForNudge(status) {
		return nudgeGate{Action: nudgeActionSkip, Reason: "already " + status}
	}

	return nudgeGate{Action: nudgeActionSend}
}

// isBusyForNudge reports whether a status means the session is already working
// and therefore has nothing to be nudged about.
func isBusyForNudge(status string) bool {
	switch strings.ToLower(status) {
	case "running", "active", "starting":
		return true
	default:
		return false
	}
}

// nudgeFields builds the JSON payload shared by every nudge outcome so callers
// can branch on one stable shape regardless of which path was taken.
func nudgeFields(inst *session.Instance, substate session.Substate, outcome string) map[string]interface{} {
	fields := map[string]interface{}{
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"status":        string(inst.Status),
		"outcome":       outcome,
	}
	if substate != "" {
		fields["substate"] = string(substate)
	}
	return fields
}
