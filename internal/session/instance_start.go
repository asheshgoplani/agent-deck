package session

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// buildTmuxOptionOverrides returns tmux option overrides from user config,
// adding remain-on-exit for sandbox sessions (needed for dead-pane detection).
// Returns nil if no overrides apply.
func (i *Instance) buildTmuxOptionOverrides() map[string]string {
	var overrides map[string]string
	tmuxCfg := GetTmuxSettings()
	if len(tmuxCfg.Options) > 0 {
		overrides = maps.Clone(tmuxCfg.Options)
	}
	if tmuxCfg.WindowStyleOverride != "" {
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides["window-style"] = tmuxCfg.WindowStyleOverride
		overrides["window-active-style"] = tmuxCfg.WindowStyleOverride
	}
	// Sandbox sessions need remain-on-exit so dead-pane detection works.
	// Non-sandbox sessions use default tmux behaviour (pane closes on exit).
	//
	// A documented one-shot needs it for a different reason: the process prints
	// its answer and exits BY DESIGN, and without remain-on-exit tmux tears the
	// pane down with the answer still in it — the user asked a question and got
	// a closed window. Keeping the pane is what makes a one-shot readable.
	if i.IsSandboxed() || i.expectsFastExit() || i.tracksCommandExit() {
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides["remain-on-exit"] = "on"
	}
	return overrides
}

// adoptExplicitClaudeSessionID adopts an explicit `--session-id <uuid>` baked
// into i.Command as the authoritative conversation id, correcting any stale or
// disk-hijacked value, and returns true when an explicit id was present (the
// caller must then skip mtime-based disk discovery). An explicit id is the
// user's declaration of WHICH conversation this session owns; honoring it
// before disk discovery is what stops sibling sessions in a shared cwd from
// converging on the newest transcript. Shared by both session-id preludes —
// ensureClaudeSessionIDFromDiskForRestart (#1147) and
// ensureClaudeSessionIDFromDisk (#1465). The reason label distinguishes the
// Start path from the Restart path in the resume log line.
func (i *Instance) adoptExplicitClaudeSessionID(reason string) bool {
	explicit, ok := extractExplicitClaudeSessionID(i.Command)
	if !ok {
		// #1815: a custom command carrying `claude --resume <uuid>` executes
		// verbatim, so without adopting that id the instance would reach the
		// spawn with an empty id and never meet the chokepoint at all.
		// Baking it into this session's own command is the same ownership
		// declaration as --session-id.
		//
		// Review finding on #1830: but ONLY when the command is not shaped
		// like the fork builder's `--resume <SOURCE id> --fork-session`
		// (also reachable via a legacy `--session-id "$VAR"` spelling that
		// makes the --session-id extraction above bail without a UUID,
		// falling through here). Any `--fork-session` or `--session-id`
		// token present means the --resume argument names a DIFFERENT
		// session's (the fork source's) conversation, not this instance's
		// own -- adopting it as verified would be exactly the
		// child-resumes-parent's-conversation shape #1815 exists to stop.
		if !commandHasToken(i.Command, "--fork-session") && !commandHasToken(i.Command, "--session-id") {
			explicit, ok = extractExplicitClaudeResumeID(i.Command)
		}
	}
	if !ok {
		return false
	}
	if i.ClaudeSessionID != explicit {
		i.ClaudeSessionID = explicit
		// #1815 / CodeQL go/log-injection: explicit is parsed out of this
		// session's own command string, which is session-supplied; reason is
		// always one of this file's own string-literal call sites, never a
		// variable from outside, so it needs no sanitizing.
		safeExplicit := logging.SanitizeValue(explicit)
		sessionLog.Info("resume: id="+safeExplicit+" reason="+reason,
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("claude_session_id", safeExplicit),
			slog.String("reason", reason))
	}
	// #1815: vouch AFTER the assignment, so the suspicion cleared is the one
	// on the EXPLICIT value — clearing before would absolve whatever id
	// happened to be current and leave the explicit one's history intact.
	i.markClaudeSessionIDVerified()
	if i.ClaudeDetectedAt.IsZero() {
		i.ClaudeDetectedAt = time.Now()
	}
	return true
}

