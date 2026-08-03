package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/verify"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// contextVerifyOptions are the knobs `--verify` exposes.
type contextVerifyOptions struct {
	// Yes skips the interactive confirmation. It exists for CI, and it is the
	// only way to run the comparison unattended: the default always asks,
	// because the comparison types into a live session.
	Yes bool
	// Timeout bounds the wait for the harness's panel to render.
	Timeout time.Duration
	// Tolerance is the band a row may differ by before it is graded as drift.
	Tolerance verify.Tolerance
	// ReadyWait bounds the hold before anything is typed, while the agent is
	// still working. Zero uses [defaultContextVerifyReadyWait].
	ReadyWait time.Duration
}

// Timings for the pre-send pipeline. `session send` has run this pipeline since
// issues #1409 and #966; --verify used to type straight into the pane.
const (
	// defaultContextVerifyReadyWait bounds the wait for the agent to stop
	// working. It is generous because the alternative to waiting is landing a
	// slash command in the middle of somebody's turn.
	defaultContextVerifyReadyWait = 2 * time.Minute
	// contextVerifySlashWait bounds the extra hold-back for Claude's
	// slash-command parser (#966): a bare "/context" typed after the composer
	// renders but before the parser registers is silently dropped, and the
	// comparison then waits out its whole budget for a panel nobody asked for.
	contextVerifySlashWait = 10 * time.Second
	// The composer-draft guard's bounds (#1409). They match the `session send`
	// tuning: hold briefly for an operator draft to clear on its own, then save
	// it and Ctrl+C the composer so the automated keystrokes cannot merge with
	// half-typed operator input and be submitted as one prompt.
	contextVerifyGuardHold      = 2 * time.Second
	contextVerifyGuardPoll      = 150 * time.Millisecond
	contextVerifyGuardClearWait = 1 * time.Second
)

// contextVerifyJSON is the machine surface of `session context --verify --json`.
type contextVerifyJSON struct {
	SchemaVersion int                `json:"schema_version"`
	Session       contextSessionJSON `json:"session"`
	// Command is the slash command that was sent to the live session.
	Command string `json:"command"`
	// Parity is the comparison. Nil when the panel could not be read.
	Parity *verify.Parity `json:"parity,omitempty"`
	// Report is the agent-deck report that was compared.
	Report *ctxinspect.Report `json:"report"`
	// Warnings record what the comparison had to do to the session that the
	// user did not ask for — chiefly an operator draft that was cleared out of
	// the composer and could not be typed back.
	Warnings []string `json:"warnings,omitempty"`
	// Error is set when the comparison could not be performed.
	Error string `json:"error,omitempty"`
}

// runContextVerify performs the live parity check and returns the process exit
// code.
//
// This is the one part of the context inspector that mutates: it types the
// harness's own accounting command into the user's session and presses Enter.
// It is therefore an explicit verb with an explicit confirmation, it is never
// reached from the TUI, and it refuses rather than guesses whenever the panel
// cannot be read.
func runContextVerify(out *CLIOutput, v contextView, sess contextLiveSession, opts contextVerifyOptions, jsonMode bool) int {
	rep := v.Report

	spec, ok := verify.SpecForAdapter(rep.Adapter)
	if !ok {
		err := verify.ErrUnverifiable{Adapter: rep.Adapter}
		emitContextVerifyFailure(out, v, "", err, jsonMode)
		return contextExitError
	}

	if sess == nil {
		emitContextVerifyFailure(out, v, spec.Command,
			fmt.Errorf("session %q has no live pane: the comparison sends %s to a running agent, so the session must be running", v.Title, spec.Command),
			jsonMode)
		return contextExitError
	}

	if !opts.Yes {
		if !confirmContextVerify(os.Stdin, os.Stdout, v, spec) {
			out.Error("verification cancelled: nothing was sent to the session", ErrCodeInvalidOperation)
			return contextExitError
		}
	}

	// The comparison types into a pane the operator may be using right now.
	// That is the hazard internal/send/guard.go exists for (issue #1409), and
	// running it here rather than sending blind is the difference between
	// "agent-deck asked the agent for its panel" and "agent-deck appended
	// /context to the operator's half-written prompt and submitted it".
	draft, err := awaitContextVerifySendable(sess, v.Tool, opts.ReadyWait)
	if err != nil {
		emitContextVerifyFailure(out, v, spec.Command, err, jsonMode)
		return contextExitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout+contextVerifyGrace)
	defer cancel()

	harness, runErr := verify.RunLive(ctx, spec, sess, sess, verify.LiveOptions{Timeout: opts.Timeout})
	warnings := restoreContextVerifyDraft(sess, draft)
	if runErr != nil {
		emitContextVerifyFailure(out, v, spec.Command, runErr, jsonMode)
		emitContextVerifyWarnings(warnings, jsonMode)
		return contextExitError
	}

	parity := verify.Compare(rep, harness, spec, opts.Tolerance)

	if jsonMode {
		out.Print("", contextVerifyJSON{
			SchemaVersion: contextSchemaVersion,
			Session:       contextSessionJSON{ID: rep.SessionID, Title: v.Title, Profile: v.Profile, Tool: v.Tool, Path: rep.ProjectPath, Ref: v.Ref},
			Command:       spec.Command,
			Parity:        parity,
			Report:        rep,
			Warnings:      warnings,
		})
	} else {
		out.Print(renderContextParity(v, spec, parity), nil)
		emitContextVerifyWarnings(warnings, jsonMode)
	}

	switch parity.Status {
	case verify.StatusDrift:
		return contextExitDrift
	case verify.StatusIndeterminate:
		return contextExitIndeterminate
	default:
		return contextExitOK
	}
}

