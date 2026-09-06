package main

import (
	"errors"
	"fmt"
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
	// reasonTransportNotOptedIn: send_transport is neither "auto" nor an
	// explicit "tmux" pin — an absent key or an unrecognized value. Distinct
	// from reasonConfigPinnedTmux so "the operator pinned tmux" and "nobody
	// opted in" stay legible as different facts (round-2 review of #2100).
	reasonTransportNotOptedIn send.UnavailableReason = "transport_not_opted_in"
	// reasonNoResolver: chooseSendTransport was given no resolve seam and no
	// instance to build one from, so a socket send is not something it can
	// arrange. A caller-shape problem, never a statement about the target.
	reasonNoResolver send.UnavailableReason = "no_resolver"
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
	reasonTransportNotOptedIn: true,
	reasonNoResolver:          true,
}

// transportInputs bundles chooseSendTransport's inputs so it stays a pure,
// table-testable decision function. resolve is a seam: production code
// passes a closure over resolveClaudeSocketTargetForInstance, tests pass a
// stub so resolver branches (dead pid, procStart drift, out-of-tree record,
// ...) don't need real filesystem state or a live process. It takes no
// arguments because resolution is per-INSTANCE, not per-session-ID: the
// config dir and the pane process tree both come from the instance
// (maintainer review of #2100). isSSH is a plain bool (not
// *session.Instance) so this stays decoupled from the Instance type;
// performSend is the one that reads inst.IsSSH().
type transportInputs struct {
	tool            string
	configValue     string
	message         string
	claudeSessionID string
	isSSH           bool
	resolve         func() (send.ClaudeSocketTarget, error)
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
//  2. Anything but a literal send_transport = "auto" takes tmux. The socket
//     is opt-in (maintainer review of #2100), so an explicit "tmux" pin, an
//     absent key and an unrecognized value all land on the historical
//     keystroke path; sendTransportFromConfig has already normalized and
//     warned, and this check is the same rule restated at the decision
//     boundary so a future caller cannot opt a user in by accident. The pin
//     and the not-opted-in cases report different reasons: both are
//     selector-level, but conflating them would describe a default as a
//     decision the operator made.
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
	if in.configValue != "auto" {
		return transportTmux, reasonTransportNotOptedIn, send.ClaudeSocketTarget{}
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
		// No instance to resolve against and no stub: a socket send is not
		// something this function can arrange on its own. Selector-level —
		// no socket was ever attempted, so this must not surface as a
		// fallback_reason (round-2 review of #2100).
		return transportTmux, reasonNoResolver, send.ClaudeSocketTarget{}
	}
	target, err := resolve()
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

// resolveClaudeSocketTargetForInstance is chooseSendTransport's production
// resolve implementation. It resolves against the INSTANCE, not a bare
// session ID: the config dir is the one this instance actually runs under
// (account, conductor, group, profile, worker scratch — not $HOME/.claude,
// which is a different account's records whenever the session selects one),
// and the record is disambiguated by membership in this session's own tmux
// pane process tree rather than by which per-PID file was written most
// recently. Both corrections come from the maintainer review of #2100: the
// old resolver could address a live process belonging to another account or
// another window on the same conversation id.
//
// Every failure here is a pre-write refusal (*send.Unavailable), so the
// caller falls back to tmux exactly as it does for a dead pid. In
// particular a pane-tree probe error is ReasonNotInPaneTree, not a silent
// pass: an incomplete forest cannot prove membership, and guessing is the
// failure mode this function exists to remove.
//
// session.ClaudeSessionRecordsIn returns send.ClaudeSocketRecord (a type
// alias, not a separate struct — see claude_title_reconcile.go), so no
// field-by-field conversion is needed.
func resolveClaudeSocketTargetForInstance(inst *session.Instance) (send.ClaudeSocketTarget, error) {
	if inst == nil {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNoRecord}
	}
	claudeDir := session.ClaudeConfigDirForSend(inst)
	if claudeDir == "" {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNoRecord}
	}

	records := session.ClaudeSessionRecordsIn(claudeDir, inst.ClaudeSessionID)
	if len(records) == 0 {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNoRecord}
	}

	panePIDs, err := inst.PaneProcessTreePIDs()
	if err != nil {
		return send.ClaudeSocketTarget{}, &send.Unavailable{Reason: send.ReasonNotInPaneTree, Err: err}
	}

	rec, err := send.SelectClaudeSocketRecord(records, panePIDs)
	if err != nil {
		return send.ClaudeSocketTarget{}, err
	}
	// Same claudeDir the records came from: liveness, procStart, uid and the
	// key file must all be keyed to the account this instance runs under.
	return send.ResolveClaudeSocketTarget(rec, claudeDir)
}

