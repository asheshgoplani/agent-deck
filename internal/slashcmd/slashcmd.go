// Package slashcmd delivers a Claude Code client-side slash command
// (/model, /compact, /clear, /exit, any skill) to a live Claude pane and
// verifies the outcome from the screen.
//
// Typing a slash command blind is unsafe: the autocomplete picker can run a
// different entry than the one typed (a partial "/mod" + Enter opens /model),
// the composer may hold the operator's unsent text, and an empty composer may
// show a ghost suggestion. Run therefore proves the composer is empty and
// idle before typing (send.ClaudeComposerReady), proves the picker closed on
// exactly the typed text before pressing Enter, and reads the screen after
// every step. It never presses Enter on text it did not type.
package slashcmd

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Pane is the tmux surface Run drives. *tmux.Session satisfies it. Enter is
// sent as the raw named key: tmux.Session.SendEnter adds a vim insert-mode
// sequence (Escape, i) that would cancel a dialog and type "i".
type Pane interface {
	CapturePaneFresh() (string, error)
	SendKeys(text string) error
	SendNamedKey(key string) error
}

// Kind selects how the outcome is verified.
type Kind string

const (
	KindModel   Kind = "model"
	KindCompact Kind = "compact"
	KindClear   Kind = "clear"
	KindExit    Kind = "exit"
	KindGeneric Kind = "generic"
)

// Failure codes beyond the send.ComposerCode* preflight codes.
const (
	CodeInvalid        = "invalid_command"
	CodeUnknownCommand = "unknown_command"     // picker would run a different entry
	CodeTypeMismatch   = "type_mismatch"       // composer does not hold exactly our text
	CodeVerifyTimeout  = "verify_timeout"      // submitted, outcome not seen in time
	CodeCommandError   = "command_error"       // Claude printed an error
	CodeInteractive    = "interactive_command" // command opened a dialog; cancelled
	CodeEnterIgnored   = "enter_not_accepted"  // Enter left the command in the composer
	CodePaneGone       = "pane_gone"
	CodeKeys           = "send_failed"
	CodeRefused        = "refused_command" // never run remotely (/login, /logout)
	CodeDialogUnowned  = "dialog_open"     // a dialog is up and our echo is not visible; left untouched
)

// ErrRefused is returned by Parse for commands session command never runs:
// /login and /logout act on the Claude config dir, which every session using
// it shares, so one remote /logout logs out all of them.
var ErrRefused = errors.New("refused")

// Command is a parsed slash command.
type Command struct {
	Text string // normalized "/name args"
	Name string // "/name"
	Args string
	Kind Kind
}

