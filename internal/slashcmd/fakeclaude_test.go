package slashcmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// fakeClaude simulates the Claude Code 2.1.296 composer and slash-command
// picker as observed live (lab/evidence in the harness-control workdir):
//
//   - typing text that starts with "/" opens the picker; the picker closes
//     once the first token is an exact known command followed by a space
//   - a partial or unknown token keeps the picker open, highlighting the
//     first fuzzy match; Enter then runs THAT entry (the incident class)
//   - Escape closes the picker and keeps the text; C-u clears the line
//   - a ghost suggestion is a placeholder: typing replaces it, Escape does
//     not clear it
type fakeClaude struct {
	known      []string
	composer   string
	ghost      string
	busy       bool
	dialog     bool
	escaped    bool // picker dismissed with Escape for the current text
	transcript []string
	executed   []string // commands Claude actually ran
	gone       bool
	keys       []string // every key event, for assertions
	model      string
	// cached makes /model <id> ask "Switch model?" first (live 2.1.296 with
	// a cached conversation); pendingModel holds the requested display name.
	cached       bool
	pendingModel string
	// afterExec lets a test change the screen after submission (e.g. a slow
	// compaction); nil means the command completes immediately.
	afterExec func(f *fakeClaude, cmd string)
	// beforeCapture runs at the start of every capture (n counts captures),
	// to change the pane between two reads.
	beforeCapture func(f *fakeClaude, n int)
	captures      int
	// ignoreEnter makes Enter do nothing (the command stays in the composer).
	ignoreEnter bool
	// lateError renders "Unknown command" one capture after the echo.
	lateError bool
	// switchTo overrides the model named in the "Switch model?" dialog.
	switchTo string
	// permissionAfter makes a generic command open a permission dialog.
	permissionAfter bool
	// eatTrailingSemicolon drops a trailing ';' from typed text, as tmux
	// did before SendKeys escaped it (review L4).
	eatTrailingSemicolon bool
}

func newFakeClaude() *fakeClaude {
	return &fakeClaude{
		known: []string{"/clear", "/compact", "/model", "/exit", "/status", "/catchup", "/voice-mode:on", "/doctor"},
		model: "Fable 5.1",
	}
}

func (f *fakeClaude) CapturePaneFresh() (string, error) {
	f.captures++
	if f.beforeCapture != nil {
		f.beforeCapture(f, f.captures)
	}
	if f.lateError && len(f.transcript) > 0 && strings.HasPrefix(f.transcript[len(f.transcript)-1], "❯ /") {
		// The error renders after this frame was drawn.
		cmd := strings.TrimPrefix(f.transcript[len(f.transcript)-1], "❯ ")
		defer func() {
			f.transcript = append(f.transcript, "  ⎿  Unknown command: "+cmd)
		}()
	}
	if f.gone {
		return "", errors.New("can't find pane")
	}
	div := strings.Repeat("─", 60)
	if f.pendingModel != "" {
		return strings.Join(append(append([]string{}, f.transcript...), div, div,
			"  Switch model?", "  Your next response will be slower and use more tokens",
			"  ❯ 1. Yes, switch to "+f.pendingModel, "    2. No, go back"), "\n"), nil
	}
	if f.dialog && f.permissionAfter {
		return strings.Join(append(append([]string{}, f.transcript...), div, div,
			"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No, and tell Claude what to do differently"), "\n"), nil
	}
	if f.dialog {
		return strings.Join(append(append([]string{}, f.transcript...),
			"  Select model", "  ❯ 1. Default", "    2. Haiku 5.5",
			"  Enter to set as default · s to use this session only · Esc to cancel"), "\n"), nil
	}
	lines := []string{" ▐▛███▛█   Claude Code v2.1.296", "▝▜██████▀  " + f.model + " · Claude Max"}
	lines = append(lines, f.transcript...)
	if f.busy {
		lines = append(lines, "✻ Brewing… (2s · ↓ 1k tokens)") // live 2.1.296 shape: no "esc to interrupt"
	}
	body := f.composer
	if body == "" && f.ghost != "" {
		body = "\x1b[2m" + f.ghost + "\x1b[0m"
	} else if hint := f.argHint(); hint != "" {
		body += "  \x1b[38;5;246m" + hint + "\x1b[39m"
	}
	// Live 2.1.296 draws the session name into the top border.
	top := "\x1b[38;5;244m" + div + " ht-picker ─"
	lines = append(lines, top, "\x1b[39m❯ "+body, "\x1b[38;5;244m"+div)
	if entries := f.pickerEntries(); len(entries) > 0 {
		for i, e := range entries {
			mark := "    "
			if i == 0 {
				mark = "  ❯ "
			}
			lines = append(lines, fmt.Sprintf("%s%-44s  Description of %s", mark, e, e))
		}
	} else {
		lines = append(lines, "  [personal] ashesh@mac:/tmp/x | [Fable 5.1] ctx:0%", "  ⏵⏵ bypass permissions on (shift+tab to cycle)")
	}
	return strings.Join(lines, "\n"), nil
}