// ensureClaudeSessionIDFromDisk is the Phase 5 / REQ-7 prelude invoked by
// Start() and StartWithMessage() when an IsClaudeCompatible Instance has an
// empty ClaudeSessionID. It attempts to discover the latest UUID-named
// JSONL under Claude's projects dir for i.ProjectPath and, on success,
// mutates i.ClaudeSessionID to that UUID IN PLACE so the very next check
// (`if i.ClaudeSessionID != ""`) naturally routes through
// buildClaudeResumeCommand, reusing the Phase 3 resume contract verbatim.
//
// PERSIST-11: runs uniformly on every empty-ID Claude-compatible start —
// no branch on i.Command (custom wrapper vs default wrapper), matching
// spec REQ-7 acceptance item 7 and CONTEXT D-04.
//
// PERSIST-12: the write-through onto i.ClaudeSessionID is in-memory; the
// external storage layer (TUI main loop / CLI save cycle / storage
// watcher at internal/session/storage.go) picks up the mutation on its
// next save — identical to how Phase 3's buildClaudeCommand at
// instance.go:567 mutates i.ClaudeSessionID without an inline save.
//
// PERSIST-13: returns quietly (no error, no log) when no JSONL is found.
// Start() then falls through to its existing fresh-session branch.
//
// D-07: emits exactly ONE `resume: id=<uuid> reason=jsonl_discovery`
// sessionLog.Info line on discovery success. buildClaudeResumeCommand
// downstream will emit its OWN `resume: id=<uuid> reason=...` line with
// reason=conversation_data_present or reason=session_id_flag_no_jsonl —
// two grep-stable lines for a Phase 5 discovery start, distinguishable
// by the `reason=` attr.
func (i *Instance) ensureClaudeSessionIDFromDisk() {
	// Issue #1465: an explicit `--session-id <uuid>` baked into i.Command is
	// the authoritative conversation id. Adopt it before the #608 gate and
	// disk discovery so a Start-path session sharing a cwd with newer sibling
	// transcripts (e.g. a removed-then-recreated review session) cannot hijack
	// a sibling's conversation. The Restart prelude already does this for
	// #1147; this closes the same gap on the Start path.
	// Issue #1851: the same bail-out the Restart variant needs, placed before
	// every disk touch below. The #608 gate further down makes the Start path
	// only ACCIDENTALLY safe for remote sessions — it is re-opened by
	// `session set <id> claude-session-id X` (mutators.go), which stamps
	// ClaudeDetectedAt, after which a later clear leaves a remote session
	// eligible for the local mtime walk. The rule is unconditional: a local
	// ~/.claude/projects lookup is never right for a conversation that lives on
	// another host.
	if !i.TranscriptIsResolvableLocally() {
		sessionLog.Info("resume: skipped reason=remote_session",
			slog.String("instance_id", i.ID),
			slog.String("ssh_host", i.SSHHost),
			slog.String("ssh_remote_path", i.SSHRemotePath),
			slog.String("reason", "jsonl_discovery_remote_skip"))
		return
	}
	explicitSessionID := i.adoptExplicitClaudeSessionID("session_id_flag_explicit")
	if i.Tool == "claude" && i.ClaudeSessionID != "" {
		if restored, err := RestoreOrphanedConversationBackup(i, GetClaudeConfigDirForInstance(i)); err == nil && restored != "" {
			sessionLog.Info("resume: restored orphaned conversation backup id="+i.ClaudeSessionID+" reason=orphan_bak_restore",
				slog.String("instance_id", i.ID),
				slog.String("claude_session_id", i.ClaudeSessionID),
				slog.String("path", restored),
				slog.String("reason", "orphan_bak_restore"))
		}
	}
	if explicitSessionID {
		return
	}
	if i.ClaudeSessionID != "" {
		return
	}
	// Fix for https://github.com/asheshgoplani/agent-deck/issues/608:
	// Only attempt JSONL discovery for sessions that previously had a
	// conversation (restart recovery). A zero ClaudeDetectedAt means this
	// session has never been started before — it should get a fresh
	// conversation, not resume another session's history from the same
	// directory.
	if i.ClaudeDetectedAt.IsZero() {
		return
	}
	// Issue #663: multi-repo sessions write their JSONL under
	// ~/.claude/projects/<encoded MultiRepoTempDir>/. ProjectPath is a
	// symlink inside MultiRepoTempDir, so EvalSymlinks would resolve it
	// to the original source repo and miss the JSONL. Use
	// EffectiveWorkingDir() so the encoded-path key matches what Claude
	// actually wrote on the first boot.
	lookupPath := i.EffectiveWorkingDir()
	uuid, found := discoverLatestClaudeJSONL(lookupPath)
	if !found {
		return
	}
	// #1815: discovery returns the newest transcript in this directory, which
	// in a shared directory can belong to another session. Record it as
	// UNVERIFIED so the resume-time identity guard refuses to --resume it.
	i.adoptDiscoveredClaudeSessionID(uuid)
	safeUUID := logging.SanitizeValue(uuid)
	sessionLog.Info("resume: id="+safeUUID+" reason=jsonl_discovery",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("claude_session_id", safeUUID),
		slog.String("path", logging.SanitizeValue(lookupPath)),
		slog.String("reason", "jsonl_discovery"))
}