var namePattern = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9_.:\-]*$`)

// Parse validates a single-line slash command.
func Parse(s string) (Command, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return Command{}, errors.New("slash command must be one line without control characters")
		}
	}
	name, args, _ := strings.Cut(s, " ")
	if !namePattern.MatchString(name) {
		return Command{}, fmt.Errorf("not a slash command: %q (want /name [args])", s)
	}
	args = strings.TrimSpace(args)
	switch name {
	case "/login", "/logout":
		return Command{}, fmt.Errorf("%w: %s changes the login of the shared Claude config dir, which logs every session using it in or out; run it yourself in that session", ErrRefused, name)
	}
	cmd := Command{Name: name, Args: args, Text: strings.TrimSpace(name + " " + args), Kind: KindGeneric}
	switch name {
	case "/model":
		cmd.Kind = KindModel
	case "/compact":
		cmd.Kind = KindCompact
	case "/clear":
		cmd.Kind = KindClear
	case "/exit", "/quit":
		cmd.Kind = KindExit
	}
	return cmd, nil
}

// Options bounds every wait. Zero values get defaults.
type Options struct {
	Timeout  time.Duration // outcome verification budget after Enter
	WaitIdle time.Duration // how long to wait for an empty, idle composer
	TypeWait time.Duration // how long the typed text may take to render
	Poll     time.Duration
	Settle   time.Duration // pause after each keystroke before reading
	DryRun   bool
	Sleep    func(time.Duration)
}

func (o *Options) defaults() {
	if o.Timeout <= 0 {
		o.Timeout = 120 * time.Second
	}
	if o.TypeWait <= 0 {
		o.TypeWait = 2 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	if o.Settle <= 0 {
		o.Settle = 300 * time.Millisecond
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
}

// Result is the JSON contract of `agent-deck session command`.
type Result struct {
	OK       bool     `json:"ok"`
	Session  string   `json:"session,omitempty"`
	ID       string   `json:"session_id,omitempty"`
	Command  string   `json:"command"`
	Kind     Kind     `json:"kind"`
	Before   string   `json:"before"`
	After    string   `json:"after"`
	Evidence []string `json:"evidence"`
	Steps    []string `json:"steps"`
	Plan     []string `json:"plan,omitempty"`
	Code     string   `json:"code,omitempty"`
	Error    string   `json:"error,omitempty"`
	Queued   bool     `json:"queued,omitempty"`
	Result   string   `json:"result_file,omitempty"`
}

type runner struct {
	p         Pane
	cmd       Command
	opts      Options
	res       Result
	confirmed bool // accepted the /model "Switch model?" confirmation
}

// Run delivers cmd to the pane and verifies the outcome.
func Run(p Pane, cmd Command, opts Options) Result {
	opts.defaults()
	r := &runner{p: p, cmd: cmd, opts: opts, res: Result{Command: cmd.Text, Kind: cmd.Kind, Evidence: []string{}, Steps: []string{}}}
	return r.run()
}

func (r *runner) step(s string) { r.res.Steps = append(r.res.Steps, s) }

func (r *runner) fail(code, msg string, evidence ...string) Result {
	r.res.OK, r.res.Code, r.res.Error = false, code, msg
	r.res.Evidence = append(r.res.Evidence, evidence...)
	return r.res
}

func (r *runner) typed() string {
	if r.cmd.Args == "" {
		// The space closes the picker on an exact command match (live 2.1.296).
		return r.cmd.Text + " "
	}
	return r.cmd.Text
}

func (r *runner) run() Result {
	raw, ready := r.waitReady()
	if !ready.Ready {
		return r.fail(ready.Code, preflightMessage(ready))
	}
	content := tmux.StripANSI(raw)
	r.res.Before = r.observeBefore(content)
	if ready.Ghost {
		r.step("preflight: composer empty (ghost suggestion shown; typing replaces it), idle")
	} else {
		r.step("preflight: composer empty, idle")
	}

	if r.opts.DryRun {
		r.res.OK = true
		r.res.Plan = []string{
			fmt.Sprintf("type %q", r.typed()),
			"read the screen: composer must hold exactly the typed text and the picker must be closed, else Escape + Ctrl+U and refuse",
			"press Enter",
			"verify: " + verifyDescription(r.cmd.Kind),
		}
		return r.res
	}

	if err := r.p.SendKeys(r.typed()); err != nil {
		return r.fail(CodeKeys, "typing failed: "+err.Error())
	}
	r.step(fmt.Sprintf("typed %q", r.typed()))
	if res, ok := r.confirmTyped(); !ok {
		return res
	}

	// Last look before Enter, on one capture: still exactly our text, picker
	// closed, no turn started. Anything else is never submitted.
	raw, err := r.p.CapturePaneFresh()
	if err != nil {
		return r.fail(CodePaneGone, "pane unreadable before submit: "+err.Error())
	}
	baseline := tmux.StripANSI(raw)
	if input := composerInput(raw); input != send.NormalizePromptText(r.typed()) || readPicker(baseline).open {
		return r.fail(CodeTypeMismatch, "composer changed right before Enter; "+r.clearIfOwnText(input)+", nothing submitted", fmt.Sprintf("composer: %q", input))
	}
	if send.ClaudeTurnRunning(raw) {
		// No Escape here: during a turn it interrupts the turn.
		return r.fail(send.ComposerCodeBusy, "a turn started while typing; "+removedPhrase(r.eraseOwnLine())+", nothing submitted")
	}
	if err := r.p.SendNamedKey("Enter"); err != nil {
		return r.fail(CodeKeys, "Enter failed: "+err.Error())
	}
	r.step("submitted")
	return r.verify(baseline)
}

func (r *runner) waitReady() (string, send.ComposerReadiness) {
	deadline := time.Now().Add(r.opts.WaitIdle)
	for {
		raw, err := r.p.CapturePaneFresh()
		var ready send.ComposerReadiness
		if err != nil {
			ready = send.ComposerReadiness{Code: CodePaneGone}
		} else {
			ready = send.ClaudeComposerReady(raw)
		}
		if ready.Ready || !time.Now().Before(deadline) {
			return raw, ready
		}
		r.opts.Sleep(r.opts.Poll)
	}
}

func preflightMessage(r send.ComposerReadiness) string {
	switch r.Code {
	case send.ComposerCodeDraft:
		return fmt.Sprintf("the composer holds unsent text (%d chars); nothing was typed. Submit or clear it first", r.DraftLen)
	case send.ComposerCodeBusy:
		return "Claude is running a turn; retry when it is idle or pass --wait-idle"
	case send.ComposerCodeDialog:
		return "a selection dialog is open; Enter would pick an entry. Close it first"
	case CodePaneGone:
		return "the pane could not be read"
	default:
		return "no Claude composer on screen (session starting, exited, or in another view)"
	}
}

// confirmTyped waits until the composer holds exactly our text with the
// picker closed. If the picker stays open (no exact command match), it
// removes our own text and refuses.
func (r *runner) confirmTyped() (Result, bool) {
	want := send.NormalizePromptText(r.typed())
	deadline := time.Now().Add(r.opts.TypeWait)
	var input, last string
	var picker pickerState
	for {
		r.opts.Sleep(r.opts.Settle)
		raw, err := r.p.CapturePaneFresh()
		last = raw
		if err != nil {
			return r.fail(CodePaneGone, "pane unreadable after typing: "+err.Error()), false
		}
		input = composerInput(raw)
		picker = readPicker(tmux.StripANSI(raw))
		if input == want && !picker.open {
			r.step("composer holds exactly the command; picker closed")
			return Result{}, true
		}
		if !time.Now().Before(deadline) {
			break
		}
	}
	if input != want {
		// Never submit it. If what is there is only our own text, mangled
		// in transit (tmux once ate a trailing ';'), clear it so a partial
		// command is not left for the next Enter.
		return r.fail(CodeTypeMismatch, "composer does not hold exactly the typed command; "+r.clearIfOwnText(input)+", nothing submitted",
			fmt.Sprintf("composer: %q", input)), false
	}
	ev := fmt.Sprintf("picker still open, highlighting %s", picker.selected)
	var removed bool
	if send.ClaudeTurnRunning(last) {
		removed = r.eraseOwnLine() // Escape would interrupt the turn
	} else {
		removed = r.closePickerAndErase()
	}
	return r.fail(CodeUnknownCommand, fmt.Sprintf("%s is not an exact command here; Enter would have run %s. %s, nothing submitted", r.cmd.Name, picker.selected, capitalize(removedPhrase(removed))), ev), false
}

// closePickerAndErase closes the open picker (Escape keeps the text) and
// erases our line. Only called with the composer proven to hold exactly our
// text and no turn running (Escape during a turn interrupts it).
func (r *runner) closePickerAndErase() bool {
	_ = r.p.SendNamedKey("Escape")
	r.opts.Sleep(r.opts.Settle)
	raw, err := r.p.CapturePaneFresh()
	if err != nil || composerInput(raw) != send.NormalizePromptText(r.typed()) {
		r.step("abort: composer changed after Escape; left untouched")
		return false
	}
	return r.eraseOwnLine()
}

// eraseOwnLine clears the composer line with Ctrl+U and confirms it is empty.
// Only called with the composer proven to hold nothing but our own text
// (exactly, or a mangled subsequence of it). Ctrl+U does not interrupt a
// running turn.
func (r *runner) eraseOwnLine() bool {
	_ = r.p.SendNamedKey("C-u")
	r.opts.Sleep(r.opts.Settle)
	if raw, err := r.p.CapturePaneFresh(); err == nil && (composerInput(raw) == "" || send.ClaudeComposerReady(raw).Ready) {
		r.step("abort: removed the typed command (Ctrl+U), composer empty")
		return true
	}
	r.step("abort: composer not confirmed empty after Ctrl+U")
	return false
}

// clearIfOwnText clears the composer when it holds nothing but (part of)
// the text we typed: the composer was proven empty before typing, so a
// subsequence of our text is ours, with characters lost in transit. Any
// character we did not type means someone else is typing there too; that
// text is never cleared (it may be the owner's draft). Returns the phrase
// for the result message.
func (r *runner) clearIfOwnText(input string) string {
	if input != "" && !isSubsequence(input, send.NormalizePromptText(r.typed())) {
		return "left untouched (it holds text we did not type)"
	}
	if input == "" {
		return "composer empty"
	}
	return removedPhrase(r.eraseOwnLine())
}

// isSubsequence reports whether every rune of sub appears in s, in order.
func isSubsequence(sub, s string) bool {
	rs := []rune(s)
	i := 0
	for _, c := range sub {
		for i < len(rs) && rs[i] != c {
			i++
		}
		if i == len(rs) {
			return false
		}
		i++
	}
	return true
}

func removedPhrase(removed bool) string {
	if removed {
		return "removed the typed command"
	}
	return "could not confirm the typed command was removed, check the composer"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