// loadUserConfigForSend is a seam over session.LoadUserConfig so
// sendTransportFromConfig is table-testable with an injected loader error,
// without touching a real config.toml.
var loadUserConfigForSend = session.LoadUserConfig

// sendTransportFromConfig resolves the send_transport config value for one
// `session send` call. Every uncertain case resolves to "tmux": the socket
// transport is opt-in, so only a literal send_transport = "auto" selects it
// (maintainer review of #2100 — an empty or unreadable config used to select
// the socket, which is not opt-in).
//
// session.LoadUserConfig's own contract: a malformed config.toml returns a
// default config PLUS a non-nil error (it caches the default and the error
// together precisely so every call sees the failure, not just the first one
// after the file changes), so a load error can hide a pin either way and
// reports a one-line warning for the caller to print — unconditionally,
// matching how the existing draftRestoreFailed warning in handleSessionSend
// is not gated by -q/--json either. A non-empty value that is neither "auto"
// nor "tmux" is a typo, not an intent, and gets its own warning naming the
// value so it does not fail silently.
func sendTransportFromConfig() (value string, warn string) {
	cfg, err := loadUserConfigForSend()
	if err != nil {
		return "tmux", fmt.Sprintf("Warning: could not load config.toml (%v); using the tmux transport for this send", err)
	}
	if cfg == nil {
		return "tmux", ""
	}
	if raw := cfg.SendTransport; raw != "" && raw != "auto" && raw != "tmux" {
		return "tmux", fmt.Sprintf("Warning: unknown send_transport %q in config.toml; using the tmux transport for this send", raw)
	}
	return cfg.GetSendTransport(), ""
}