// sendMessageWhenReady waits for the agent to be ready and sends the message.
// Uses the shared WaitForAgentReady helper (same semantics as `session send`).
func (i *Instance) sendMessageWhenReady(message string) error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	if err := send.WaitForAgentReady(i.tmuxSession, i.Tool, send.DefaultAgentReadyTimeout, send.PromptGates{
		ClaudeComposer: IsClaudeCompatible(i.Tool),
		CodexPrompt:    IsCodexCompatible(i.Tool),
	}); err != nil {
		return fmt.Errorf("timeout waiting for agent to be ready")
	}

	// Pre-send provenance probe for the #1777 attribution gate: Claude
	// collapses a bulk paste behind "[Pasted text #N +M lines]", so the
	// composer body can no longer be matched against our payload. Observing
	// an unmarked composer immediately before typing is the evidence that a
	// marker seen afterwards is OUR collapse and not a foreign paste parked
	// there. A capture failure leaves it false, and the gate then withholds
	// the nudge.
	//
	// The same capture tells whether the composer shows a Claude prompt
	// suggestion. Typing replaces it; the Enter after typing is then gated
	// on the composer holding our message rather than the suggestion.
	composerPasteFreeBeforeSend, suggestionShownBeforeSend := send.ProbeComposerBeforeSend(i.tmuxSession.CapturePaneFresh, UsesClaudeDeliveryVerify(i.Tool))

	// Issue #2079: a large or slow-mounting composer can swallow the leading
	// bytes of a paste, submitting a truncated tail that the agent answers as
	// if it were the whole prompt — and every downstream signal (composer
	// empty, agent went active) reads identically to a clean delivery. For a
	// Claude-compatible target the multi-line transport collapses whatever
	// landed behind a "[Pasted text #N +M lines]" marker whether the paste
	// arrived whole or was cut short, so the declared M is checked against the
	// message's hard line-break count BEFORE Enter is pressed — the only point
	// after which a truncated fragment could still be caught rather than
	// already being submitted. A mismatch withholds Enter and fails the send
	// loudly instead of silently reporting success on a fragment.
	//
	// M is Claude's count of hard line breaks in the paste (a 6-line prompt
	// shows "+5 lines"), not a line count and not the rows the pane wraps it
	// to; comparing it with anything else refuses intact prompts (the rc.3
	// launch regression). send.CheckPasteMarker owns that comparison.
	//
	// A deferred submit (the composer still painted the suggestion after
	// typing) keeps the paste truncation check: send.TypeAndSubmit runs it on
	// the frame it submits.
	expectedBreaks := 0
	if UsesClaudeDeliveryVerify(i.Tool) {
		expectedBreaks = send.ExpectedPasteMarkerLineBreaks(message)
	}
	sendErr := send.TypeAndSubmit(i.tmuxSession, message, expectedBreaks, suggestionShownBeforeSend)
	if sendErr != nil {
		return fmt.Errorf("failed to send message: %w", sendErr)
	}

	// The verify loop below keys off Claude-specific signals (an
	// "active" transition, composer glyph, unsent-paste markers). Non-
	// Claude tools never surface those, so the loop false-negatives a
	// delivered message and Enter-spams the composer; skip it for every
	// non-Claude tool (#1238 — generalizes #1228's codex-only skip).
	if !UsesClaudeDeliveryVerify(i.Tool) {
		return nil
	}

	// Verify the agent accepted Enter and began processing.
	const verifyRetries = 50
	const verifyDelay = 300 * time.Millisecond
	const activeSuccessThreshold = 2
	const waitingAfterActiveThreshold = 2
	waitingNoMarkerChecks := 0
	activeChecks := 0
	sawActiveAfterSend := false
	// attrib is the #1777 attribution gate. EVERY bare Enter below —
	// including the unsent-prompt branch, which used to press unconditionally
	// whenever a "[Pasted text …]" marker appeared anywhere in the pane —
	// routes through attrib.NudgeEnter, so no branch can submit composer
	// content agent-deck cannot attribute to its own delivery.
	attrib := send.EnterAttribution{
		Message:        message,
		OwnPasteMarker: composerPasteFreeBeforeSend,
	}

	for retry := 0; retry < verifyRetries; retry++ {
		time.Sleep(verifyDelay)

		unsentPromptDetected := false
		// paneNow is this iteration's observation (raw ANSI + whether the
		// capture succeeded at all), and is what the attribution gate reads.
		captured, captureErr := i.tmuxSession.CapturePaneFresh()
		paneNow := send.CaptureOutcome(captured, captureErr)
		if paneNow.OK {
			unsentPromptDetected = send.ComposerHoldsPasteMarker(captured, tmux.StripANSI) || send.ComposerHoldsUnsentMessage(captured, message, tmux.StripANSI)
		}
		verifiedStatus, statusErr := i.tmuxSession.GetStatus()

		if unsentPromptDetected {
			waitingNoMarkerChecks = 0
			activeChecks = 0
			attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
			continue
		}

		if statusErr == nil && verifiedStatus == "active" {
			sawActiveAfterSend = true
			waitingNoMarkerChecks = 0
			activeChecks++
			if activeChecks >= activeSuccessThreshold {
				return nil
			}
			continue
		}
		activeChecks = 0

		if statusErr == nil && (verifiedStatus == "waiting" || verifiedStatus == "idle") {
			if sawActiveAfterSend {
				waitingNoMarkerChecks++
				if waitingNoMarkerChecks >= waitingAfterActiveThreshold {
					return nil
				}
			} else {
				waitingNoMarkerChecks = 0
				if retry < 5 || retry%2 == 0 {
					attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
				}
			}
			continue
		}

		waitingNoMarkerChecks = 0
		if retry < 4 {
			attrib.NudgeEnter(i.tmuxSession, paneNow, tmux.StripANSI)
		}
	}

	return nil
}

