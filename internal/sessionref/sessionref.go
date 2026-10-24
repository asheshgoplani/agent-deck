// Package sessionref hands one session a short reference to another (ASK-111).
//
// Nothing moves: the target session B gets a few words in its composer that
// name the source session A (title, id, project path, status) and tell B's
// agent how to read what A did ("agent-deck session output <id> -q", plus the
// transcript path when it is known locally). The text is typed into B's
// composer and never submitted; the user presses Enter. A's output itself is
// never pasted.
//
// Hand is the one path the CLI (`agent-deck session ref`) and the TUI's
// pick/drop use; the Mac app calls the CLI.
package sessionref

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Refusal codes. They are part of the CLI's --json contract (error_code).
const (
	CodeMissingSession   = "missing_session"
	CodeSameSession      = "same_session"
	CodeCrossProfile     = "cross_profile"
	CodeSourceRemote     = "source_remote"
	CodeTargetRemote     = "target_remote"
	CodeTargetNotRunning = "target_not_running"
	CodeTargetNoPane     = "target_no_pane"
	CodeTargetMenuOpen   = "target_menu_open"
	CodeTargetBusyVim    = "target_busy_vim"
	CodeInsertFailed     = "insert_failed"
)

// Endpoint is one side of a reference: the instance and the profile it was
// loaded from ("" means the default profile).
type Endpoint struct {
	Inst    *session.Instance
	Profile string
}

// Options tunes Hand. The zero value inserts into B through tmux.
type Options struct {
	// PrintOnly builds the reference and inserts nothing; B need not run.
	PrintOnly bool
	// Insert types line into target's composer without pressing Enter.
	// nil uses B's tmux primary window (tmux.InsertTextToPrimaryWindow).
	Insert func(target *session.Instance, line string) error
	// Running reports whether target can take keystrokes. nil uses Exists().
	Running func(*session.Instance) bool
	// Capture returns B's agent pane (ANSI intact) for the menu guard: no
	// key is typed while B shows a menu or permission prompt, because the
	// typed text would answer it (digits pick an option, y/n/a approve or
	// deny, a vim-mode Escape dismisses it). The same read refuses a
	// vim-mode B that is working (pane or status reads running): the Escape
	// that puts its composer in insert mode would interrupt the turn. nil
	// uses tmux
	// (CapturePrimaryPaneFresh) on the tmux path, where the guard runs before
	// every key batch; with a custom Insert and nil Capture there is no pane
	// check (that Insert owns its target).
	Capture func(target *session.Instance) (string, error)
	// Transcript returns A's transcript path or "". nil uses
	// session.ListedTranscriptPath (stored identity only, no pane access).
	Transcript func(*session.Instance) string
}