// performSend is the delivery-leg core of handleSessionSend (#2089): decide
// tmux vs. Claude's messaging socket via chooseSendTransport, then execute
// it. hookStatus, resolve and sendFn are seams (all nil in production,
// defaulting to no hook-driven status, resolveClaudeSocketTargetForInstance
// and send.SendOverClaudeSocket) so tests can exercise the
// socket-write-failure / no-tmux-call and explicit-tmux-pin cases without a
// real live Claude process.
//
// resolve is a func(*session.Instance) here rather than the no-argument
// closure chooseSendTransport takes, so tests can stub it without building
// the closure themselves; performSend binds inst into it.
//
// A socket send that fails with *send.Unavailable — discovered only at
// SendOverClaudeSocket time, after chooseSendTransport's own resolve()
// already succeeded (e.g. the target's socket died in the interim, or the
// message turned out to be oversize) — still falls back to tmux: nothing
// was written to the socket in that case, so it is exactly as safe as a
// resolve()-time refusal. Only a *send.CommittedError (a write actually
// started) is a hard failure with no fallback.
//
// On the socket branch it also probes whether the target was mid-turn at the
// moment of the write. A socket write does not interrupt a running turn, so
// the completion that follows belongs to the turn that was already in
// flight, not to this message — the --wait branch uses this to refuse to
// attribute one to the other (maintainer review of #2100).
func performSend(
	inst *session.Instance,
	tmuxTarget sendRetryTarget,
	message string,
	noWait bool,
	tun sendExecTuning,
	sendTransportValue string,
	hookStatus func() (string, error),
	resolve func(*session.Instance) (send.ClaudeSocketTarget, error),
	sendFn func(send.ClaudeSocketTarget, string) (string, error),
) (sendDeliveryResult, error) {
	if resolve == nil {
		resolve = resolveClaudeSocketTargetForInstance
	}
	transport, fallbackReason, target := chooseSendTransport(transportInputs{
		tool:            inst.Tool,
		configValue:     sendTransportValue,
		message:         message,
		claudeSessionID: inst.ClaudeSessionID,
		isSSH:           inst.IsSSH(),
		resolve:         func() (send.ClaudeSocketTarget, error) { return resolve(inst) },
	})
	if transport == transportSocket {
		probe := probeTargetBusy(hookStatus, tmuxTarget)
		res, err := executeSocketSend(target, message, sendFn)
		res.targetBusyAtSend = probe == busyProbeBusy
		res.busyProbeFailed = probe == busyProbeFailed
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

// busyProbeResult is the outcome of the pre-write busy probe. It is
// deliberately tri-state: "we could not tell" is a different fact from "the
// target was generating", and reporting the first as the second puts a
// claim in the operator's --json payload that nothing established (round-2
// review of #2100).
type busyProbeResult int

const (
	// busyProbeIdle: the target was confirmed not mid-turn.
	busyProbeIdle busyProbeResult = iota
	// busyProbeBusy: the target was confirmed mid-turn.
	busyProbeBusy
	// busyProbeFailed: neither status source could be read, so busyness is
	// unknown. Treated like busy for the purpose of declining to wait — an
	// unverifiable target cannot be given a verified completion — but
	// reported as its own outcome, never as confirmed busy.
	busyProbeFailed
)

// probeTargetBusy reads the target's status immediately before a socket
// write and classifies it.
//
// It reads the SAME signal --defer-if-busy holds on (fetchHookDrivenStatus,
// i.e. Claude's UserPromptSubmit/Stop hook edges, classified by
// send.StatusIsBusy) rather than the tmux pane heuristic. That matters:
// #1578 chose the hook signal precisely because the pane-content heuristic
// false-positives to idle during tool calls and thinking pauses, so probing
// the pane here would have told --wait the target was free at exactly the
// moments it was not. The two paths now share one notion of busy — a target
// --defer-if-busy would hold for is a target --wait refuses to attribute a
// completion to (round-2 review of #2100).
//
// The tmux pane status is the fallback for targets the hook signal cannot
// speak for (non-hook tools, an absent or stale hook file, a failed load),
// and only when BOTH are unreadable is the result busyProbeFailed.
//
// Residual race, not closed by this probe: a turn that starts in the window
// between the probe returning idle and the write landing in the inbox is
// still misattributed, because --wait would then observe that turn's
// completion and report it as this message's. The probe narrows the window;
// only an in-band receipt keyed to the message id could close it, and
// Claude's inbox provides none (see the #2089 CommittedError note).
//
// hookStatus is a seam (nil in tests that only exercise the pane fallback,
// and in any caller with no session reference to resolve).
func probeTargetBusy(hookStatus func() (string, error), paneChecker statusChecker) busyProbeResult {
	if hookStatus != nil {
		if status, err := hookStatus(); err == nil && status != "" {
			return classifyBusyStatus(status)
		}
	}
	if paneChecker != nil {
		if status, err := paneChecker.GetStatus(); err == nil && status != "" {
			return classifyBusyStatus(status)
		}
	}
	return busyProbeFailed
}

// classifyBusyStatus maps a status string from either source onto the
// probe's idle/busy split. It accepts both vocabularies because
// fetchHookDrivenStatus itself falls through to the list --json pipeline
// when no fresh hook edge exists: send.StatusIsBusy covers the hook words
// ("running", "starting") and "active" is the pane/list word for the same
// state.
func classifyBusyStatus(status string) busyProbeResult {
	if send.StatusIsBusy(status) || status == "active" {
		return busyProbeBusy
	}
	return busyProbeIdle
}

// skippedWaitOutcome returns the `wait_outcome` value for a send whose
// `--wait` must not wait, or "" when --wait proceeds normally. Only the
// socket transport can hit it: a socket write lands in the target's inbox
// without interrupting a running turn, so a target that was already mid-turn
// will finish THAT turn next, and attributing it to this message would be
// wrong. The tmux path submits into the composer and is unaffected, so it
// never consults the probe (maintainer review of #2100).
//
// A failed probe declines to wait for the same reason but reports a
// different outcome: nothing established that the target was generating, so
// saying it was would be a fabricated fact (round-2 review of #2100).
func skippedWaitOutcome(res sendDeliveryResult) string {
	if res.transport != "socket" {
		return ""
	}
	switch {
	case res.busyProbeFailed:
		return waitOutcomeUnverifiedBusyProbeFailed
	case res.targetBusyAtSend:
		return waitOutcomeUnverifiedBusyTarget
	}
	return ""
}

// shouldSkipWaitForBusyTarget reports whether `--wait` must decline to wait
// for this send's completion — the boolean face of skippedWaitOutcome.
func shouldSkipWaitForBusyTarget(res sendDeliveryResult) bool {
	return skippedWaitOutcome(res) != ""
}