// WaitForClaudeSession waits for the tmux environment variable to be set.
// The capture-resume pattern sets CLAUDE_SESSION_ID in tmux env, so we poll for that.
// Returns the detected session ID or empty string after timeout.
func (i *Instance) WaitForClaudeSession(maxWait time.Duration) string {
	if !IsClaudeCompatible(i.Tool) {
		return ""
	}

	// Poll every 200ms for up to maxWait
	interval := 200 * time.Millisecond
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		// Check tmux environment (set by capture-resume pattern)
		if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
			i.ClaudeSessionID = sessionID
			// #1815: own pane env is a WEAK vouch — see
			// noteClaudeSessionIDFromOwnPane.
			i.noteClaudeSessionIDFromOwnPane()
			i.ClaudeDetectedAt = time.Now()
			return sessionID
		}
		time.Sleep(interval)
	}

	return ""
}

// WaitForClaudeSessionWithExclude waits for the tmux environment variable to be set.
// The excludeIDs parameter is kept for API compatibility but not used since tmux env
// is authoritative and won't return duplicate IDs.
func (i *Instance) WaitForClaudeSessionWithExclude(maxWait time.Duration, excludeIDs map[string]bool) string {
	// tmux env is authoritative - no need for exclusion logic
	return i.WaitForClaudeSession(maxWait)
}

// PostStartSync captures session IDs from tmux environment after Start() or Restart().
// Designed for CLI commands that exit after starting. The TUI doesn't need this
// because its background worker handles detection.
//
// For Claude: polls tmux env for CLAUDE_SESSION_ID (set by bash uuidgen before exec).
// For Gemini: reads session ID from filesystem.
// For OpenCode/Codex: no-op (async goroutine detection, too slow for sync CLI).
func (i *Instance) PostStartSync(maxWait time.Duration) {
	switch {
	case IsClaudeCompatible(i.Tool):
		i.WaitForClaudeSession(maxWait)
		i.autoConfirmClaudeResumePicker()
	case i.Tool == "gemini":
		i.UpdateGeminiSession(nil)
	case i.Tool == "copilot":
		// Copilot uses async detection via detectCopilotSessionAsync().
		// If the session was not yet detected, attempt a quick sync check.
		if i.CopilotSessionID == "" {
			cwd := i.EffectiveWorkingDir()
			startedAfter := time.Now().Add(-30 * time.Second)
			if i.CopilotStartedAt > 0 {
				startedAfter = time.UnixMilli(i.CopilotStartedAt).Add(-2 * time.Second)
			}
			if sid := detectCopilotSessionFromDisk(cwd, startedAfter); sid != "" {
				i.CopilotSessionID = sid
				i.CopilotDetectedAt = time.Now()
				if i.tmuxSession != nil {
					_ = i.tmuxSession.SetEnvironment("COPILOT_SESSION_ID", sid)
				}
			}
		}
	}
	// OpenCode/Codex: async detection already started by Start(), skip here
}

// autoConfirmClaudeResumePicker handles the "Resume from summary" picker that
// claude --resume shows on long-running sessions (>~250k tokens). Without
// this, an unattended conductor sits frozen on the picker indefinitely.
// See issue #67. Disable via [claude].auto_resume_summary = false.
func (i *Instance) autoConfirmClaudeResumePicker() {
	if i.tmuxSession == nil {
		return
	}
	cfg, _ := LoadUserConfig()
	if cfg != nil && !cfg.Claude.GetAutoResumeSummary() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = autoResolveClaudeResumePicker(ctx, i.tmuxSession, i.tmuxSession, autoResumeOptions{
		PollInterval: 250 * time.Millisecond,
		Timeout:      3 * time.Second,
	})
}