// SourceInfo describes A in the result.
type SourceInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Tool       string `json:"tool,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Profile    string `json:"profile,omitempty"`
}

// TargetInfo describes B in the result.
type TargetInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Result is what Hand built and whether it was typed into B.
type Result struct {
	Source SourceInfo `json:"source"`
	Target TargetInfo `json:"target"`
	// Text is the reference on a few lines (display, composers of its own).
	Text string `json:"text"`
	// Line is the same content on one line: exactly what is typed into B.
	Line     string `json:"line"`
	Inserted bool   `json:"inserted"`
}

// Refusal is returned when a reference cannot or must not be handed.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

// normProfile maps "" to the default profile name so the two compare equal.
func normProfile(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return session.DefaultProfile
	}
	return p
}

func isRemote(inst *session.Instance) bool {
	return strings.TrimSpace(inst.SSHHost) != ""
}

// Check applies the refusal rules only: no tmux, no insert. needRunning adds
// the rule that B must be running (insert mode). running nil uses Exists().
func Check(src, dst Endpoint, needRunning bool, running func(*session.Instance) bool) *Refusal {
	if src.Inst == nil || dst.Inst == nil {
		return refuse(CodeMissingSession, "both a source and a target session are required")
	}
	a, b := src.Inst, dst.Inst
	if a == b || (a.ID != "" && a.ID == b.ID) {
		return refuse(CodeSameSession, "pick a different session: %q cannot take a reference to itself", b.Title)
	}
	if normProfile(src.Profile) != normProfile(dst.Profile) {
		return refuse(CodeCrossProfile, "%q is in profile %q and %q is in profile %q: a reference only works inside one profile",
			a.Title, normProfile(src.Profile), b.Title, normProfile(dst.Profile))
	}
	if isRemote(a) {
		return refuse(CodeSourceRemote, "%q runs on %s (remote): remote sessions can't give a reference yet", a.Title, a.SSHHost)
	}
	if isRemote(b) {
		return refuse(CodeTargetRemote, "%q runs on %s (remote): remote sessions can't take a reference yet", b.Title, b.SSHHost)
	}
	if needRunning {
		if running == nil {
			running = func(i *session.Instance) bool { return i.Exists() }
		}
		if !running(b) {
			return refuse(CodeTargetNotRunning, "%q is not running: start it first, or use --print to copy the reference", b.Title)
		}
	}
	return nil
}

// clean makes a value safe to type into a terminal: control characters
// (newline, ESC, ...) become spaces and the result is trimmed.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// lineSep joins Text's lines into Line.
const lineSep = " · "

// Build returns the reference text for A, addressed to B. It does no checks
// beyond nil inputs. text has one item per line; line is the same items on
// one line with a trailing space so the user can keep typing after it.
func Build(src Endpoint, dst *session.Instance, transcript string) (text, line string) {
	a := src.Inst
	if a == nil {
		return "", ""
	}
	_ = dst // B is not named in the text: B's agent reads it in its own pane.
	status := clean(string(a.GetStatusThreadSafe()))
	if status == "" {
		status = "unknown"
	}
	id := clean(a.ID)
	head := fmt.Sprintf("[ref] session %s (%s)", strconv.Quote(clean(a.Title)), id)
	if p := clean(a.ProjectPath); p != "" {
		head += lineSep + p
	}
	head += lineSep + "status " + status

	cmd := "agent-deck "
	// The profile is always named when known: a bare command resolves the
	// reader's own default profile, which need not be A's.
	if p := clean(src.Profile); p != "" {
		cmd += "-p " + p + " "
	}
	cmd += "session output " + id + " -q"

	items := []string{head, "read it with: " + cmd}
	if tp := clean(transcript); tp != "" {
		items = append(items, "transcript: "+tp)
	}
	return strings.Join(items, "\n"), strings.Join(items, lineSep) + " "
}

// Hand validates the pair, builds the reference and, unless PrintOnly, types
// its one-line form into B's composer without pressing Enter. A refusal comes
// back as (nil, *Refusal); a failed insert as (result, *Refusal{insert_failed})
// with Inserted false.
func Hand(src, dst Endpoint, opts Options) (*Result, error) {
	if r := Check(src, dst, !opts.PrintOnly, opts.Running); r != nil {
		return nil, r
	}
	a, b := src.Inst, dst.Inst
	transcript := ""
	if opts.Transcript != nil {
		transcript = opts.Transcript(a)
	} else {
		transcript = session.ListedTranscriptPath(a)
	}
	text, line := Build(src, b, transcript)
	res := &Result{
		Source: SourceInfo{
			ID:         a.ID,
			Title:      a.Title,
			Path:       a.ProjectPath,
			Status:     string(a.GetStatusThreadSafe()),
			Tool:       a.Tool,
			Transcript: clean(transcript),
			Profile:    strings.TrimSpace(src.Profile),
		},
		Target: TargetInfo{ID: b.ID, Title: b.Title},
		Text:   text,
		Line:   line,
	}
	if opts.PrintOnly {
		return res, nil
	}
	var err error
	if opts.Insert == nil {
		err = insertTmux(b, line, opts.Capture)
	} else {
		if opts.Capture != nil {
			if r := menuGuard(b, opts.Capture); r != nil {
				return res, r
			}
		}
		err = opts.Insert(b, line)
	}
	if err != nil {
		if r, ok := err.(*Refusal); ok {
			return res, r
		}
		return res, refuse(CodeInsertFailed, "could not type the reference into %q: %v", b.Title, err)
	}
	res.Inserted = true
	return res, nil
}

// menuGuard reads B's pane and refuses when it shows a menu or prompt
// (session.PaneMenuOpen, the rule session send's guard applies) or cannot be
// read at all. For a vim-mode B it also refuses while B is working
// (targetBusy): the insert starts with Escape, and Escape interrupts a
// running Claude turn. It returns nil when typing is safe.
func menuGuard(target *session.Instance, capture func(*session.Instance) (string, error)) *Refusal {
	raw, err := capture(target)
	if err != nil {
		return refuse(CodeTargetNoPane, "could not read %q's terminal, so nothing was typed: %v", target.Title, err)
	}
	if session.PaneMenuOpen(target.Tool, raw) {
		return refuse(CodeTargetMenuOpen, "%q is showing a menu/prompt; answer it first (nothing was typed)", target.Title)
	}
	if vimMode(target) && targetBusy(target, raw) {
		return refuse(CodeTargetBusyVim, "%q is working; drop again when it is waiting (vim mode: the Escape before typing would interrupt it; nothing was typed)", target.Title)
	}
	return nil
}

// vimMode reports whether B's composer gets Escape+i before any text
// (tmux.Session.VimMode, set from [claude].vim_mode for Claude tools).
func vimMode(target *session.Instance) bool {
	ts := target.GetTmuxSession()
	return ts != nil && ts.VimMode
}

// targetBusy reports whether B is working on a turn: its pane reads running
// (session.PaneBusy) or its status is running. Either one is enough; a stale
// running status only costs a refusal, never a key.
func targetBusy(target *session.Instance, raw string) bool {
	return target.GetStatusThreadSafe() == session.StatusRunning || session.PaneBusy(target.Tool, raw)
}

// insertTmux types line into B's managed agent window; never presses Enter.
// The menu guard runs before every key batch, so a menu that opens between
// the vim-mode Escape and the text still stops the insert.
func insertTmux(target *session.Instance, line string, capture func(*session.Instance) (string, error)) error {
	ts := target.GetTmuxSession()
	if ts == nil {
		return refuse(CodeTargetNoPane, "%q has no tmux pane", target.Title)
	}
	if capture == nil {
		capture = func(*session.Instance) (string, error) { return ts.CapturePrimaryPaneFresh() }
	}
	return ts.InsertTextToPrimaryWindowChecked(line, func() error {
		if r := menuGuard(target, capture); r != nil {
			return r
		}
		return nil
	})
}
