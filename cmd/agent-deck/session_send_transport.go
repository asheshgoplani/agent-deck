package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sendTransport is which delivery mechanism `session send` uses for one
// send: the historical tmux keystroke path, or Claude Code's own messaging
// socket (#2089).
type sendTransport string

const (
	transportTmux   sendTransport = "tmux"
	transportSocket sendTransport = "socket"
)

// Selector-level reasons a send never even reached socket resolution. These
// are deliberately NOT surfaced as fallback_reason in the --json payload
// (see chooseSendTransport doc comment): fallback_reason means "a socket was
// possible but became unavailable", and none of these cases were ever a
// candidate for a socket in the first place — pinning tmux explicitly is not
// a fallback, and neither is a target that structurally can't take a socket
// send. They exist so callers/tests can still see WHY, even though the CLI's
// --json contract only surfaces a reason for a genuine resolve()/write-time
// refusal.
const (
	reasonConfigPinnedTmux    send.UnavailableReason = "config_pinned_tmux"
	reasonRemoteSession       send.UnavailableReason = "remote_session"
	reasonNotClaudeCompatible send.UnavailableReason = "not_claude_compatible"
	reasonSlashCommand        send.UnavailableReason = "slash_command"
	reasonNoClaudeSessionID   send.UnavailableReason = "no_claude_session_id"
)

// selectorLevelReasons is exactly the set above: reasons that do NOT mean "a
// socket was attempted and refused". runTmuxSend uses this to decide what
// actually reaches sendDeliveryResult.fallbackReason (and therefore the
// --json fallback_reason field) — see its doc comment.
var selectorLevelReasons = map[send.UnavailableReason]bool{
	reasonConfigPinnedTmux:    true,
	reasonRemoteSession:       true,
	reasonNotClaudeCompatible: true,
	reasonSlashCommand:        true,
	reasonNoClaudeSessionID:   true,
}

// transportInputs bundles chooseSendTransport's inputs so it stays a pure,
// table-testable decision function. resolve is a seam: production code
// passes resolveClaudeSocketTargetForSession, tests pass a stub so resolver
// branches (dead pid, procStart drift, ...) don't need real filesystem state
// or a live process. isSSH is a plain bool (not *session.Instance) so this
// stays decoupled from the Instance type; performSend is the one that reads
// inst.IsSSH().
type transportInputs struct {
	tool            string
	configValue     string
	message         string
	claudeSessionID string
	isSSH           bool
	resolve         func(sessionID string) (send.ClaudeSocketTarget, error)
}

// isBareSlashCommand reports whether message, after trimming leading
// whitespace, starts with "/" — the same shape as shouldGateSlashRegistration
// (session_cmd.go:4043), generalized off that function's hardcoded
// tool != "claude" check: chooseSendTransport already establishes
// Claude-compatibility before this check runs, so the predicate itself only
// needs to look at the message.
func isBareSlashCommand(message string) bool {
	trimmed := strings.TrimLeft(message, " \t")
	return trimmed != "" && strings.HasPrefix(trimmed, "/")
}

