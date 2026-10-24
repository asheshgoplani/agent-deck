package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// fetchHookDrivenStatus reloads the target from storage and reports the same
// hook-driven status string that `agent-deck list --json` shows. `session send
// --defer-if-busy` polls this so its hold gate keys off the turn-finished
// Stop-hook signal (a true edge) rather than WaitForAgentReady's pane-diff
// readiness heuristic, which false-positives to idle during tool calls and
// thinking pauses (#1578).
//
// It mirrors handleList's status pipeline exactly: reload -> warm caches +
// cold-load hook files via RefreshInstancesForCLIStatus -> UpdateStatus. The
// reload each poll is deliberate: a fresh OS process has no StatusFileWatcher,
// so the only way to observe the target's newest hook edge is to re-read it
// from disk.
func fetchHookDrivenStatus(profile, sessionRef string) (string, error) {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return "", err
	}
	inst, errMsg, _ := ResolveSession(sessionRef, instances)
	if inst == nil {
		return "", fmt.Errorf("%s", errMsg)
	}
	// Cold-load the on-disk hook file into the instance — a fresh CLI process
	// has no StatusFileWatcher, so the target's newest hook edge only reaches us
	// by re-reading it from disk each poll.
	session.RefreshInstancesForCLIStatus([]*session.Instance{inst})
	// Prefer the FRESH hook-driven signal. It is the true turn-finished edge
	// (Claude's UserPromptSubmit hook -> "running", Stop hook -> "waiting") and,
	// unlike UpdateStatus, is not gated on a live tmux handle — exactly the
	// property #1578 needs so the hold gate keys off "turn finished" rather than
	// a pane-diff heuristic. Fall back to the full list --json pipeline when no
	// fresh hook signal exists (non-hook tools, or a stale/absent hook file).
	if hs, fresh := inst.GetHookStatus(); fresh && hs != "" {
		return hs, nil
	}
	_ = inst.UpdateStatus()
	return StatusString(inst.Status), nil
}

// hookDrivenBusy reports the target's FRESH hook-driven busy state as
// (busy, known), re-reading the hook file from disk on every call — the
// sending CLI is a fresh OS process with no StatusFileWatcher. known is false
// when no fresh hook signal exists (non-hook tools, or a stale or absent hook
// file), in which case the caller must fall back to its own evidence rather
// than treat silence as idle (issues #1978, #2033). It deliberately does not
// fall through to UpdateStatus the way fetchHookDrivenStatus does: that is
// the spike-filtered heuristic whose decay to idle is the very thing this
// signal exists to override.
func hookDrivenBusy(inst *session.Instance) (busy, known bool) {
	if inst == nil {
		return false, false
	}
	session.ReloadHookStatus(inst)
	hs, fresh := inst.GetHookStatus()
	if !fresh || hs == "" {
		return false, false
	}
	return send.StatusIsBusy(hs), true
}