// awaitContextVerifySendable holds until the live session can actually accept a
// typed slash command, and moves an operator draft out of the way if one is
// sitting in the composer.
//
// It is the same pre-send pipeline `session send` runs, for the same reasons:
//
//   - a busy agent has no composer to type into, so the keystrokes land in
//     whatever it draws next (send.WaitForAgentReady);
//   - Claude registers its slash-command parser some hundreds of milliseconds
//     after the composer first renders, and a bare "/context" typed in that
//     window is silently dropped (#966);
//   - a composer already holding half-typed operator input merges the
//     automated keystrokes into that input and submits the pair as one prompt
//     (#1409). The guard holds briefly, then saves and clears the draft.
//
// The saved draft is returned so the caller can type it back — without Enter —
// once the comparison's own keystrokes have been delivered.
func awaitContextVerifySendable(sess contextLiveSession, tool string, readyWait time.Duration) (savedDraft string, err error) {
	if readyWait <= 0 {
		readyWait = defaultContextVerifyReadyWait
	}
	claudeLike := session.IsClaudeCompatible(tool)

	if err := send.WaitForAgentReady(sess, tool, readyWait, send.PromptGates{
		ClaudeComposer: claudeLike,
		CodexPrompt:    session.IsCodexCompatible(tool),
	}); err != nil {
		return "", fmt.Errorf("the session is still working, so there is nothing safe to type into: %w\n"+
			"agent-deck will not drop a slash command into a mid-turn composer. Re-run when the session is idle", err)
	}

	if !claudeLike {
		// Composer introspection is Claude-shaped; other tools gate readiness
		// upstream and have no draft to protect.
		return "", nil
	}

	if err := waitForSlashCommandReady(sess, tool, contextVerifySlashWait); err != nil {
		return "", fmt.Errorf("%w\nThe composer is up but the agent has not registered its slash commands yet; a command sent now would be swallowed", err)
	}

	guard := send.GuardComposerDraft(sess, send.ComposerGuardOptions{
		HoldWait:     contextVerifyGuardHold,
		PollInterval: contextVerifyGuardPoll,
		ClearWait:    contextVerifyGuardClearWait,
		Strip:        tmux.StripANSI,
	})
	return guard.SavedDraft, nil
}

// restoreContextVerifyDraft types a saved operator draft back into the
// composer, without pressing Enter, and returns what the user must be told.
//
// A failure here is never silent: the guard cleared text the operator wrote,
// and if it cannot be put back the only honest thing to do is print it.
func restoreContextVerifyDraft(sess contextLiveSession, draft string) []string {
	if draft == "" {
		return nil
	}
	if err := sess.SendKeysChunked(draft); err != nil {
		return []string{fmt.Sprintf(
			"your unsent composer draft was cleared to make room for the verification command and could not be typed back (%v). It was:\n%s", err, draft)}
	}
	return nil
}