// chooseSendTransport is the pure decision half of the #2089 socket-send
// feature: given everything relevant about one send, decide tmux or socket,
// why, and — on a socket decision — the already-vetted target to send to
// (so callers resolve exactly once per send; see performSend). It never
// touches the filesystem or a live process directly — all of that lives
// behind the resolve seam — which is what makes every branch here
// table-testable without a real Claude session.
//
// --draft never reaches this function: handleSessionSend returns before
// calling performSend when --draft is set (session_cmd.go's --draft branch
// pre-fills the composer and returns), so there is no draft case to handle
// here.
//
// Decision order (cheapest / most certain first):
//  1. An SSH-backed instance always takes tmux. The messaging socket is a
//     Unix domain socket that must be dialed on the machine that owns it
//     (plan §4: remote/cross-machine sessions are out of scope); resolving a
//     local ~/.claude record for a remote instance's claudeSessionID would
//     be a coincidental collision at best, not the target the operator means.
//  2. An explicit send_transport = "tmux" pin always wins, no exceptions.
//  3. Non-Claude-compatible tools have no Claude messaging socket at all.
//  4. A bare slash command (e.g. "/compact") arrives over the socket as
//     literal text (skipSlashCommands=true is baked into the receiver —
//     §1.7.1 of the plan, verified against the 2.1.259 binary), so routing
//     it to tmux is the only way it still executes as a command.
//  5. No known Claude session ID for the target means there is nothing to
//     resolve a socket record for.
//  6. Otherwise, ask resolve(). A *send.Unavailable here is the only case
//     that reports a non-empty reason: every case above means a socket was
//     never a candidate to begin with, not that one was tried and refused.
func chooseSendTransport(in transportInputs) (sendTransport, send.UnavailableReason, send.ClaudeSocketTarget) {
	if in.isSSH {
		return transportTmux, reasonRemoteSession, send.ClaudeSocketTarget{}
	}
	if in.configValue == "tmux" {
		return transportTmux, reasonConfigPinnedTmux, send.ClaudeSocketTarget{}
	}
	if !session.IsClaudeCompatible(in.tool) {
		return transportTmux, reasonNotClaudeCompatible, send.ClaudeSocketTarget{}
	}
	if isBareSlashCommand(in.message) {
		return transportTmux, reasonSlashCommand, send.ClaudeSocketTarget{}
	}
	if in.claudeSessionID == "" {
		return transportTmux, reasonNoClaudeSessionID, send.ClaudeSocketTarget{}
	}
	resolve := in.resolve
	if resolve == nil {
		resolve = resolveClaudeSocketTargetForSession
	}
	target, err := resolve(in.claudeSessionID)
	if err != nil {
		var unavail *send.Unavailable
		if errors.As(err, &unavail) {
			return transportTmux, unavail.Reason, send.ClaudeSocketTarget{}
		}
		// A non-Unavailable error from resolve() (should not happen given
		// its contract) still fails safe to tmux, with no fabricated reason.
		return transportTmux, "", send.ClaudeSocketTarget{}
	}
	return transportSocket, "", target
}

// resolveClaudeSocketTargetForSession is chooseSendTransport's production
// resolve implementation: look up the session record for sessionID under
// the real ~/.claude and vet it. session.ClaudeSessionRecordFor already
// returns send.ClaudeSocketRecord (a type alias, not a separate struct — see
// claude_title_reconcile.go), so no field-by-field conversion is needed.
func resolveClaudeSocketTargetForSession(sessionID string) (send.ClaudeSocketTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNoRecord, Err: err}
	}
	claudeDir := filepath.Join(home, ".claude")

	rec, ok := session.ClaudeSessionRecordFor(sessionID)
	if !ok {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNoRecord}
	}
	return send.ResolveClaudeSocketTarget(rec, claudeDir)
}

// loadUserConfigForSend is a seam over session.LoadUserConfig so
// sendTransportFromConfig is table-testable with an injected loader error,
// without touching a real config.toml.
var loadUserConfigForSend = session.LoadUserConfig

// sendTransportFromConfig resolves the send_transport config value for one
// `session send` call. session.LoadUserConfig's own contract: a malformed
// config.toml returns a default config PLUS a non-nil error (it caches the
// default and the error together precisely so every call sees the failure,
// not just the first one after the file changes). Silently falling through
// to "auto" on that error would mean a user who pinned
// send_transport = "tmux" and later made an unrelated typo elsewhere in the
// file would silently get the socket transport instead of the pin they set.
// Conservative on error: report "tmux" and a one-line warning for the
// caller to print — unconditionally, matching how the existing
// draftRestoreFailed warning in handleSessionSend is not gated by -q/--json
// either.
func sendTransportFromConfig() (value string, warn string) {
	cfg, err := loadUserConfigForSend()
	if err != nil {
		return "tmux", fmt.Sprintf("Warning: could not load config.toml (%v); using the tmux transport for this send", err)
	}
	if cfg == nil {
		return "auto", ""
	}
	return cfg.GetSendTransport(), ""
}