// handleSessionSend sends a message to a running session
// Waits for the agent to be ready before sending (Claude, Gemini, etc.)
func handleSessionSend(profile string, args []string) {
	fs := flag.NewFlagSet("session send", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode: nothing on a confirmed delivery; one stderr line when delivery is unconfirmed or queued; errors as usual")
	noWait := fs.Bool("no-wait", false, "Don't wait for agent to be ready (send immediately)")
	wait := fs.Bool("wait", false, "Block until agent finishes processing, then print output (Claude socket waits share --timeout between observing a fresh turn and its completed reply; returns immediately with wait_outcome=unverified_busy_target/unverified_busy_probe_failed if the target could not be shown idle)")
	stream := fs.Bool("stream", false, "Stream JSONL events (Claude only) to stdout instead of returning a snapshot")
	draft := fs.Bool("draft", false, "Pre-fill the prompt without submitting (incompatible with --wait/--stream/--no-wait)")
	messageFile := fs.String("message-file", "", "Read the message from a file ('-' for stdin) instead of a positional argument; avoids shell quoting of long prompts")
	deferIfBusy := fs.Bool("defer-if-busy", false, "Hold delivery until the target is idle (turn-finished, hook-driven) instead of interrupting a mid-generation turn (incompatible with --no-wait)")
	deferTimeout := fs.Duration("defer-timeout", 30*time.Minute, "Max time --defer-if-busy holds a busy target before dropping the message with a non-zero exit")
	timeout := fs.Duration("timeout", sessionSendDefaultTimeout, "Max time to wait for the agent to become ready, and separately (with --wait/--stream) one shared budget for the message's turn to start, finish, and its reply to be read")
	streamIdle := fs.Duration("stream-idle", 10*time.Second, "Max idle time before --stream aborts with error")
	streamCharBudget := fs.Int("stream-char-budget", 4000, "Char budget for text flush in --stream mode")
	streamToolBudget := fs.Int("stream-tool-budget", 3, "Tool-event budget for text flush in --stream mode")
	codexComposerFallback := fs.Bool("codex-composer-fallback", false, "Codex only: when the session's Codex identity is provably unavailable (fresh composer, rollout re-created after the trust prompt), send through the verified composer path instead of refusing. Never used for --json --wait; every other acceptance error still refuses")
	queue := fs.Bool("queue", false, "Return at once with a send_id; a background worker delivers when the target is idle, at most once; every send ends landed, failed or settled with a reason (see session send-status)")
	queueWorker := fs.Bool("queue-worker", false, "Internal: deliver a durable queued send directly")
	ignoreBudget := fs.Bool("ignore-budget", false, ignoreBudgetFlagHelp)
	noTag := fs.Bool("no-tag", false, "Do not prefix the [agent-deck from:<id>] envelope a send from inside a session gets by default ([send] tag_sends)")
	requireInputPrompt := fs.Bool("require-input-prompt", false, "Refuse with menu_open unless an input prompt is visible immediately before each keystroke batch")
	sourceFlag := fs.String("source", "", sendSourceFlagHelp)
	var images imageList
	fs.Var(&images, "image", "Attach an image (repeatable): Claude Code and Gemini get @<copy under .agentdeck-images/>; Codex and other harnesses exit 2")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session send <id|title> <message> [options]")
		fmt.Println()
		fmt.Println("Send a message to a running session.")
		fmt.Println()
		fmt.Println("Claude targets: sends type into the pane by default. Set")
		fmt.Println("send_transport = \"auto\" in config.toml to opt in to writing directly to")
		fmt.Println("the target's Claude Code messaging socket (peerProtocol 1) when it has")
		fmt.Println("one. A bare slash command (e.g. \"/compact\") always uses")
		fmt.Println("tmux, since Claude's socket path renders slash commands as literal text.")
		fmt.Println("SSH-backed (remote) targets always use tmux too: the messaging socket must")
		fmt.Println("be dialed on the machine that owns it.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session send my-project \"Summarize recent changes\"")
		fmt.Println("  agent-deck session send my-project \"run tests\" --wait")
		fmt.Println("  agent-deck session send my-project \"quick ping\" --no-wait")
		fmt.Println("  agent-deck session send my-project \"trace progress\" --stream")
		fmt.Println("  agent-deck session send my-project \"cwd: /path/to/dir\" --draft")
		fmt.Println("  agent-deck session send my-project --message-file answer.md   # long reply from file")
		fmt.Println("  git diff | agent-deck session send my-project --message-file -   # message from stdin")
		fmt.Println("  agent-deck session send parent \"child done\" --defer-if-busy --defer-timeout 30m")
		fmt.Println("  agent-deck session send my-project --message-file - --json   # returns send_id at once")
		fmt.Println("  agent-deck session send my-project \"what is this?\" --image shot.png")
		fmt.Println("  agent-deck session send my-project --no-wait -- \"-v is the message\"   # text after -- is never a flag")
		fmt.Println()
		fmt.Println("--json (without --wait/--stream/--draft/--no-wait) and --queue return a durable")
		fmt.Println("  send_id with verdict queued. Claude accepts input while busy; other harnesses wait")
		fmt.Println("  for idle. send-status and delivery events upgrade the verdict when evidence arrives.")
		fmt.Println("  The send is watched until its text lands in")
		fmt.Println("  the transcript (state landed, landed_row_id). A refusal before typing (composer_blocked,")
		fmt.Println("  target_busy) is retried with a doubling wait capped at 1m; after the 30m budget the send")
		fmt.Println("  fails with a reason, and a sender session gets the failure in its inbox.")
		fmt.Println("  Exit 0 queued, 1 failed at once (e.g. target not running).")
		fmt.Println("From inside an agent-deck session, a send to a Claude target starts with one")
		fmt.Println("  [agent-deck from:<your session id>] line so the reply is routed back to you")
		fmt.Println("  (urgent in your inbox). --no-tag or [send] tag_sends = false turns it off;")
		fmt.Println("  slash commands, --draft and heartbeats are never tagged. --json reports tagged.")
		fmt.Println("--image: Claude Code and Gemini receive @path; Codex takes images only at launch (-i), so a")
		fmt.Println("  running Codex session exits 2, as does any other harness. Exit codes: 0 sent/queued,")
		fmt.Println("  1 delivery failed, 2 usage error, unknown session or unsupported image.")
		fmt.Println()
		fmt.Println("Codex --json --wait:")
		fmt.Println("  Emits one structured result correlated to the accepted Codex turn.")
		fmt.Println("  Requires a locally readable exact accepted-turn receipt; remote or sandboxed targets are refused.")
		fmt.Println("  Local agent-deck sends are serialized; direct pane or keyboard input is outside this guarantee.")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	needPositionalMessage := *messageFile == ""
	if len(remaining) < 1 || (needPositionalMessage && len(remaining) < 2) {
		fs.Usage()
		out.Error("session and message (or --message-file) are required", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if *stream && *wait {
		out.Error("--stream and --wait are mutually exclusive", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if *draft && (*wait || *stream || *noWait) {
		out.Error("--draft is incompatible with --wait, --stream, and --no-wait", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// #1578: --defer-if-busy holds delivery until the target is turn-finished;
	// --no-wait fires immediately. They are opposites.
	if *deferIfBusy && *noWait {
		out.Error("--defer-if-busy is incompatible with --no-wait", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	sessionRef := remaining[0]
	message, err := resolveMessageInput(strings.Join(remaining[1:], " "), *messageFile, os.Stdin)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// Load sessions
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}

	// Triggers (#2702): every send is a fire of its source. The queue
	// worker delivers a send whose fire was recorded when it was queued.
	var fire *sendTriggerFire
	if !*queueWorker {
		fireSender := sendSenderID()
		if *noTag {
			fireSender = "" // --no-tag: never attributed to an inherited session
		}
		fire = newSendTriggerFire(storage.GetDB(), *sourceFlag, fireSender, sessionRef, profile, message)
		cliExitHook = fire.onExit
	}

	// Resolve session
	inst, errMsg, errCode := ResolveSession(sessionRef, instances)
	if inst == nil {
		if errCode == ErrCodeNotFound && fire != nil {
			fire.exitOutcome = triggers.OutcomeDeadTarget
		}
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
		return // unreachable, satisfies staticcheck SA5011
	}
	if fire != nil {
		fire.sessionID = inst.ID
		// A muted trigger's send is accepted and logged, never typed. The
		// host job that sent it keeps running.
		if until, mutedID := fire.mutedUntil(); !until.IsZero() {
			fire.record(triggers.OutcomeMuted)
			summary := fmt.Sprintf("Not delivered to '%s': %s is muted until %s (logged as muted)", inst.Title, mutedID, until.Local().Format("2006-01-02 15:04"))
			mutedData := map[string]interface{}{
				"success":       true,
				"muted":         true,
				"muted_until":   until.UTC(),
				"trigger_id":    mutedID,
				"session_id":    inst.ID,
				"session_title": inst.Title,
			}
			if *wait || *stream {
				// A waiting caller (the chat bridge) gets a complete reply
				// that says why nothing ran, not a "send failed".
				mutedData["completion"] = "complete"
				mutedData["content"] = summary
			}
			out.Success(summary, mutedData)
			out.QuietNotice(summary)
			return
		}
	}

	// Machine callers get a durable id immediately. The worker opts out of
	// this branch so its own JSON result describes the actual transport.
	asyncJSON := *jsonOutput && !*queueWorker && !*wait && !*stream && !*draft && !*deferIfBusy && !*noWait
	// Opt-in telemetry: count the send by tool and length bucket only (no-op
	// without consent), once it was queued or delivered. A send from inside
	// a session is automation; the queue worker's delivery was counted when
	// the message was queued.
	messageChars := utf8.RuneCountInString(message)
	recordSent := func() {
		if *queueWorker {
			return
		}
		via := telemetry.SendCLI
		if telemetry.InsideSession() {
			via = telemetry.SendConductor
		}
		telemetry.MessageSent(inst.Tool, via, messageChars, *queue)
	}
	// PR5 of the comms redesign: tag an agent-originated send with the
	// sender's id. The queue worker delivers a message tagged when queued.
	senderID := sendSenderID()
	message, tagged := tagSendMessage(message, sendTagInputs{
		senderID:   senderID,
		senderTool: sendSenderTool(senderID, instances),
		targetID:   inst.ID,
		targetTool: inst.Tool,
		enabled:    !*noTag && !*queueWorker && sendTagsEnabled(),
		draft:      *draft,
	})
	if *queue && (*wait || *stream || *draft || *noWait || *deferIfBusy) {
		out.Error("--queue is incompatible with --wait, --stream, --draft, --no-wait and --defer-if-busy", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	// Cost budgets (prime-agent adoption C9): with [costs.budgets]
	// enforce = true, a send is refused before anything is queued or typed
	// when a limit that applies to the target is spent. A draft only
	// pre-fills the prompt; the queue worker's child was checked by the
	// worker itself; --ignore-budget is for messages a person starts.
	if !*queueWorker && !*draft && !*ignoreBudget {
		enforceSendBudget(profile, "send", storage, inst, instances, out, *quiet, nil)
	}
	if len(images) > 0 || *queue || asyncJSON {
		var imgErr error
		var copies []string
		message, copies, imgErr = attachImages(inst, message, images, time.Now())
		if imgErr != nil {
			out.Error(imgErr.Error(), ErrCodeInvalidOperation)
			exitCLI(2)
		}
		if *queue || asyncJSON {
			if err := inst.PromptDeliveryError(); err != nil {
				out.Error(err.Error(), ErrCodeInvalidOperation)
				exitCLI(1)
			}
			ledgerSender := senderID
			if *noTag {
				ledgerSender = "" // --no-tag: never attributed to an inherited session
			}
			queueSend(profile, storage, inst, message, copies, tagged, ledgerSender, *requireInputPrompt, *ignoreBudget, out) // exits on failure
			recordSent()
			fire.record(triggers.OutcomeQueued)
			return
		}
	}

	// --stream is Claude-only in Phase 1. Non-Claude tools error cleanly
	// with a stable message so the CLI contract stays legible.
	if *stream {
		if msg := streamPreconditionError(inst.Tool); msg != "" {
			out.Error(msg, ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Check if session is running
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// PR #1942 review (P1a): refuse a send the target cannot receive. A DeepSeek
	// web-profile pane runs an HTTP server with no terminal prompt, so keystrokes
	// go to the server process's stdin and vanish while this command reports
	// success. Silent message loss is the worst failure class here, so it is a
	// hard refusal rather than a warning. Every other tool returns nil.
	if err := inst.PromptDeliveryError(); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}

	if shouldSkipConductorHeartbeatSend(inst, message) {
		fire.skip()
		out.Success(fmt.Sprintf("Skipped heartbeat for '%s'", inst.Title), map[string]interface{}{
			"success":       true,
			"skipped":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
			"tagged":        tagged,
		})
		return
	}

	// Get tmux session
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error("could not determine tmux session", ErrCodeInvalidOperation)
		exitCLI(1)
	}

	// #1578: --defer-if-busy holds delivery until the target is turn-finished.
	// Runs BEFORE WaitForAgentReady + the composer-draft Ctrl+C guard, so a
	// mid-generation target is never interrupted. Keys off the hook-driven
	// status (the same turn-finished signal `list --json` reports), not the
	// pane-diff readiness heuristic that false-positives idle mid-turn.
	if *deferIfBusy {
		if err := send.WaitUntilNotBusy(func() (string, error) {
			return fetchHookDrivenStatus(profile, sessionRef)
		}, *deferTimeout, send.DeferPollInterval, time.Sleep); err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}

	// Every local Codex send participates in the same durable acceptance
	// protocol, whether structured or human and whether it waits for completion
	// or not. Structured remote/sandbox waits still fail closed; ordinary remote
	// sends keep their legacy transport and never read host-local marker state.
	structuredCodexWait := *jsonOutput && *wait && session.IsCodexCompatible(inst.Tool)
	acceptanceFence := codexAcceptanceFence{}
	var acceptanceGuard *codexAcceptanceGuard
	if shouldAcquireCodexAcceptanceGuard(inst, *jsonOutput, *wait, *draft) {
		// Every send keeps the double-send refusal: an unresolved earlier
		// submission, an identity owned by another session, a held
		// acceptance lock or a remote rollout all refuse, whatever the
		// flags. Only --codex-composer-fallback, and only when the identity
		// is provably unavailable (macapp-core-needs §3: right after the
		// trust prompt there is no identity, or the stored id has no
		// rollout), sends through the verified composer path instead.
		guardErr := hydrateLegacyCodexIdentity(inst, instances, storage)
		if guardErr == nil {
			acceptanceGuard, guardErr = acquireCodexAcceptanceGuard(inst, codexAcceptanceLockWait(*timeout))
		}
		switch {
		case guardErr == nil:
			acceptanceFence = acceptanceGuard.fence
		case codexComposerFallbackAllowed(guardErr, *codexComposerFallback, structuredCodexWait):
			acceptanceGuard = nil
			fmt.Fprintf(os.Stderr, "Note: no Codex accepted-turn receipt yet (%v); sending through the composer (--codex-composer-fallback)\n", guardErr)
		default:
			out.ErrorWithData(fmt.Sprintf("cannot establish exact Codex turn acceptance: %v", guardErr), ErrCodeInvalidOperation,
				codexAcceptanceRefusalData(guardErr))
			exitCLI(1)
		}
	}

	// Wait for agent to be ready (unless --no-wait is specified).
	// Issue #957: honor --timeout for the readiness phase too, not just the
	// post-ready completion wait. Otherwise --timeout 5m against a busy
	// recipient silently fails at ~80s.
	busyAcceptsInput := !*wait && session.AcceptsInputWhileBusy(inst.Tool)
	if busyAcceptsInput {
		status, statusErr := fetchHookDrivenStatus(profile, inst.ID)
		busyAcceptsInput = statusErr == nil && status == "running"
	}
	if !*noWait && !busyAcceptsInput {
		if err := send.WaitForAgentReady(tmuxSess, inst.Tool, *timeout, send.PromptGates{
			ClaudeComposer: session.IsClaudeCompatible(inst.Tool),
			CodexPrompt:    session.IsCodexCompatible(inst.Tool),
		}); err != nil {
			if acceptanceGuard != nil {
				acceptanceGuard.Release()
			}
			telemetry.ErrorOccurred(telemetry.AreaSend, telemetry.KindTimeout, inst.Tool)
			out.Error(fmt.Sprintf("timeout waiting for agent: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		// Issue #966: after a restart, Claude reaches "waiting" + composer
		// visible before its slash-command parser registers. Bare `/foo`
		// in that window is silently dropped. Hold back only when needed.
		if shouldGateSlashRegistration(inst.Tool, message) {
			slashTimeout := *timeout
			if slashTimeout <= 0 || slashTimeout > 10*time.Second {
				slashTimeout = 10 * time.Second
			}
			if err := waitForSlashCommandReady(tmuxSess, inst.Tool, slashTimeout); err != nil {
				if acceptanceGuard != nil {
					acceptanceGuard.Release()
				}
				out.Error(fmt.Sprintf("timeout waiting for slash-command registration: %v", err), ErrCodeInvalidOperation)
				exitCLI(1)
			}
		}
	}

	// Record send time before the actual send. It stamps the last-sent
	// self-heal clock and is the NotBefore guard for turn identity below; the
	// reply for a Claude --wait/--stream is selected by durable turn identity,
	// never by comparing this timestamp against transcript records.
	sentAt := time.Now()
	var turnPath string
	var turnCursor int64
	// trackTurn: every Claude send with a real prompt reads its own turn
	// record out of the transcript — as the verification loop's authoritative
	// submission signal, and as the reply binding for --wait/--stream.
	trackTurn := sendTracksTurn(inst.Tool, message)
	useTurnIdentity := trackTurn && (*wait || *stream)
	if trackTurn {
		if fresh := inst.GetSessionIDFromTmux(); fresh != "" {
			inst.ClaudeSessionID = fresh
			// #1815: own pane env — weak vouch.
			session.NoteClaudeSessionIDFromOwnPane(inst)
			inst.ClaudeDetectedAt = time.Now()
		}
		var pathErr error
		turnPath, pathErr = inst.GetJSONLPathChecked(instances)
		if pathErr != nil {
			// #1400: a colliding transcript is refused before anything is
			// typed, exactly as the legacy freshness path refused it.
			failSessionSend(out, *stream, fmt.Sprintf("cannot establish turn identity: %v", pathErr), nil)
		}
		// turnPath == "" here is a fresh session whose transcript Claude has
		// not written yet (or a session ID not yet visible). For --wait and
		// --stream the path is resolved after the send and searched from
		// offset 0 under the exact NotBefore guard; refusing to send would
		// break --wait on every first message to a new session.
		if turnPath != "" {
			turnCursor, pathErr = session.TranscriptCursor(turnPath)
			if pathErr != nil {
				failSessionSend(out, *stream, fmt.Sprintf("cannot capture turn identity cursor: %v", pathErr), nil)
			}
		}
	}
	// pathKnownBeforeSend decides the identity guard: a pre-send cursor is
	// proof by position, so no timestamp is needed; a path learned only after
	// the send is searched from offset 0 and every candidate must carry a
	// timestamp at or after sentAt.
	pathKnownBeforeSend := turnPath != ""

	// --draft: type text into the prompt without pressing Enter, letting the
	// user review and submit manually.
	if *draft {
		fire.skip() // a pre-fill is not a fire
		var draftTarget draftSender = tmuxSess
		if *requireInputPrompt {
			lock, err := session.AcquireSendLock(inst.ID, sendTargetLockWait)
			if err != nil {
				out.ErrorWithData(fmt.Sprintf("cannot guard draft: %v", err), ErrCodeDeliveryFailed, map[string]interface{}{"delivery": deliveryTargetBusy})
				exitCLI(1)
			}
			defer lock.Release()
			draftTarget = &promptGuardTarget{sendRetryTarget: tmuxSess, tool: inst.Tool}
		}
		if err := executeDraft(draftTarget, message); err != nil {
			if guard, ok := draftTarget.(*promptGuardTarget); ok && guard.refused {
				out.ErrorWithData(fmt.Sprintf("message not submitted to '%s': %v", inst.Title, err), ErrCodeDeliveryFailed,
					map[string]interface{}{"delivery": deliveryMenuOpen})
				exitCLI(1)
			}
			out.Error(fmt.Sprintf("failed to pre-fill prompt: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		out.Success(fmt.Sprintf("Pre-filled prompt in '%s'", inst.Title), map[string]interface{}{
			"success":       true,
			"session_id":    inst.ID,
			"session_title": inst.Title,
			"message":       message,
			"tagged":        tagged,
		})
		return
	}

	// Send message atomically (text + Enter in single tmux invocation).
	// --no-wait: skip full readiness waiting, but run a capped preflight
	// barrier + extended verification loop to avoid the #616 race where
	// Claude's composer renders after the loop has already returned
	// success on startup "active" status, leaving the message unsubmitted.
	// default mode: full retry budget after readiness check.
	//
	// Both modes run the composer-draft guard (issue #1409) and submit
	// verification with a machine-checkable delivery status (issue #1413).
	tun := sendTuning(*noWait, *requireInputPrompt)
	if *queueWorker || busyAcceptsInput {
		// A live Claude composer already exists. Startup preflight and the
		// 30-check idle-turn probe add delay without proving this queued send.
		tun.preflightWait, tun.settleDelay = 0, 0
		tun.retry.maxRetries, tun.retry.checkDelay = 10, 100*time.Millisecond
	}
	// #2033: give the verification loop the hook-driven busy signal so the
	// Ctrl+C-and-resend recovery can tell a queued message on a live turn
	// from a message lost during TUI init. Same signal --defer-if-busy reads.
	tun.retry.targetBusyByHook = func() (bool, bool) {
		return hookDrivenBusy(inst)
	}
	// #1978: turn advancement in Claude's own transcript is the authoritative
	// submission signal; it is only available when the transcript existed
	// before the send (position is the proof).
	if pathKnownBeforeSend {
		turnQuery := session.TurnQuery{Path: turnPath, Prompt: message, Cursor: turnCursor}
		tun.retry.turnAdvanced = func() bool { return session.TurnAdvanced(turnQuery) }
	}
	if acceptanceGuard != nil {
		// Codex's counterpart: a new turn in the exact rollout past the
		// acceptance fence, the evidence the accepted-turn receipt rests on.
		tun.retry.turnAdvanced = func() bool { return codexTurnAdvancedPastFence(inst, acceptanceFence) }
		if err := validateCodexAcceptanceFence(inst, acceptanceFence); err != nil {
			acceptanceGuard.Release()
			out.Error(fmt.Sprintf("cannot submit against changed Codex turn fence: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		if err := acceptanceGuard.Prepare(inst.ID, time.Now()); err != nil {
			acceptanceGuard.Release()
			out.Error(fmt.Sprintf("cannot durably reserve Codex turn acceptance: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
	}
	// #2089: pick tmux keystrokes or Claude Code's own messaging socket for
	// this send. chooseSendTransport is pure and every check it runs happens
	// strictly before any byte is written, so falling back to tmux here is
	// indistinguishable from today's behavior on any resolution failure.
	sendTransportValue, sendTransportWarn := sendTransportFromConfig()
	if sendTransportWarn != "" {
		fmt.Fprintln(os.Stderr, sendTransportWarn)
	}
	// The busy probe reads the same hook-driven status --defer-if-busy holds
	// on, so it needs the same lookup closure. performSend only calls it
	// under --wait, which is the only caller that acts on the answer.
	hookStatus := func() (string, error) { return fetchHookDrivenStatus(profile, sessionRef) }
	// Comms Ledger (P3): the send's receipt is recorded before the action,
	// its transport outcome after. A queued send is recorded by the queue
	// (queueSend, publishSendState), so its worker does not record it twice,
	// and a machine wake line (a nudge typed by the daemon) is a wake record,
	// never a send.
	ledgerReq, ledgerSender := "", ""
	if !*queueWorker && ledgerSendAllowed() {
		// --no-tag: the caller opted out of being the sender (a web or
		// script send that only inherited a session's environment): recorded
		// as from a person at a shell, never attributed to that session.
		ledgerSender = senderID
		if *noTag {
			ledgerSender = ""
		}
		ledgerReq = session.SpoolCommsSend(ledgerSender, inst.ID, message, ledgerSendVia(inst), "")
	}
	sendRes, sendErr := performSend(inst, tmuxSess, message, *noWait || busyAcceptsInput, tun, sendTransportValue, *wait, hookStatus, nil, nil)
	if ledgerReq != "" {
		session.SpoolCommsDelivery(ledgerSender, inst.ID, ledgerReq, ledgerDeliveryState(sendRes.delivery, sendErr), sendRes.transport, ledgerDeliveryReason(sendErr), false)
	}
	// Computed now (accurate ack_ms), journaled after the verdict at every
	// exit path below — never before it, per the same rule applied to
	// handleSessionStop/handleSessionRestart.
	sendDetail := sendEventDetail(sendRes, sendErr, sentAt, sendJournalMetaFor(profile, storage, *queueWorker, message))
	if acceptanceGuard != nil {
		if markerErr := acceptanceGuard.RecordTransportOutcome(sendRes.delivery, time.Now()); markerErr != nil {
			acceptanceGuard.Release()
			extra := sendRes.jsonFields()
			extra["session_id"] = inst.ID
			extra["session_title"] = inst.Title
			out.ErrorWithData(fmt.Sprintf("cannot persist Codex submission state: %v", markerErr), ErrCodeInvalidOperation, extra)
			recordSendEvent(profile, inst.ID, sendDetail)
			exitCLI(1)
		}
	}
	if sendErr != nil {
		if acceptanceGuard != nil {
			acceptanceGuard.Release()
		}
		telemetry.ErrorOccurred(telemetry.AreaSend, telemetry.ErrKindOf(sendErr), inst.Tool)
		extra := sendRes.jsonFields()
		extra["session_id"] = inst.ID
		extra["session_title"] = inst.Title
		switch sendRes.delivery {
		case deliveryTypedNotSubmitted:
			out.ErrorWithData(fmt.Sprintf("message typed but not submitted to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliveryLineTooLong:
			// Nothing was typed, so the composer is exactly as the operator
			// left it. Retrying the same body is pointless; the actionable
			// advice is to break the line or send a file reference.
			out.ErrorWithData(fmt.Sprintf("message too long for '%s' to receive as one line: %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliveryMenuOpen, deliveryPaneGone:
			// Positive evidence the send did not go through (issue #1793):
			// the evidence line is the message.
			out.ErrorWithData(fmt.Sprintf("message not submitted to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliveryNoEvidence:
			out.ErrorWithData(fmt.Sprintf("message not delivered to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliveryComposerBlocked:
			// Nothing was typed: the operator's draft is exactly as they
			// left it. This is a delivery failure automation may retry once
			// the composer clears, not an invalid operation.
			out.ErrorWithData(fmt.Sprintf("message not delivered to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliveryTargetBusy:
			// Nothing was typed: another send held the target for the whole
			// bounded wait. Retry once it finishes.
			out.ErrorWithData(fmt.Sprintf("message not delivered to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		case deliverySocketWriteFailed:
			// #2089: the write to the Claude messaging socket started and
			// failed partway. The message may or may not have reached the
			// target's inbox — it is NEVER safe to retry on tmux here, that
			// would risk double delivery.
			out.ErrorWithData(fmt.Sprintf("socket write to '%s' failed after the send was already committed (message may or may not have been queued; do not resend): %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		default:
			out.ErrorWithData(fmt.Sprintf("failed to send message: %v", sendErr), ErrCodeInvalidOperation, extra)
		}
		recordSendEvent(profile, inst.ID, sendDetail)
		exitCLI(1)
	}
	recordSent()
	// Delivered: a later --wait/--stream failure is about the reply, not
	// about the trigger's send.
	fire.record(triggers.OutcomeDelivered)

	// Self-heal Stage 1: stamp the "we talked to it" clock. A delivered send is
	// exactly the event the idle_at_empty_prompt dwell is measured from — a
	// session is only stuck at an empty prompt if WE sent it something and
	// nothing happened. Targeted single-column write (never SaveInstances);
	// best-effort, never blocks or fails the send.
	if db := statedb.GetGlobal(); db != nil {
		_ = db.WriteLastSentAt(inst.ID, sentAt.Unix())
	}

	// Delivery succeeded, but if an operator draft was cleared and could not
	// be typed back, it's no longer on screen — surface it on stderr (it's
	// also in saved_draft in --json) so the operator can recover it rather
	// than discovering a silent loss. draft_restore_failed never blocks the
	// send: the automated message did go through.
	if sendRes.draftSaved != "" && sendRes.draftRestoreFailed {
		fmt.Fprintf(os.Stderr,
			"Warning: cleared the operator draft to deliver this message but could not restore it. Recover it from: %s\n",
			sendRes.draftSaved)
	}

	sendData := sendSuccessData(inst, message, sendRes, *wait)
	sendData["tagged"] = tagged
	if session.IsCodexCompatible(inst.Tool) {
		sendData["accepted_turn_kind"] = "codex_rollout"
	}
	acceptedAt := time.Now()
	acceptedTurn := observeAcceptedCodexTurn(*wait, inst, sendRes.delivery, acceptedAt, acceptanceFence)
	if acceptedTurn != nil && acceptanceGuard != nil {
		if err := acceptanceGuard.ResolveAccepted(); err != nil {
			acceptanceGuard.Release()
			sendData["accepted_turn"] = acceptedTurn
			out.ErrorWithData(
				fmt.Sprintf("accepted Codex turn but failed to finalize durable ownership: %v", err),
				ErrCodeInvalidOperation,
				completionTimeoutPayload(sendData),
			)
			exitCLI(1)
		}
		acceptanceGuard.Release()
		acceptanceGuard = nil
	}
	if acceptanceGuard != nil && !retainCodexAcceptanceGuardForCompletion(*wait, acceptedTurn) {
		// The unresolved marker survives this process. A later sender either
		// reconciles the exact new generation or refuses with the operator
		// recovery path; it can never claim this attempt's turn.
		acceptanceGuard.Release()
		acceptanceGuard = nil
	}
	if acceptedTurn != nil {
		sendData["accepted_turn"] = acceptedTurn
	}

	// A socket write does not interrupt a running turn: it lands in the
	// target's inbox and is picked up at the next turn boundary. So when the
	// target was already mid-turn at the moment of the write — or when the
	// probe could not establish that it was idle — the next completion
	// --wait would observe cannot be shown to belong to this message, and
	// printing its output as this message's response is a wrong
	// attribution. There is no in-band receipt to correlate against (§Step
	// 3, maintainer review of #2100), so --wait reports the outcome as
	// explicitly unverified rather than guessing. It is not an error — the
	// write itself succeeded — so the exit code stays 0, and the message is
	// never resent on tmux.
	skippedOutcome := ""
	if *wait {
		skippedOutcome = skippedWaitOutcome(sendRes)
	}

	// sendEventRecorded guards against double-journaling: some branches below
	// print their verdict and record immediately (and then return), others
	// fall through to --wait/--stream continuations whose own, later verdict
	// site records instead. Never journal before the verdict a given flag
	// combination actually prints (round-3 re-review finding).
	sendEventRecorded := false
	recordSendEventOnce := func() {
		if sendEventRecorded {
			return
		}
		sendEventRecorded = true
		recordSendEvent(profile, inst.ID, sendDetail)
	}

	if !*stream {
		// JSON --wait is one result, emitted only when completion succeeds or
		// times out. Human and non-wait output keep their existing eager ack.
		if !*wait || !*jsonOutput {
			summary := fmt.Sprintf("Sent message to '%s'", inst.Title)
			if sendRes.delivery == deliveryDelivered {
				// Delivered, confirmation unknown (issue #1793): say exactly
				// what could not be established, never "NOT delivered".
				summary = fmt.Sprintf("Sent message to '%s' (%s)", inst.Title, sendRes.note)
			}
			if sendRes.delivery == deliveryQueued {
				summary = fmt.Sprintf("Queued message for '%s' (target is mid-turn; it takes the message up when the current turn ends)", inst.Title)
			}
			// The socket path cannot claim delivery the way tmux's submit
			// verification can: the write completed, and Claude's inbox says
			// nothing back (maintainer review of #2100).
			if sendRes.transport == "socket" {
				summary = fmt.Sprintf("Wrote message to '%s' inbox (unacknowledged: Claude's inbox never confirms delivery)", inst.Title)
			}
			out.Success(summary, sendData)
			if journalSendOutcome(sendRes.delivery, nil) != health.SendConfirmed {
				out.QuietNotice(summary)
			}
			recordSendEventOnce()
		}
	}

	if !*stream && !*wait {
		return
	}

	// One --timeout budget for everything after delivery: establishing turn
	// identity, observing completion, and reading the reply. Each phase gets
	// what is left, never a fresh copy (PR #2043 round 2).
	waitDeadline := time.Now().Add(*timeout)

	if skippedOutcome != "" {
		// Refuse attribution before waiting for any transcript path or turn.
		// JSON --wait suppressed the eager ack, so emit its single result here.
		fmt.Fprintln(os.Stderr, sendSkippedWaitWarning(inst.Title, skippedOutcome))
		if *jsonOutput {
			out.Success(fmt.Sprintf("Wrote message to '%s' inbox (unacknowledged)", inst.Title), sendData)
		}
		recordSendEventOnce()
		return
	}

	// #2043: delivery acknowledgement is not reply identity. A message sent to
	// a busy target is queued behind the live turn and only becomes a user
	// record when its own turn starts, so --wait and --stream bind to that
	// durable record first — the in-flight turn's completion and output are
	// never consumed as the queued message's reply.
	var turnQuery session.TurnQuery
	if useTurnIdentity {
		if turnPath == "" {
			var err error
			turnPath, err = awaitTranscriptPath(inst, sessionRef, profile, instances, waitDeadline)
			if err != nil {
				failSessionSend(out, *stream, err.Error(), recordSendEventOnce)
			}
		}
		turnQuery = session.TurnQuery{Path: turnPath, Prompt: message, Cursor: turnCursor}
		if !pathKnownBeforeSend {
			turnQuery.NotBefore = sentAt
		}
	}

	// --stream: tail the Claude transcript and pipe JSONL events to
	// stdout until end_turn, idle timeout, or error. Issue #689.
	if *stream {
		var turnID session.TurnIdentity
		if useTurnIdentity {
			var identityErr error
			turnID, identityErr = session.AwaitTurnIdentity(turnQuery, time.Until(waitDeadline), 100*time.Millisecond)
			if identityErr != nil {
				failSessionSend(out, true, fmt.Sprintf("turn identity not established: %v", identityErr), recordSendEventOnce)
			}
		}
		if err := streamSessionSend(inst, sessionRef, profile, turnID, sentAt, streamOptions{
			idle:       *streamIdle,
			charBudget: *streamCharBudget,
			toolBudget: *streamToolBudget,
			timeout:    time.Until(waitDeadline),
		}); err != nil {
			// Error already serialized as a stream event; exit 1.
			recordSendEventOnce()
			exitCLI(1)
		}
		recordSendEventOnce()
		return
	}

	// --wait: block until the agent finishes processing, then print output.
	// Socket waits observe a new durable turn rather than correlate a prompt:
	// Claude wraps peer messages, and its inbox never acknowledges acceptance.
	// A dropped message with no new turn must not read back stale output.
	var finalStatus string
	var response *session.ResponseOutput
	var responseErr error
	if useTurnIdentity {
		var identityErr, completionErr error
		if sendRes.transport == "socket" {
			response, finalStatus, identityErr, completionErr, responseErr = awaitClaudeObservedReply(turnQuery, waitDeadline)
		} else {
			response, finalStatus, identityErr, completionErr, responseErr = awaitClaudeWaitReply(turnQuery, waitDeadline, func(remaining time.Duration) (string, error) {
				return waitAfterSend(tmuxSess, sendRes.transport, remaining)
			})
		}
		if identityErr != nil {
			out.Error(fmt.Sprintf("turn identity not established: %v", identityErr), ErrCodeInvalidOperation)
			recordSendEventOnce()
			exitCLI(1)
		}
		if completionErr != nil {
			out.Error(fmt.Sprintf("timeout waiting for completion: %v", completionErr), ErrCodeInvalidOperation)
			recordSendEventOnce()
			exitCLI(1)
		}
		if sendRes.transport != "socket" && errors.Is(responseErr, session.ErrTurnResponseIncomplete) && response != nil {
			fmt.Fprintf(os.Stderr, "Warning: %v — response may be incomplete\n", responseErr)
			responseErr = nil
		}
	} else {
		var err error
		finalStatus, err = waitAfterSend(tmuxSess, sendRes.transport, time.Until(waitDeadline))
		// The turn-start record may flush just after submit verification. Retry
		// at the completion boundary before emitting the one structured result.
		var receiptErr error
		acceptedTurn, receiptErr = retryAndRequireStructuredCodexAcceptedTurn(
			inst, *jsonOutput, *wait, acceptedTurn,
			sendRes.delivery, acceptedAt, acceptanceFence,
		)
		if acceptedTurn != nil {
			sendData["accepted_turn"] = acceptedTurn
		}
		if acceptedTurn != nil && acceptanceGuard != nil {
			if markerErr := acceptanceGuard.ResolveAccepted(); markerErr != nil {
				acceptanceGuard.Release()
				out.ErrorWithData(
					fmt.Sprintf("accepted Codex turn but failed to finalize durable ownership: %v", markerErr),
					ErrCodeInvalidOperation,
					completionTimeoutPayload(sendData),
				)
				recordSendEventOnce()
				exitCLI(1)
			}
		}
		if acceptanceGuard != nil {
			acceptanceGuard.Release()
			acceptanceGuard = nil
		}
		if receiptErr != nil {
			out.ErrorWithData(receiptErr.Error(), ErrCodeInvalidOperation, sendData)
			recordSendEventOnce()
			exitCLI(1)
		}
		if err != nil {
			out.ErrorWithData(
				fmt.Sprintf("timeout waiting for completion: %v", err),
				ErrCodeInvalidOperation,
				completionTimeoutPayload(sendData),
			)
			recordSendEventOnce()
			exitCLI(1)
		}
		// Refresh session ID: the instance was loaded before sending the
		// message, so the ClaudeSessionID may be stale (e.g., PostStartSync
		// timed out, TUI updated it during the wait, or /clear created a new
		// session). First try tmux env (fast), then fall back to reloading
		// from DB.
		if session.IsClaudeCompatible(inst.Tool) {
			if freshID := inst.GetSessionIDFromTmux(); freshID != "" {
				inst.ClaudeSessionID = freshID
				// #1815: own pane env — weak vouch (see
				// NoteClaudeSessionIDFromOwnPane).
				session.NoteClaudeSessionIDFromOwnPane(inst)
				inst.ClaudeDetectedAt = time.Now()
			}
		}
		// Pre-#2043 contract for non-Claude tools, whose output adapters do
		// not expose transcript UUIDs, and for slash commands, which Claude
		// records as command meta records rather than as the typed text.
		// Codex additionally binds a structured --json --wait reply to its
		// exact accepted turn generation rather than a freshness scan.
		if structuredCodexWait {
			response, responseErr = waitForCodexTurnOutput(inst, acceptedTurn.TurnGeneration, waitDeadline)
		} else {
			response, responseErr = waitForFreshOutput(inst, sentAt, instances)
		}
		if responseErr != nil {
			// Fallback: reload session from DB in case tmux env was also stale
			// (e.g., /clear created a new session that TUI or hooks detected)
			if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
				if freshInst, _, _ := ResolveSession(sessionRef, freshInstances); freshInst != nil {
					if structuredCodexWait {
						response, responseErr = waitForCodexTurnOutput(freshInst, acceptedTurn.TurnGeneration, waitDeadline)
					} else {
						response, responseErr = waitForFreshOutput(freshInst, sentAt, freshInstances)
					}
				}
			}
		}
	}
	if responseErr != nil {
		out.ErrorWithData(
			fmt.Sprintf("failed to get response: %v", responseErr),
			ErrCodeInvalidOperation,
			responseReadFailureData(sendData),
		)
		recordSendEventOnce()
		exitCLI(1)
	}
	if *jsonOutput {
		sendData["completion"] = "complete"
		sendData["content"] = response.Content
		if response.CodexTurnGeneration != "" {
			sendData["codex_turn_generation"] = response.CodexTurnGeneration
		}
		// #2397: the turn this reply is bound to. Its user record's
		// sessionId overrides the instance's claude_session_id set by
		// sendSuccessData: it is the conversation the turn landed in.
		if response.ClaudeTurnUUID != "" {
			sendData["claude_turn_uuid"] = response.ClaudeTurnUUID
			if response.SessionID != "" {
				sendData["claude_session_id"] = response.SessionID
			}
		}
		out.Success(fmt.Sprintf("Sent message to '%s'", inst.Title), sendData)
	} else {
		fmt.Println(response.Content)
		if sendRes.transport == "socket" {
			// The payload already says verified: false; say it on stderr too
			// so a human reading the terminal sees the same caveat.
			fmt.Fprintln(os.Stderr, sendUncorrelatedOutputNote())
		}
	}
	recordSendEventOnce()

	// Exit 1 for error/inactive status
	if finalStatus == "inactive" || finalStatus == "error" {
		exitCLI(1)
	}
}

// awaitClaudeWaitReply is the whole Claude --wait reply path after delivery,
// for exact-prompt sends: establish
// the turn identity (blocking until the message's own user record exists —
// a message queued behind a live turn waits here for its turn to start),
// observe completion (the UI prompt reappearing), then read THIS turn's
// reply. completion is injected so the path is testable without a tmux pane;
// it receives the remaining budget. All three phases share deadline.
//
// The status heuristic detects the prompt reappearing, but the JSONL may not
// be flushed yet — and the spike-filtered heuristic can read idle mid-turn
// (#1578). So the reply is polled for THIS turn's end-of-turn record for the
// rest of the budget; at the deadline, text the turn already produced comes
// back with ErrTurnResponseIncomplete rather than being dropped, and is never
// dressed up as complete. Output from the turn that was in flight when the
// message was queued can never be returned: the read starts after the
// message's own user record and stops at the next human prompt.
func awaitClaudeWaitReply(q session.TurnQuery, deadline time.Time, completion func(remaining time.Duration) (string, error)) (resp *session.ResponseOutput, finalStatus string, identityErr, completionErr, responseErr error) {
	return awaitClaudeReply(q, deadline, session.AwaitTurnIdentity, completion)
}

// awaitClaudeObservedReply waits for the selected transcript turn's end record,
// not pane-global status, which may already describe a later unrelated turn.
// Observation and reply polling share one deadline without a completion grace.
func awaitClaudeObservedReply(q session.TurnQuery, deadline time.Time) (resp *session.ResponseOutput, finalStatus string, identityErr, completionErr, responseErr error) {
	turnID, identityErr := session.AwaitObservedTurn(q, time.Until(deadline), 100*time.Millisecond)
	if identityErr != nil {
		return nil, "", identityErr, nil, nil
	}
	resp, responseErr = session.AwaitTurnResponse(turnID, time.Until(deadline), 100*time.Millisecond)
	return resp, "waiting", nil, nil, responseErr
}

func awaitClaudeReply(q session.TurnQuery, deadline time.Time, turn func(session.TurnQuery, time.Duration, time.Duration) (session.TurnIdentity, error), completion func(remaining time.Duration) (string, error)) (resp *session.ResponseOutput, finalStatus string, identityErr, completionErr, responseErr error) {
	turnID, identityErr := turn(q, time.Until(deadline), 100*time.Millisecond)
	if identityErr != nil {
		return nil, "", identityErr, nil, nil
	}
	finalStatus, completionErr = completion(time.Until(deadline))
	if completionErr != nil {
		return nil, finalStatus, nil, completionErr, nil
	}
	resp, responseErr = session.AwaitTurnResponse(turnID, time.Until(deadline), 100*time.Millisecond)
	return resp, finalStatus, nil, nil, responseErr
}

// sendTracksTurn reports whether a send reads its own turn record out of the
// Claude transcript: as the verification loop's authoritative submission
// signal, and (with --wait/--stream) as the reply binding (PR #2043).
//
// Claude-compatible tools only: other tools' output adapters expose no
// transcript UUIDs, so non-Claude --wait keeps its best-effort contract.
// Slash commands are excluded too: Claude records them as
// `<command-name>` meta records, never as the typed text, so no identity
// could be established and --wait would time out on every `/compact`.
func sendTracksTurn(tool, message string) bool {
	if !session.IsClaudeCompatible(tool) {
		return false
	}
	return !strings.HasPrefix(strings.TrimLeft(message, " \t"), "/")
}

// failSessionSend reports a post-delivery failure and exits 1. --stream
// consumers read stdout as JSONL events and must always get a parseable
// response, so the failure is emitted as an error event there. record, when
// non-nil, journals the send event after this verdict is printed and before
// exit — the pre-send call sites (turn identity setup before performSend)
// pass nil since there is no send to journal yet.
func failSessionSend(out *CLIOutput, stream bool, msg string, record func()) {
	if stream {
		emitStreamErrorEvent(msg)
	} else {
		out.Error(msg, ErrCodeInvalidOperation)
	}
	if record != nil {
		record()
	}
	exitCLI(1)
}

// emitStreamErrorEvent writes one --stream error event to stdout, matching
// the event schema so consumers need no separate error channel.
func emitStreamErrorEvent(msg string) {
	b, _ := json.Marshal(map[string]interface{}{
		"type":    "error",
		"message": msg,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	})
	fmt.Println(string(b))
}

// awaitTranscriptPath resolves the Claude transcript path for a session whose
// transcript did not exist before the send. Claude writes the file after the
// first assistant chunk, so this polls until deadline, refreshing the session
// from the DB in case the TUI or hooks learned the session ID meanwhile.
// A colliding transcript (#1400) is refused, never polled.
func awaitTranscriptPath(inst *session.Instance, sessionRef, profile string, peers []*session.Instance, deadline time.Time) (string, error) {
	resolved := inst
	if !resolved.TranscriptIsResolvableLocally() {
		return "", fmt.Errorf("session runs on %s; its Claude transcript is not on this machine", resolved.SSHHost)
	}
	for {
		if fresh := resolved.GetSessionIDFromTmux(); fresh != "" && fresh != resolved.ClaudeSessionID {
			resolved.ClaudeSessionID = fresh
			session.NoteClaudeSessionIDFromOwnPane(resolved)
			resolved.ClaudeDetectedAt = time.Now()
		}
		p, err := resolved.GetJSONLPathChecked(peers)
		if err != nil {
			return "", fmt.Errorf("refusing a colliding transcript: %w", err)
		}
		if p != "" {
			return p, nil
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("session transcript not found before the --timeout deadline (session id=%q)", resolved.ClaudeSessionID)
		}
		if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
			peers = freshInstances
			if fi, _, _ := ResolveSession(sessionRef, freshInstances); fi != nil {
				resolved = fi
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// defaultSendOptions returns the verification-loop options used by the default
// (non-`--no-wait`) CLI send path. verifyDelivery is enabled so the CLI
// surfaces silent drops as errors rather than returning false success — see
// issue #876.
func defaultSendOptions() sendRetryOptions {
	return sendRetryOptions{
		maxRetries:     50,
		checkDelay:     300 * time.Millisecond,
		verifyDelivery: true,
	}
}

func shouldSkipConductorHeartbeatSend(inst *session.Instance, message string) bool {
	if inst == nil || !session.IsConductorHeartbeatMessage(message) {
		return false
	}
	name := inst.ResolveConductorName()
	if name == "" {
		return false
	}
	meta, err := session.LoadConductorMeta(name)
	if err != nil {
		return false
	}
	idleMinutes := meta.GetHeartbeatIdleMinutes()
	if idleMinutes <= 0 {
		return false
	}
	lastActivity, err := session.GetConductorLastActivity(name, meta.Profile)
	if err != nil {
		return false
	}
	if lastActivity.IsZero() {
		return false
	}
	return time.Since(lastActivity) >= time.Duration(idleMinutes)*time.Minute
}

// sendUncorrelatedOutputNote is the stderr line printed after a socket
// --wait prints output: the payload says `verified: false`, and a human
// reading the terminal deserves the same caveat.
func sendUncorrelatedOutputNote() string {
	return "Note: output is turn-observed, not correlated to this message (socket delivery has no receipt); a turn that started between the idle probe and the write would be misattributed."
}

// sendSuccessData assembles the --json success payload for one completed
// send: the identity fields, every delivery field jsonFields reports, and —
// on a socket send with --wait — the wait_outcome plus `verified: false`.
//
// Every socket --wait gets both keys, including the one that returns real
// output (waitOutcomeObservedNotCorrelated): see the wait_outcome constants
// for why none of the three can claim correlation. A tmux --wait gets
// neither, and no send without --wait gets either, since there is no wait
// outcome to describe. handleSessionSend calls exactly this, so a test of
// this function is a test of what the CLI actually emits (round-2 review of
// #2100; CodeRabbit on e94b296c).
func sendSuccessData(inst *session.Instance, message string, res sendDeliveryResult, wait bool) map[string]interface{} {
	data := map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"message":       message,
	}
	for k, v := range res.jsonFields() {
		data[k] = v
	}
	// #2397: name the native Claude conversation, as show/output do.
	if session.IsClaudeCompatible(inst.Tool) && inst.ClaudeSessionID != "" {
		data["claude_session_id"] = inst.ClaudeSessionID
	}
	if wait {
		if outcome := socketWaitOutcome(res); outcome != "" {
			data["wait_outcome"] = outcome
			data["verified"] = false
		}
	}
	return data
}

// sendSkippedWaitWarning is the stderr line for a --wait that declined to
// wait. The two outcomes get different words on purpose: only the confirmed
// case may say the target was mid-turn (round-2 review of #2100).
func sendSkippedWaitWarning(title, outcome string) string {
	reason := "was mid-turn"
	if outcome == waitOutcomeUnverifiedBusyProbeFailed {
		reason = "could not be confirmed idle"
	}
	return fmt.Sprintf(
		"Warning: '%s' %s when this message was written to its inbox, so its next completion cannot be attributed to this message; --wait returned without waiting (wait_outcome: %s)",
		title, reason, outcome)
}

// deliveryConfirmation maps a delivery status to its --json "confirmation".
func deliveryConfirmation(delivery string) string {
	switch delivery {
	case deliverySubmitted, deliveryQueued:
		return send.ConfirmationConfirmed
	case deliveryDelivered, deliveryUnverified, deliveryQueuedSocket:
		return send.ConfirmationUnknown
	default:
		return send.ConfirmationFailed
	}
}

// sendDeliveryResult is the prompt-state-aware outcome of executeSend.
type sendDeliveryResult struct {
	// delivery is one of the delivery* constants above.
	delivery string
	// note is the human wording for a deliveryDelivered outcome: which tool
	// could not confirm, or within what window (send.UnconfirmedMessage).
	note string
	// held is how long the composer guard waited/worked before the send
	// (bounded hold plus the final read-only settle check).
	held time.Duration
	// draftSaved is the operator draft that was cleared from the composer to
	// make way for the automated send (empty when no clear was needed).
	draftSaved string
	// draftCleared reports whether the guard confirmed the composer emptied
	// after Ctrl+C.
	draftCleared bool
	// draftRestored reports whether the saved operator draft was typed back
	// (without Enter) after the automated delivery.
	draftRestored bool
	// draftRestoreFailed reports that a saved operator draft was cleared but
	// the type-back failed (SendKeysChunked errored) — the draft is held in
	// draftSaved for recovery and must be surfaced, not silently dropped.
	draftRestoreFailed bool

	// transport is "tmux" or "socket" (issue #2089), always set.
	transport string
	// fallbackReason is populated only when transport is "tmux" AND a socket
	// send was genuinely attempted and refused (chooseSendTransport's
	// resolve() step) — not when a socket was never a candidate to begin
	// with (draft, an explicit send_transport=tmux pin, a non-Claude tool, a
	// slash command, or no known Claude session ID). An explicit pin is not
	// a fallback.
	fallbackReason send.UnavailableReason
	// socketMsgID is the msg_id SendOverClaudeSocket generated, set only on
	// a successful socket send.
	socketMsgID string
	// targetBusyAtSend reports that the target was CONFIRMED mid-turn when
	// the socket write happened (socket transport only), from a status probe
	// taken immediately before the write.
	targetBusyAtSend bool
	// busyProbeFailed reports that the same probe could read no status at
	// all, so busyness is unknown. Kept separate from targetBusyAtSend:
	// both make --wait decline to wait, but only one of them is a fact about
	// the target (round-2 review of #2100).
	busyProbeFailed bool
}

func completionTimeoutPayload(data map[string]interface{}) map[string]interface{} {
	payload := make(map[string]interface{}, len(data)+1)
	for key, value := range data {
		payload[key] = value
	}
	payload["completion"] = "timeout"
	return payload
}

func responseReadFailureData(data map[string]interface{}) map[string]interface{} {
	return completionTimeoutPayload(data)
}

// jsonFields returns the delivery-status fields added to `session send`
// success and error payloads in --json mode (issue #1413 machine-checkable
// contract; #1409 draft-guard observability).
func (r sendDeliveryResult) jsonFields() map[string]interface{} {
	fields := map[string]interface{}{}
	if r.delivery != "" {
		fields["delivery"] = r.delivery
		// Explicit, machine-checkable: a caller must not have to know which
		// delivery strings imply an accepted turn. Only deliverySubmitted
		// does — it rests on positive evidence the agent took the message up.
		// `typed` in particular means the bytes arrived and nothing confirmed
		// that (issue #1793), and a socket write is in the same class: it
		// proves the write completed, not that the inbox accepted it
		// (maintainer review of #2100).
		fields["submitted"] = r.delivery == deliverySubmitted
		// confirmation is the three-outcome contract (issue #1793): what was
		// established about the harness taking the message. `confirmed`
		// (submitted, or Claude's own queued acknowledgement), `unknown`
		// (delivered, no signal either way), `failed` (positive evidence).
		fields["confirmation"] = deliveryConfirmation(r.delivery)
	}
	if r.transport == "socket" {
		// Claude's inbox never confirms delivery in-band, so nothing can
		// flip this today. The field exists so a caller can distinguish
		// "written, unconfirmed" from a future receipt path that confirms.
		fields["acknowledged"] = false
		// Each surfaced only when true: absence is the common case and
		// carries no information. They are mutually exclusive — a probe
		// that failed established nothing about the target.
		if r.targetBusyAtSend {
			fields["target_busy_at_send"] = true
		}
		if r.busyProbeFailed {
			fields["busy_probe_failed"] = true
		}
	}
	if ms := r.held.Milliseconds(); ms > 0 {
		fields["held_for_composer_ms"] = ms
	}
	if r.draftSaved != "" {
		fields["saved_draft"] = r.draftSaved
		fields["draft_restored"] = r.draftRestored
		if r.draftRestoreFailed {
			fields["draft_restore_failed"] = true
		}
	}
	if r.transport != "" {
		fields["transport"] = r.transport
	}
	if r.transport == "tmux" && r.fallbackReason != "" {
		fields["fallback_reason"] = string(r.fallbackReason)
	}
	if r.socketMsgID != "" {
		fields["msg_id"] = r.socketMsgID
	}
	return fields
}

// sendExecTuning bundles the bounded budgets of the full executeSend
// pipeline (preflight barrier, composer-draft guard, verification loop) so
// tests can shrink them and production paths share one definition.
type sendExecTuning struct {
	requireInputPrompt bool
	// guardHold bounds the #1409 hold-and-retry phase: how long an automated
	// send waits for a non-empty operator draft to clear on its own before
	// refusing delivery while preserving it.
	guardHold      time.Duration
	guardPoll      time.Duration
	guardClearWait time.Duration
	// preflightWait/preflightPoll bound the --no-wait composer-visibility
	// barrier (issue #616).
	preflightWait time.Duration
	preflightPoll time.Duration
	// settleDelay is the post-composer-render settle pause (issue #616).
	settleDelay time.Duration
	// retry is the verification-loop budget (issues #876, #1413).
	retry sendRetryOptions
}

func sendTuning(noWait, requireInputPrompt bool) sendExecTuning {
	tun := defaultSendTuning()
	if noWait {
		tun = noWaitSendTuning()
	}
	tun.requireInputPrompt = requireInputPrompt
	return tun
}

// defaultSendTuning is the tuning for the default (readiness-waited) send
// path. The guard hold is generous because the caller already waited for
// readiness; an operator mid-keystroke gets up to 10s to finish or pause.
func defaultSendTuning() sendExecTuning {
	return sendExecTuning{
		guardHold:      10 * time.Second,
		guardPoll:      250 * time.Millisecond,
		guardClearWait: 1500 * time.Millisecond,
		retry:          defaultSendOptions(),
	}
}

// noWaitSendTuning is the tuning for `session send --no-wait`. --no-wait
// skips the readiness wait, NOT the composer guard or submit verification —
// but its guard hold is kept small (2s) so automated callers (heartbeats,
// inbox nudges, watchers) pay minimal added latency. When the composer is
// empty the guard costs a single pane capture.
func noWaitSendTuning() sendExecTuning {
	return sendExecTuning{
		guardHold:      2 * time.Second,
		guardPoll:      150 * time.Millisecond,
		guardClearWait: time.Second,
		preflightWait:  5 * time.Second,
		preflightPoll:  100 * time.Millisecond,
		settleDelay:    500 * time.Millisecond,
		retry:          noWaitSendOptions(),
	}
}

// executeSend is the prompt-state-aware send pipeline used by
// `session send` (issues #1409 + #1413):
//
//  1. --no-wait only: capped preflight barrier until the Claude composer is
//     visible, plus a short settle delay (issue #616).
//  2. Composer-draft guard (issue #1409): hold while the composer shows a
//     non-empty operator draft; at the bound, save the draft and clear the
//     composer (Ctrl+C) so the automated message cannot merge with it.
//  3. Send + bounded submit verification (issues #876, #1413), classifying
//     the outcome into a delivery status.
//  4. Restore the saved operator draft (typed back without Enter) once the
//     automated delivery is not stuck in the composer. When the automated
//     message itself ends typed_not_submitted the draft is NOT retyped (it
//     would merge into the stuck composer) — it is surfaced in the result
//     instead so the caller can report it.
//
// Steps 1 and 4 are Claude-only: non-Claude tools gate readiness upstream.
// Step 2 runs for every tool whose composer agent-deck can read: Claude, and
// Codex since issue #2536 (composerDraftReaderFor).
func executeSend(target sendRetryTarget, tool, message string, noWait bool, tun sendExecTuning) (sendDeliveryResult, error) {
	res := sendDeliveryResult{}
	if tun.requireInputPrompt {
		guard := &promptGuardTarget{sendRetryTarget: target, tool: tool}
		target = guard
		tun.retry.promptGuard = guard
	}
	claudeLike := session.IsClaudeCompatible(tool)

	if noWait && claudeLike {
		if awaitComposerReadyBestEffort(target, tun.preflightWait, tun.preflightPoll) {
			// Post-composer settle: React mount can lag behind the
			// composer glyph by a few hundred ms on cold starts.
			if tun.settleDelay > 0 {
				time.Sleep(tun.settleDelay)
			}
		}
	}
	if guard := tun.retry.promptGuard; guard != nil {
		if err := guard.beforeBatch(); err != nil {
			res.delivery, err = guard.refusal()
			return res, err
		}
	}

	if draftReader, guarded := composerDraftReaderFor(tool); guarded {
		guard := send.GuardComposerDraft(target, send.ComposerGuardOptions{
			HoldWait:     tun.guardHold,
			PollInterval: tun.guardPoll,
			ClearWait:    tun.guardClearWait,
			Strip:        tmux.StripANSI,
			Draft:        draftReader,
		})
		res.held = guard.Held
		if guard.Refused {
			res.delivery = deliveryComposerBlocked
			return res, fmt.Errorf("message not sent: composer is occupied or unreadable; existing draft preserved")
		}
		if claudeLike {
			// Provenance for the #1777 attribution gate, taken from the
			// capture the guard already made just before we type: with no
			// paste marker parked in the composer then, a marker seen
			// during verification is the collapsed form of our own payload
			// and may be nudged.
			tun.retry.composerPasteFreeBeforeSend = guard.ComposerPasteMarkerFree
			tun.retry.suggestionShownBeforeSend = guard.SuggestionShown
		}
	}

	tun.retry.tool = tool
	// #2148 item 4b: give `session send` the same #2079 truncation guard the
	// launch/instance path already has. A single-line message (or a
	// non-Claude target, since claudeLike gates this) never collapses behind
	// a paste marker, so expectedPasteBreaks stays 0 and the send is
	// unchecked, matching pre-existing behavior exactly.
	if claudeLike {
		tun.retry.expectedPasteBreaks = send.ExpectedPasteMarkerLineBreaks(message)
	}
	delivery, err := sendWithRetryTarget(target, message, skipClaudeDeliveryVerify(tool), tun.retry)
	res.delivery = delivery
	if delivery == deliveryDelivered {
		res.note = send.UnconfirmedMessage(tool, verificationWindow(tun.retry.verificationChecks(claudeLike), tun.retry.checkDelay))
	}

	return res, err
}

// composerDraftReaderFor returns the composer-draft reader for tool and
// whether the composer-draft guard (issue #1409) applies to it at all. Claude
// tools read Claude's composer; Codex reads its last "›" cell (issue #2536).
// Other tools have no introspectable composer and stay unguarded.
func composerDraftReaderFor(tool string) (send.ComposerDraftReader, bool) {
	switch {
	case session.IsClaudeCompatible(tool):
		return send.ComposerDraft, true
	case tool == "codex":
		return send.CodexComposerDraft, true
	}
	return nil, false
}

// skipClaudeDeliveryVerify reports whether the Claude-tuned post-send delivery
// verification (issue #876) should be skipped for tool. The verify keys off
// Claude-specific TUI signals (an "active" transition, the composer glyph,
// unsent-paste markers); non-Claude tools never surface those, so running it
// false-negatives a delivered message as "dropped silently" (#1238, #1205,
// #876). Claude tools keep the verify; every non-Claude tool skips it — the
// general superset of #1228's codex-only skip.
func skipClaudeDeliveryVerify(tool string) bool {
	return !session.UsesClaudeDeliveryVerify(tool)
}

// draftSender is implemented by *tmux.Session for the --draft path.
type draftSender interface {
	SendKeysChunked(string) error
}

// executeDraft pre-fills the prompt without pressing Enter.
func executeDraft(target draftSender, message string) error {
	return target.SendKeysChunked(message)
}

// noWaitSendOptions returns the verification-loop options used by the
// `session send --no-wait` path.
//
// Budget sizing (issue #616): a fresh Claude session with MCPs can take
// 5-40s before its TUI input handler is interactive. If verification
// returns on `activeChecks>=2` (from startup animations) before the
// composer renders, a swallowed Enter leaves the message typed-but-not-
// submitted. Budget must be long enough to see the composer either
// accept or reject the submission.
//
// Full-body resend recovery is disabled for every send mode. Historically,
// maxFullResends=-1 disabled the Ctrl+C-then-resend
// path (issue #479 — would otherwise double-send).
func noWaitSendOptions() sendRetryOptions {
	return sendRetryOptions{
		maxRetries:     30,
		checkDelay:     200 * time.Millisecond,
		maxFullResends: -1,
		// Issue #876: even on the --no-wait path, callers expect that a
		// `Sent` exit means the message reached the agent. Without this,
		// the verification loop would still fall through to nil on a
		// silent drop.
		verifyDelivery: true,
	}
}

// awaitComposerReadyBestEffort polls the pane until the Claude composer
// prompt (`❯`) appears, returning true. If the composer never appears
// within maxWait, returns false without blocking longer — preserving the
// `--no-wait` spirit when the session is slow or broken.
//
// Added for issue #616: eliminates the race where `session send --no-wait`
// fires before Claude's TUI input handler is mounted.
func awaitComposerReadyBestEffort(target sendRetryTarget, maxWait, pollInterval time.Duration) bool {
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(maxWait)
	for {
		if rawContent, err := target.CapturePaneFresh(); err == nil {
			if send.HasCurrentComposerPrompt(tmux.StripANSI(rawContent)) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		remaining := time.Until(deadline)
		sleep := pollInterval
		if remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
	}
}

// waitForCompletion polls until the agent finishes processing (status leaves "active").
// Returns the final status string ("waiting", "idle", "inactive") or an error on timeout.
func waitForCompletion(checker statusChecker, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	pollInterval := waitForCompletionTiming.poll

	// Initial grace period: wait for the agent to start processing.
	// sendWithRetry already checks for "active", but give a small buffer.
	time.Sleep(waitForCompletionTiming.grace)

	consecutiveErrors := 0
	const maxConsecutiveErrors = 5

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("agent still running after %s", timeout)
		default:
		}

		status, err := checker.GetStatus()
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				return "error", nil // Session likely died
			}
			time.Sleep(pollInterval)
			continue
		}
		consecutiveErrors = 0

		// "active" means still processing, keep waiting
		if status == "active" {
			time.Sleep(pollInterval)
			continue
		}

		// Any non-active status means the agent is done
		return status, nil
	}
}

// waitForTurnStart polls checker.GetStatus() every 500ms until it reports
// "active" or timeout elapses. Returns true iff it saw "active" in time.
//
// #2089: the socket send path returns as soon as the frame is written to
// Claude's messaging socket — unlike the tmux path, nothing here observed
// the target actually pick the message up. waitForCompletion's 1s initial
// grace period is calibrated for tmux, where executeSend's own verification
// loop already blocked until an "active" transition; on the socket path
// that transition hasn't necessarily happened yet by the time --wait starts
// polling, so waitForCompletion could see a stale non-active status and
// return immediately with stale output. Called only on the socket path,
// before waitForCompletion, to close that gap.
func waitForTurnStart(checker statusChecker, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	const pollInterval = 500 * time.Millisecond
	for {
		if status, err := checker.GetStatus(); err == nil && status == "active" {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
}

// waitAfterSend is the `--wait` completion pipeline used by handleSessionSend
// (#2089): on the socket transport, first wait (bounded) for the turn to
// start (see waitForTurnStart's doc comment), then wait for it to finish. On
// the tmux transport, skip straight to the completion wait — unchanged from
// before #2089.
//
// Both waits share ONE deadline computed from timeout: waitForTurnStart and
// waitForCompletion must not each get their own full timeout budget, or
// --wait could take up to turnStartBound + timeout instead of honoring
// timeout as the cap.
//
// On the socket transport, a turn that never starts within the bound is a
// hard error, not a fall-through to waitForCompletion: waitForCompletion
// treats a non-"active" status as complete, so falling through would let
// --wait print stale output and exit 0 for a message the target never
// actually consumed (held by its own inbound controls, or silently
// dropped — §1.5, Claude's inbox never acknowledges either way). Reported
// the same way the existing --wait timeout is, so callers don't need a new
// error-handling branch.
func waitAfterSend(checker statusChecker, transport string, timeout time.Duration) (string, error) {
	waitDeadline := time.Now().Add(timeout)
	if transport == "socket" {
		turnStartBound := 30 * time.Second
		if remaining := time.Until(waitDeadline); remaining < turnStartBound {
			turnStartBound = remaining
		}
		if !waitForTurnStart(checker, turnStartBound) {
			return "", fmt.Errorf("target did not start a turn within %s after socket delivery; the message may be held by the target's inbound controls or dropped, check the target session", turnStartBound.Round(time.Second))
		}
	}
	completionTimeout := time.Until(waitDeadline)
	if completionTimeout <= 0 {
		// The turn-start wait (successfully) consumed the whole budget:
		// same shape of failure waitForCompletion itself would report on a
		// real timeout, so report it identically rather than starting a new
		// poll against an already-exhausted budget.
		return "", fmt.Errorf("agent still running after %s", timeout)
	}
	return waitForCompletion(checker, completionTimeout)
}

// freshOutputConfig holds tunable parameters for waitForFreshOutput.
// Tests override these via freshOutputTestConfig; production uses defaults.
type freshOutputConfig struct {
	pollInterval time.Duration
	timeout      time.Duration
}

// waitForCodexTurnOutput bridges the ordering gap between Codex's completion
// hook and the final rollout append. Content and timestamps are insufficient:
// consecutive turns can legitimately emit identical replies, so only the
// exact accepted thread:turn generation can satisfy this read.
//
// The status heuristic that precedes this read can report a long Codex turn
// as finished while it is still running (a quiet pane during a long tool
// call), so the read polls until the caller's --wait deadline rather than a
// fixed flush window of its own (#2395). task_complete is only written when
// the turn ends, so a running turn can never satisfy it early.
func waitForCodexTurnOutput(inst *session.Instance, generation string, deadline time.Time) (*session.ResponseOutput, error) {
	if inst == nil || generation == "" {
		return nil, fmt.Errorf("accepted Codex turn identity is unavailable")
	}
	pollInterval := 250 * time.Millisecond
	if cfg := freshOutputTestConfig; cfg != nil {
		pollInterval = cfg.pollInterval
	}
	var lastErr error
	for {
		resp, err := inst.GetLastResponseBestEffort()
		if err == nil && resp.CodexTurnGeneration == generation {
			return resp, nil
		}
		lastErr = err
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(min(pollInterval, max(time.Until(deadline), time.Millisecond)))
	}
	if lastErr != nil {
		return nil, fmt.Errorf("Codex turn %s did not complete within the --timeout budget: %w", generation, lastErr)
	}
	return nil, fmt.Errorf("Codex turn %s did not complete within the --timeout budget", generation)
}

// waitForFreshOutput polls the session's JSONL file until it contains an assistant
// response with a timestamp not before sentAt (with a 250ms skew tolerance).
// This bridges the gap between the UI prompt reappearing (detected by
// waitForCompletion) and the JSONL being flushed to disk.
//
// Local Pi sessions also expose structured timestamps. Other tools and
// nonlocal Pi sessions retain best-effort output without a freshness claim.
//
// Claude retains its best-effort response with a warning on timeout. Pi
// returns an error instead of reporting an earlier turn as this send's reply.
//
// peers carries the profile snapshot for the #1400 collision guard: a
// claude_session_id shared by multiple live instances resolves to ONE
// transcript, so waiting on it would return another session's output.
// Fail fast (same semantics as --stream's #1352 guard) instead of polling
// a colliding transcript until the freshness timeout.
func waitForFreshOutput(inst *session.Instance, sentAt time.Time, peers []*session.Instance) (*session.ResponseOutput, error) {
	// Pi's transcript lives in the tool's HOME. Legacy SSH/sandbox instances
	// use terminal fallback, which does not provide timestamp evidence.
	localPi := inst.Tool == "pi" && !inst.IsSSH() && !inst.IsSandboxed()
	if !session.IsClaudeCompatible(inst.Tool) && !localPi {
		return inst.GetLastResponseBestEffort()
	}

	// #1400: refuse a colliding transcript before entering the poll loop.
	if session.IsClaudeCompatible(inst.Tool) {
		if _, err := inst.GetJSONLPathChecked(peers); err != nil {
			return nil, fmt.Errorf("refusing to read a colliding transcript: %w", err)
		}
	}

	pollInterval := 250 * time.Millisecond
	timeout := 5 * time.Second
	if cfg := freshOutputTestConfig; cfg != nil {
		pollInterval = cfg.pollInterval
		timeout = cfg.timeout
	}

	// Allow 250ms of clock skew / rounding tolerance.
	// Claude's JSONL timestamps may have only second precision, and local
	// time.Now() can be slightly ahead of Claude's clock. Tighter than the
	// original 2s to reduce false positives on genuinely stale output.
	threshold := sentAt.Add(-250 * time.Millisecond)
	if localPi {
		// Pi records milliseconds; do not accept a previous turn under Claude's
		// wider clock-skew allowance.
		threshold = sentAt.Truncate(time.Millisecond)
	}

	deadline := time.Now().Add(timeout)
	var lastResp *session.ResponseOutput
	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := inst.GetLastResponseBestEffort()
		if err != nil {
			lastErr = err
			time.Sleep(pollInterval)
			continue
		}
		lastResp = resp
		lastErr = nil

		// If the response has a timestamp, check freshness
		if resp.Timestamp != "" {
			if ts, parseErr := time.Parse(time.RFC3339Nano, resp.Timestamp); parseErr == nil {
				if !ts.Before(threshold) {
					return resp, nil
				}
			} else if ts, parseErr := time.Parse(time.RFC3339, resp.Timestamp); parseErr == nil {
				if !ts.Before(threshold) {
					return resp, nil
				}
			}
		}

		time.Sleep(pollInterval)
	}

	// Pi's send --wait result must belong to this turn. Neither stale text nor
	// a terminal fallback without a timestamp can establish that relationship.
	if localPi {
		if lastErr != nil {
			return nil, fmt.Errorf("Pi output freshness timeout (%s): %w", timeout, lastErr)
		}
		return nil, fmt.Errorf("Pi output freshness timeout (%s): no fresh assistant response", timeout)
	}
	// Preserve Claude's historical warning-and-best-effort timeout behavior.
	if lastResp != nil {
		fmt.Fprintf(os.Stderr, "Warning: output freshness timeout (%s) — response may be stale\n", timeout)
		return lastResp, nil
	}
	return nil, lastErr
}

// streamPreconditionError returns a non-empty error message when the given
// tool is not supported by --stream. Phase 1 is Claude-only (issue #689);
// non-Claude tools error cleanly here rather than silently producing empty
// output.
func streamPreconditionError(tool string) string {
	if session.IsClaudeCompatible(tool) {
		return ""
	}
	return fmt.Sprintf("--stream is not supported for tool %q (Phase 1 supports Claude-compatible tools only)", tool)
}

// streamOptions carries caller-tunable knobs for --stream.
type streamOptions struct {
	idle       time.Duration
	charBudget int
	toolBudget int
	timeout    time.Duration
}

// streamSessionSend tails the Claude session JSONL for a freshly sent
// message and writes structured stream events as JSONL to stdout until
// the assistant reaches end_turn, idle-times out, or ctx is cancelled.
//
// With a non-zero turnID (PR #2043) the stream starts at that durable user
// record, so a message queued behind a live turn never streams that turn's
// tail. A zero turnID is the legacy timestamp path, kept for slash commands
// (see sendTracksTurn), where sentAt gates out history instead.
//
// Overall budget: streamOptions.timeout bounds the entire stream (not just
// idle gaps), matching the semantics of --wait's --timeout.
func streamSessionSend(inst *session.Instance, sessionRef, profile string, turnID session.TurnIdentity, sentAt time.Time, opts streamOptions) error {
	cfg := session.StreamConfig{
		IdleTimeout: opts.idle,
		CharBudget:  opts.charBudget,
		ToolBudget:  opts.toolBudget,
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if turnID.Path != "" {
		// The identity was established against a checked, locally resolvable
		// transcript; there is nothing left to resolve or guess.
		return session.StreamTranscriptForTurn(ctx, turnID, inst.ClaudeSessionID, os.Stdout, cfg)
	}

	// Legacy path: resolve the JSONL path. Claude writes the file after the
	// first assistant chunk, so we poll briefly for its existence.
	resolvedInst := inst
	if session.IsClaudeCompatible(inst.Tool) {
		if fresh := inst.GetSessionIDFromTmux(); fresh != "" {
			inst.ClaudeSessionID = fresh
			// #1815: own pane env — weak vouch.
			session.NoteClaudeSessionIDFromOwnPane(inst)
			inst.ClaudeDetectedAt = time.Now()
		}
	}

	// A remote session's transcript is on the remote host, so the poll below can
	// only ever time out. Say why now instead of after the full timeout with
	// "transcript not found", which reads as "not written yet" and sends the
	// user looking on the wrong machine (#1851).
	if !resolvedInst.TranscriptIsResolvableLocally() {
		msg := fmt.Sprintf("session runs on %s; its Claude transcript is not on this machine, so there is nothing to stream", resolvedInst.SSHHost)
		emitStreamErrorEvent(msg)
		return fmt.Errorf("session runs on %s; its Claude transcript is not on this machine", resolvedInst.SSHHost)
	}

	var jsonlPath string
	// peers carries the latest profile snapshot so the resolve can refuse a
	// transcript path that collides with another live instance's session id
	// (issue #1349 defense-in-depth #2): streaming the wrong transcript is one
	// of the corruption symptoms the rebind bug caused.
	var peers []*session.Instance
	if _, initial, _, loadErr := loadSessionData(profile); loadErr == nil {
		peers = initial
	}
	deadline := time.Now().Add(opts.timeout)
	for time.Now().Before(deadline) {
		p, resolveErr := resolvedInst.GetJSONLPathChecked(peers)
		if resolveErr != nil {
			msg := fmt.Sprintf("refusing to stream a colliding transcript: %v", resolveErr)
			emitStreamErrorEvent(msg)
			return errors.New(msg)
		}
		jsonlPath = p
		if jsonlPath != "" {
			break
		}
		// Refresh from DB in case the session was just created.
		if _, freshInstances, _, loadErr := loadSessionData(profile); loadErr == nil {
			peers = freshInstances
			if fi, _, _ := ResolveSession(sessionRef, freshInstances); fi != nil {
				resolvedInst = fi
			}
		}
		time.Sleep(200 * time.Millisecond)
	}

	if jsonlPath == "" {
		// Emit a single error event to stdout so --stream consumers
		// always get a parseable response. Matches the schema so they
		// don't need a separate error channel.
		emitStreamErrorEvent(fmt.Sprintf("session transcript not found within %s (session id=%s)", opts.timeout, resolvedInst.ClaudeSessionID))
		return fmt.Errorf("no transcript")
	}

	return session.StreamTranscript(ctx, jsonlPath, resolvedInst.ClaudeSessionID, sentAt, os.Stdout, cfg)
}
