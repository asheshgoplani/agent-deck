package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// compactVerifyPoll is how often the transcript is re-read while waiting for the
// compaction to land. Compactions take tens of seconds to minutes (150s was
// measured on an 82k conversation), so a tight poll buys nothing.
const compactVerifyPoll = 2 * time.Second

// handleSessionCompact implements `agent-deck session compact`.
//
// Why this is a command and not a line in a runbook: `/compact` is the one
// context remedy an agent genuinely needs and cannot express as a normal
// message. Told to "run /compact", an autonomous session ends its turn and
// waits for a human — which is how orchestrate conductors were stalling. The
// keystrokes themselves work fine; what was missing was a caller that knows the
// three things that make it safe:
//
//  1. Compacting yourself must not wait for you to be idle. The generic send
//     path blocks until the target's composer is free, and a session waiting on
//     itself mid-turn waits forever.
//  2. A bare /compact leaves the session idle afterwards. Unattended callers
//     need work queued behind it or the run simply stops one step later.
//  3. Delivery status is not proof. It reports whether the keys were submitted,
//     and a queued compaction runs minutes later; the transcript's
//     compact_boundary record is the only positive evidence.
func handleSessionCompact(profile string, args []string) {
	fs := flag.NewFlagSet("session compact", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode")
	instructions := fs.String("instructions", "", "Custom compaction instructions passed to /compact (e.g. what must survive)")
	resume := fs.String("resume", "", "Message to deliver once the compaction has finished, so the session keeps working instead of going idle")
	noVerify := fs.Bool("no-verify", false, "Return as soon as the command is submitted, without waiting for the compaction to land")
	timeout := durationFlag(fs, "timeout", 5*time.Minute, "Max time to wait for the compaction to be recorded in the transcript")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session compact [<id|title>] [options]")
		fmt.Println()
		fmt.Println("Compact a Claude session's conversation. With no id, compacts the calling")
		fmt.Println("session — the case a supervising agent needs, and the one it cannot do by")
		fmt.Println("typing /compact itself.")
		fmt.Println()
		fmt.Println("Self-compaction is asynchronous by construction: the command is queued and")
		fmt.Println("runs after the current turn ends, so there is nothing to verify while the")
		fmt.Println("caller is still running. Pair it with --resume or the session goes idle.")
		fmt.Println()
		fmt.Println("--resume is delivered by a detached watcher once the compaction is recorded,")
		fmt.Println("never alongside it: a message arriving while a compaction starts CANCELS it,")
		fmt.Println("leaving the session at full context with the resume looking like it worked.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session compact")
		fmt.Println("  agent-deck session compact --instructions \"keep the task list and open questions\"")
		fmt.Println("  agent-deck session compact --resume 'bash \"$RUN_DIR/poll.sh\"'")
		fmt.Println("  agent-deck session compact my-project --timeout 10m")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()

	out := NewCLIOutput(*jsonOutput, *quiet)

	if len(remaining) > 1 {
		fs.Usage()
		out.Error("expected at most one session id or title", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Resolving self up front (not just when the arg is missing) is what lets an
	// explicit id that happens to be the caller take the self path too — an
	// agent that passes its own $AGENTDECK_INSTANCE_ID must not deadlock.
	selfID, selfErr := resolveSelfSessionID()
	sessionRef := strings.TrimSpace(strings.Join(remaining, ""))
	if sessionRef == "" || strings.EqualFold(sessionRef, "self") {
		if selfErr != nil {
			out.Error(selfErr.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		sessionRef = selfID
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

	// /compact is Claude's. Typing it at another tool puts a stray literal in
	// that agent's prompt, which is worse than refusing: it looks like it worked.
	if !session.IsClaudeCompatible(inst.Tool) {
		out.Error(fmt.Sprintf("session '%s' runs %s, which has no /compact", inst.Title, inst.Tool), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if !inst.Exists() {
		out.Error(fmt.Sprintf("session '%s' is not running", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		out.Error(fmt.Sprintf("session '%s' has no tmux session to send to", inst.Title), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	isSelf := selfErr == nil && inst.ID == selfID

	message := compactMessage(*instructions)

	// Baseline before the send, so a compaction that was already the newest
	// record in the transcript cannot be mistaken for the one we just asked for.
	var baseline *session.CompactBoundary
	if b, ok := session.LatestCompactBoundaryForInstance(inst); ok {
		baseline = &b
	}

	// A self-compact must never wait for readiness: the wait is for the target's
	// composer to be free, and the target is the caller, mid-turn. It would time
	// out at best and hang for the full timeout at worst.
	if !isSelf {
		if err := send.WaitForAgentReady(tmuxSess, inst.Tool, *timeout, send.PromptGates{
			ClaudeComposer: true,
		}); err != nil {
			out.Error(fmt.Sprintf("timeout waiting for agent: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		// Issue #966: after a restart Claude shows a composer before its
		// slash-command parser is armed, and a bare /foo in that window is
		// dropped with no error anywhere.
		slashTimeout := *timeout
		if slashTimeout <= 0 || slashTimeout > 10*time.Second {
			slashTimeout = 10 * time.Second
		}
		if err := waitForSlashCommandReady(tmuxSess, inst.Tool, slashTimeout); err != nil {
			out.Error(fmt.Sprintf("timeout waiting for slash-command registration: %v", err), ErrCodeInvalidOperation)
			os.Exit(1)
		}
	}

	tun := defaultSendTuning()
	if isSelf {
		tun = noWaitSendTuning()
	}
	sendRes, sendErr := executeSend(tmuxSess, inst.Tool, message, isSelf, tun)
	if sendErr != nil {
		extra := sendRes.jsonFields()
		extra["session_id"] = inst.ID
		extra["session_title"] = inst.Title
		out.ErrorWithData(fmt.Sprintf("failed to send /compact to '%s': %v", inst.Title, sendErr), ErrCodeDeliveryFailed, extra)
		os.Exit(1)
	}

	// The resume prompt must NOT be sent from here, and this is the sharp edge
	// of the whole feature: a message delivered while a compaction is starting
	// CANCELS it. Observed directly — the pane reads
	//
	//     ❯ /compact
	//       ⎿  Compaction canceled.
	//       ❯ <the resume message>
	//
	// and the run continues at full context having reclaimed nothing, while the
	// resume looks like it worked. So delivery is handed to a detached watcher
	// that waits for the compact_boundary record to appear and only then sends.
	resumeWatcher := false
	if s := strings.TrimSpace(*resume); s != "" {
		baselineUUID := ""
		if baseline != nil {
			baselineUUID = baseline.UUID
		}
		if err := spawnCompactResumeWatcher(profile, inst.ID, baselineUUID, s, *timeout); err != nil {
			// The compaction is already on its way; failing now would misreport
			// it as not having happened. An unresumed session is idle, not
			// broken — and on an orchestrate run the heartbeat watchdog picks it
			// up within the beat interval anyway.
			fmt.Fprintf(os.Stderr, "Warning: /compact was submitted to '%s' but the resume watcher did not start: %v\n", inst.Title, err)
			fmt.Fprintf(os.Stderr, "         That session will sit idle after compacting. Send it work with `agent-deck session send`.\n")
		} else {
			resumeWatcher = true
		}
	}

	data := map[string]interface{}{
		"success":       true,
		"session_id":    inst.ID,
		"session_title": inst.Title,
		"message":       message,
		"self":          isSelf,
		"resume_queued": resumeWatcher,
		"delivery":      sendRes.delivery,
	}

	// Self-compaction cannot be verified from inside the caller: the queued
	// command only runs once this turn ends, and this process is that turn.
	// Reporting "compacted: false" here would be a lie by omission, so the field
	// is left absent and the asynchrony is stated instead.
	if isSelf {
		data["queued"] = true
		data["verified"] = false
		data["note"] = "self-compact runs after the current turn ends; it cannot be verified from inside it"
		out.Success(fmt.Sprintf("Queued /compact for this session ('%s'); it runs when this turn ends", inst.Title), data)
		return
	}

	if *noVerify {
		data["verified"] = false
		out.Success(fmt.Sprintf("Submitted /compact to '%s' (not verified)", inst.Title), data)
		return
	}

	b, ok := waitForCompaction(inst, baseline, *timeout)
	data["verified"] = ok
	if !ok {
		data["success"] = false
		out.ErrorWithData(fmt.Sprintf(
			"/compact was submitted to '%s' but no compaction was recorded within %s. "+
				"It may still be queued behind a long turn — re-check with `agent-deck session show %s`",
			inst.Title, *timeout, inst.Title), ErrCodeInvalidOperation, data)
		os.Exit(1)
	}

	data["trigger"] = b.Trigger
	data["pre_tokens"] = b.PreTokens
	data["post_tokens"] = b.PostTokens
	data["reclaimed_tokens"] = b.Reclaimed()
	data["duration_ms"] = b.Duration.Milliseconds()
	out.Success(fmt.Sprintf("Compacted '%s': %dk → %dk tokens (%dk reclaimed) in %s",
		inst.Title, b.PreTokens/1000, b.PostTokens/1000, b.Reclaimed()/1000,
		b.Duration.Round(time.Second)), data)
}

// spawnCompactResumeWatcher starts a detached `session compact-watch` that
// delivers the resume message once the compaction is recorded.
//
// It has to be a separate process, not a goroutine: for a self-compact the
// compaction only runs after the calling agent's turn ends, and this process is
// part of that turn. Anything waiting in-process is dead before the event it is
// waiting for.
func spawnCompactResumeWatcher(profile, sessionID, baselineUUID, resume string, timeout time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{}
	if profile != "" {
		args = append(args, "-p", profile)
	}
	args = append(args, "session", "compact-watch", sessionID,
		"--baseline", baselineUUID,
		"--resume", resume,
		"--timeout", timeout.String())

	// #nosec G702 -- exe is the current signed/running executable and args are
	// passed as distinct tokens to the private compact-watch subcommand.
	cmd := exec.Command(exe, args...)
	// Setpgid detaches it from this process group, so the watcher survives the
	// CLI exiting and is not killed along with the turn that started it. Same
	// pattern the tmux and mcppool spawners use.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// Nothing ever calls Wait on it, so release the child to init rather than
	// leaving a zombie behind for as long as the parent lives.
	return cmd.Process.Release()
}

// handleSessionCompactWatch is the detached half of --resume: wait for a
// compaction newer than --baseline, then deliver --resume. Deliberately absent
// from `session` help — it is an implementation detail of `session compact`,
// not something to run by hand.
func handleSessionCompactWatch(profile string, args []string) {
	fs := flag.NewFlagSet("session compact-watch", flag.ExitOnError)
	fs.SetOutput(os.Stdout)
	baseline := fs.String("baseline", "", "UUID of the compaction that was newest before the /compact was sent")
	resume := fs.String("resume", "", "Message to deliver once a newer compaction is recorded")
	timeout := durationFlag(fs, "timeout", 5*time.Minute, "Give up if no compaction is recorded within this window")

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	remaining := fs.Args()
	if len(remaining) != 1 || strings.TrimSpace(*resume) == "" {
		fmt.Fprintln(os.Stderr, "usage: agent-deck session compact-watch <id> --resume <message> [--baseline <uuid>] [--timeout <d>]")
		os.Exit(1)
	}

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		os.Exit(1)
	}
	inst, _, _ := ResolveSession(remaining[0], instances)
	if inst == nil {
		os.Exit(2)
	}

	var base *session.CompactBoundary
	if *baseline != "" {
		base = &session.CompactBoundary{UUID: *baseline}
	}
	if _, ok := waitForCompaction(inst, base, *timeout); !ok {
		// No compaction landed. Delivering the resume anyway would push work into
		// a session still carrying the context the compaction was meant to
		// reclaim — the exact state the caller was trying to leave.
		os.Exit(1)
	}

	// Re-resolve: the compaction just rewrote the conversation, and the tmux
	// handle cached above predates it.
	_, instances, _, err = loadSessionData(profile)
	if err != nil {
		os.Exit(1)
	}
	inst, _, _ = ResolveSession(remaining[0], instances)
	if inst == nil || !inst.Exists() {
		os.Exit(2)
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		os.Exit(1)
	}
	if _, err := executeSend(tmuxSess, inst.Tool, *resume, false, defaultSendTuning()); err != nil {
		os.Exit(1)
	}
}

// compactMessage builds the literal the session will receive.
//
// Newlines are collapsed to spaces: the payload is typed into a composer as one
// line, and an embedded newline would submit the command early, leaving the rest
// of the instructions behind as a stray prompt after the compaction.
func compactMessage(instructions string) string {
	s := strings.TrimSpace(instructions)
	if s == "" {
		return "/compact"
	}
	s = strings.Join(strings.Fields(s), " ")
	return "/compact " + s
}

// waitForCompaction polls the transcript until a compaction newer than baseline
// appears, or the timeout expires.
func waitForCompaction(inst *session.Instance, baseline *session.CompactBoundary, timeout time.Duration) (session.CompactBoundary, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if b, ok := session.CompactedSince(inst, baseline); ok {
			return b, true
		}
		if !time.Now().Before(deadline) {
			return session.CompactBoundary{}, false
		}
		time.Sleep(compactVerifyPoll)
	}
}