// performSend is the delivery-leg core of handleSessionSend (#2089): decide
// tmux vs. Claude's messaging socket via chooseSendTransport, then execute
// it. resolve and sendFn are seams (both nil in production, defaulting to
// resolveClaudeSocketTargetForSession and send.SendOverClaudeSocket) so
// tests can exercise the socket-write-failure / no-tmux-call and
// explicit-tmux-pin cases without a real live Claude process.
//
// A socket send that fails with *send.Unavailable — discovered only at
// SendOverClaudeSocket time, after chooseSendTransport's own resolve()
// already succeeded (e.g. the target's socket died in the interim, or the
// message turned out to be oversize) — still falls back to tmux: nothing
// was written to the socket in that case, so it is exactly as safe as a
// resolve()-time refusal. Only a *send.CommittedError (a write actually
// started) is a hard failure with no fallback.
func performSend(
	inst *session.Instance,
	tmuxTarget sendRetryTarget,
	message string,
	noWait bool,
	tun sendExecTuning,
	sendTransportValue string,
	resolve func(string) (send.ClaudeSocketTarget, error),
	sendFn func(send.ClaudeSocketTarget, string) (string, error),
) (sendDeliveryResult, error) {
	transport, fallbackReason, target := chooseSendTransport(transportInputs{
		tool:            inst.Tool,
		configValue:     sendTransportValue,
		message:         message,
		claudeSessionID: inst.ClaudeSessionID,
		isSSH:           inst.IsSSH(),
		resolve:         resolve,
	})
	if transport == transportSocket {
		res, err := executeSocketSend(target, message, sendFn)
		if err != nil {
			var unavail *send.Unavailable
			if errors.As(err, &unavail) {
				return runTmuxSend(tmuxTarget, inst, message, noWait, tun, unavail.Reason)
			}
		}
		return res, err
	}
	return runTmuxSend(tmuxTarget, inst, message, noWait, tun, fallbackReason)
}

// runTmuxSend executes the tmux keystroke path and stamps the result with
// transport="tmux" and, when reason is a genuine socket-refusal (not a
// selector-level reason — see selectorLevelReasons), fallback_reason.
func runTmuxSend(
	tmuxTarget sendRetryTarget,
	inst *session.Instance,
	message string,
	noWait bool,
	tun sendExecTuning,
	reason send.UnavailableReason,
) (sendDeliveryResult, error) {
	res, err := executeSend(tmuxTarget, inst.Tool, message, noWait, tun)
	res.transport = "tmux"
	// Only a genuine "socket was attempted and refused" reason is a fallback
	// reason. A selector-level reason (an explicit pin, a non-Claude tool, a
	// slash command, no known session ID) means a socket was never a
	// candidate to begin with — reporting one of those as fallback_reason
	// would misdescribe an explicit pin as a fallback (plan §3 Step 5 / Step
	// 6 test: "an explicit pin is not a fallback").
	if !selectorLevelReasons[reason] {
		res.fallbackReason = reason
	}
	return res, err
}

// executeSocketSend delivers message to an already-resolved Claude Code
// messaging-socket target (#2089), bypassing the tmux pane and composer
// entirely. target comes from chooseSendTransport, which is the only
// resolve() call for this send (NIT: one resolve per send, not two).
func executeSocketSend(
	target send.ClaudeSocketTarget,
	message string,
	sendFn func(send.ClaudeSocketTarget, string) (string, error),
) (sendDeliveryResult, error) {
	if sendFn == nil {
		sendFn = send.SendOverClaudeSocket
	}
	msgID, err := sendFn(target, message)
	if err != nil {
		var unavail *send.Unavailable
		if errors.As(err, &unavail) {
			// Pre-write refusal discovered at send time. Leave delivery
			// unset: the caller (performSend) falls back to tmux and that
			// path sets its own delivery status.
			return sendDeliveryResult{transport: "socket"}, err
		}
		return sendDeliveryResult{delivery: deliverySocketWriteFailed, transport: "socket"}, err
	}
	return sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", socketMsgID: msgID}, nil
}