// emitContextVerifyWarnings prints warnings on the human path. In JSON mode
// they travel in the document instead, so this is a no-op there.
func emitContextVerifyWarnings(warnings []string, jsonMode bool) {
	if jsonMode {
		return
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
}

// contextVerifyGrace is how much longer the context deadline runs than the
// panel wait, so a timeout is reported as "the panel never rendered" rather
// than as a context cancellation.
const contextVerifyGrace = 5 * time.Second

// contextLiveSession is the live-pane surface the comparison needs.
// *tmux.Session satisfies it; a test supplies a scripted fake.
//
// It is deliberately wider than "read the pane and type into it": everything
// beyond that is the pre-send pipeline. GetStatus and CapturePaneFresh drive
// the readiness barrier, SendCtrlC lets the composer-draft guard clear an
// operator draft, and SendKeysChunked types that draft back afterwards. A
// narrower interface here is what let --verify type into a live pane with no
// guard at all.
type contextLiveSession interface {
	verify.PaneCapturer
	verify.KeySender
	// GetStatus reports whether the agent is working. With CapturePaneFresh it
	// satisfies send.AgentReadyChecker.
	GetStatus() (string, error)
	// SendCtrlC clears the composer. With CapturePaneFresh it satisfies
	// send.ComposerGuardTarget.
	SendCtrlC() error
	// SendKeysChunked types without submitting, used to restore a saved draft.
	SendKeysChunked(string) error
}

// contextLivePane returns the instance's live pane, or a nil interface when the
// session is not running.
//
// The nil is returned explicitly rather than by handing back a nil
// *tmux.Session: a typed nil in an interface is non-nil, and the caller's
// "is there a pane" check would pass before dereferencing it.
func contextLivePane(inst *session.Instance) contextLiveSession {
	if inst == nil {
		return nil
	}
	ts := inst.GetTmuxSession()
	if ts == nil || !ts.Exists() {
		return nil
	}
	return ts
}

// emitContextVerifyFailure reports a comparison that could not be performed.
// It never degrades into an empty parity table: "we could not check" and "we
// checked and everything agreed" must not print the same thing.
func emitContextVerifyFailure(out *CLIOutput, v contextView, command string, cause error, jsonMode bool) {
	if jsonMode {
		out.Print("", contextVerifyJSON{
			SchemaVersion: contextSchemaVersion,
			Session:       contextSessionJSON{ID: v.Report.SessionID, Title: v.Title, Profile: v.Profile, Tool: v.Tool, Path: v.Report.ProjectPath, Ref: v.Ref},
			Command:       command,
			Report:        v.Report,
			Error:         cause.Error(),
		})
		return
	}
	out.Error(contextVerifyFailureMessage(command, cause), ErrCodeInvalidOperation)
}

// contextVerifyFailureMessage explains a failed comparison in the terms that
// tell the reader what to do about it.
func contextVerifyFailureMessage(command string, cause error) string {
	switch {
	case errors.Is(cause, verify.ErrNoAccounting):
		return fmt.Sprintf("could not read the harness's own accounting: %v\n"+
			"The panel did not parse. Either this harness version renders %s differently, or the pane was captured mid-draw.\n"+
			"agent-deck will not report a comparison it could not make.", cause, command)
	case errors.Is(cause, verify.ErrPanelTimeout):
		return fmt.Sprintf("%v\nThe agent may have been busy. Re-run when the session is idle.", cause)
	default:
		return fmt.Sprintf("live verification failed: %v", cause)
	}
}

// confirmContextVerify asks for consent before typing into a live session.
//
// It states the mutation in concrete terms — the exact command, the exact
// session — because "verify the context" does not sound like "send a message to
// my agent", and the user must be agreeing to the second thing. It also names
// what the agreement costs when the session is in use: agent-deck waits for the
// agent to go idle and moves an unsent draft aside, but the command still joins
// that conversation permanently, and the guard is a mitigation rather than a
// promise. A non-terminal stdin returns EOF, which is read as "no": an
// unattended run must use --yes deliberately rather than be silently consented
// to.
func confirmContextVerify(in io.Reader, w io.Writer, v contextView, spec verify.Spec) bool {
	name := firstNonEmpty(v.Title, v.Ref, v.Report.SessionID, "this session")
	fmt.Fprintf(w, "\nLive verification sends %s to the running session %q and reads the panel back.\n", spec.Command, name)
	fmt.Fprintf(w, "This types into the agent's composer and presses Enter — it becomes part of that session's conversation.\n")
	fmt.Fprintf(w, "It is a live session: agent-deck waits for the agent to finish what it is doing and moves any unsent\n")
	fmt.Fprintf(w, "draft out of the composer first, but if you are typing while it runs the command can still land\n")
	fmt.Fprintf(w, "mid-turn, and it cannot be taken back out of the conversation afterwards.\n")
	fmt.Fprintf(w, "%s\n", spec.Note)
	fmt.Fprintf(w, "\nSend %s to %q now? [y/N] ", spec.Command, name)

	reader := bufio.NewReader(in)
	answer, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(answer) == "" {
		fmt.Fprintln(w)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