func (f *fakeClaude) argHint() string {
	if strings.HasSuffix(f.composer, " ") && f.isKnown(strings.TrimSpace(f.composer)) {
		return "[args]"
	}
	return ""
}

func (f *fakeClaude) isKnown(name string) bool {
	for _, k := range f.known {
		if k == name {
			return true
		}
	}
	return false
}

// pickerEntries returns the open picker's rows, best match first.
func (f *fakeClaude) pickerEntries() []string {
	if f.escaped || !strings.HasPrefix(f.composer, "/") {
		return nil
	}
	token, _, hasSpace := strings.Cut(f.composer, " ")
	if hasSpace && f.isKnown(token) {
		return nil
	}
	needle := strings.TrimPrefix(token, "/")
	var out []string
	for _, k := range f.known {
		if strings.Contains(k, needle) {
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.HasPrefix(out[i], token) && !strings.HasPrefix(out[j], token) })
	return out
}

func (f *fakeClaude) SendKeys(text string) error {
	if f.gone {
		return errors.New("can't find pane")
	}
	f.keys = append(f.keys, "text:"+text)
	if f.eatTrailingSemicolon {
		text = strings.TrimSuffix(text, ";")
	}
	f.composer += text
	f.escaped = false
	return nil
}

func (f *fakeClaude) SendNamedKey(key string) error {
	f.keys = append(f.keys, "key:"+key)
	switch key {
	case "Escape":
		if f.busy {
			f.transcript = append(f.transcript, "  ⎿  Interrupted by user")
			f.busy = false
		}
		if f.dialog && f.permissionAfter {
			f.dialog = false
			return nil
		}
		if f.pendingModel != "" {
			f.pendingModel = ""
			return nil
		}
		if f.dialog {
			f.dialog = false
			f.transcript = append(f.transcript, "  ⎿  Kept model as "+f.model)
			return nil
		}
		f.escaped = true
	case "C-u":
		f.composer = ""
		f.escaped = false
	case "Enter":
		return f.enter()
	}
	return nil
}

func (f *fakeClaude) enter() error {
	if f.ignoreEnter {
		return nil
	}
	if f.pendingModel != "" {
		f.model, f.pendingModel = f.pendingModel, ""
		f.transcript = append(f.transcript, "  ⎿  Set model to "+f.model+" and saved as your default for new sessions")
		return nil
	}
	if f.dialog {
		return nil
	}
	if entries := f.pickerEntries(); len(entries) > 0 {
		f.run(entries[0], "") // the picker's highlighted entry wins
		return nil
	}
	text := strings.TrimSpace(f.composer)
	if text == "" {
		return nil
	}
	name, args, _ := strings.Cut(text, " ")
	f.run(name, strings.TrimSpace(args))
	return nil
}

func (f *fakeClaude) run(name, args string) {
	f.composer, f.ghost, f.escaped = "", "", false
	echo := "❯ " + strings.TrimSpace(name+" "+args)
	f.executed = append(f.executed, strings.TrimSpace(name+" "+args))
	if !f.isKnown(name) {
		f.transcript = append(f.transcript, echo, "  ⎿  Unknown command: "+name)
		return
	}
	switch name {
	case "/model":
		if args == "" {
			f.transcript = append(f.transcript, echo)
			f.dialog = true
			return
		}
		display := map[string]string{
			"claude-haiku-5-5": "Haiku 5.5", "claude-fable-5-1": "Fable 5.1",
			"opus[1m]": "Opus 4.6 (1M context)", "sonnet[1m]": "Sonnet 4.6 (1M context)",
			"opus": "Opus 4.6", "s": "Sonnet 4.6", "opus-4": "Opus 4.6",
		}[args]
		if f.cached {
			f.transcript = append(f.transcript, echo)
			f.pendingModel = display
			if f.switchTo != "" {
				f.pendingModel = f.switchTo
			}
			return
		}
		f.model = display
		f.transcript = append(f.transcript, echo, "  ⎿  Set model to "+f.model+" and saved as your default for new sessions")
	case "/clear":
		f.transcript = []string{echo}
	case "/compact":
		f.transcript = append(f.transcript, echo, "  ⎿  Compacted (ctrl+o to see full summary)")
	case "/exit":
		f.gone = true
	default:
		f.transcript = append(f.transcript, echo)
		if f.lateError {
			break
		}
		f.transcript = append(f.transcript, "⏺ Running "+name)
		if f.permissionAfter {
			f.dialog = true
		}
	}
	if f.afterExec != nil {
		f.afterExec(f, name)
	}
}
