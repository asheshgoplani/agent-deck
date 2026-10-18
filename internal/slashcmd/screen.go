package slashcmd

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// composerInput returns the text typed into the composer, normalized. Hint
// text (the ghost suggestion, the grey argument hint in "/clear  [name]") is
// dropped only as a trailing suffix: hint-styled text in front of real input
// is not ours to ignore, so it stays and fails the exact-match check.
func composerInput(raw string) string {
	rawLines := strings.Split(raw, "\n")
	lines := strings.Split(tmux.StripANSI(raw), "\n")
	top, bottom := send.ComposerBounds(lines)
	if top < 0 || len(rawLines) != len(lines) {
		return ""
	}
	var segs []segment
	for i := top + 1; i < bottom; i++ {
		segs = append(segs, segment{text: " "})
		segs = append(segs, styledSegments(rawLines[i])...)
	}
	for len(segs) > 0 && (segs[len(segs)-1].hint || strings.TrimSpace(segs[len(segs)-1].text) == "") {
		segs = segs[:len(segs)-1]
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	body := b.String()
	for _, marker := range []string{"❯", "›"} {
		if idx := strings.Index(body, marker); idx >= 0 {
			body = body[idx+len(marker):]
			break
		}
	}
	return send.NormalizePromptText(body)
}

type segment struct {
	text string
	hint bool
}

var csiPattern = regexp.MustCompile(`\x1b\[([0-9;]*)([A-Za-z])`)

// styledSegments splits a raw line into text runs, each marked when rendered
// as a hint. Claude 2.1.296 draws argument hints in 256-colour grey
// (38;5;246), which send's suggestion check deliberately does not treat as a
// ghost, so the hint palette is kept here.
func styledSegments(line string) []segment {
	var out []segment
	hint := false
	last := 0
	for _, m := range csiPattern.FindAllStringSubmatchIndex(line, -1) {
		if m[0] > last {
			out = append(out, segment{text: line[last:m[0]], hint: hint})
		}
		last = m[1]
		if line[m[4]:m[5]] == "m" {
			hint = hintSGR(line[m[2]:m[3]], hint)
		}
	}
	if last < len(line) {
		out = append(out, segment{text: line[last:], hint: hint})
	}
	return out
}

func hintSGR(params string, hint bool) bool {
	if params == "" {
		return false
	}
	p := strings.Split(params, ";")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case "0", "00", "22", "39":
			hint = false
		case "2", "90":
			hint = true
		case "38":
			if i+2 < len(p) && p[i+1] == "5" {
				n, _ := strconv.Atoi(p[i+2])
				hint = n == 8 || (n >= 232 && n <= 250)
				i += 2
			} else if i+1 < len(p) && p[i+1] == "2" {
				hint = false
				i += 4
			}
		case "48", "58":
			if i+1 < len(p) && p[i+1] == "5" {
				i += 2
			} else if i+1 < len(p) && p[i+1] == "2" {
				i += 4
			}
		default:
			if n, err := strconv.Atoi(p[i]); err == nil && ((n >= 30 && n <= 37) || (n >= 91 && n <= 97)) {
				hint = false
			}
		}
	}
	return hint
}

type pickerState struct {
	open     bool
	selected string
	entries  []string
}

// pickerRow matches a slash-command picker row, with or without a
// description.
var pickerRow = regexp.MustCompile(`^\s{1,6}(❯\s+)?(/[A-Za-z0-9][A-Za-z0-9_.:\-]*)(?:\s{2,}\S|\s*$)`)

// readPicker parses the slash-command picker drawn below the composer.
func readPicker(content string) pickerState {
	lines := strings.Split(content, "\n")
	_, bottom := send.ComposerBounds(lines)
	if bottom < 0 {
		return pickerState{}
	}
	var st pickerState
	for _, line := range lines[bottom+1:] {
		m := pickerRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		st.entries = append(st.entries, m[2])
		if m[1] != "" && st.selected == "" {
			st.selected = m[2]
		}
	}
	st.open = len(st.entries) > 0
	if st.open && st.selected == "" {
		st.selected = st.entries[0]
	}
	return st
}

var (
	setModel  = regexp.MustCompile(`^(Set model to|Kept model as) (.+?)(?: and saved.*| for this session.*)?$`)
	bannerRow = regexp.MustCompile(`([A-Z][A-Za-z]*(?: [0-9][0-9.]*)+)(?: with [a-z]+ effort)? · Claude `)
)

// resultText strips the "⎿" result gutter from a transcript line.
func resultText(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "⎿"))
}

// transcriptLines returns the stripped lines above the composer.
func transcriptLines(content string) []string {
	lines := strings.Split(content, "\n")
	if top, _ := send.ComposerBounds(lines); top >= 0 {
		return lines[:top]
	}
	return lines
}

// observeBefore records what the command is about to change.
func (r *runner) observeBefore(content string) string {
	if r.cmd.Kind != KindModel {
		return ""
	}
	return currentModel(content)
}

func currentModel(content string) string {
	lines := transcriptLines(content)
	for i := len(lines) - 1; i >= 0; i-- {
		if m := setModel.FindStringSubmatch(resultText(lines[i])); m != nil {
			return m[2]
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if m := bannerRow.FindStringSubmatch(lines[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

// isRequestedModel reports whether display names the model requested by
// /model <args>: same family and 1M variant on token boundaries, and the
// exact version when one was given (model.go).
//
// Once an entry was picked, display must name that entry (same family,
// version and 1M variant): Claude echoes and confirms the entry, which for
// an alias or "default" is more specific than the argument.
func (r *runner) isRequestedModel(display string) bool {
	if r.pickedLabel != "" {
		if isDefaultModel(r.cmd.Args) {
			return strings.TrimSpace(display) != ""
		}
		picked, ok := parseModelSpec(r.pickedLabel)
		got, ok2 := parseModelSpec(display)
		return ok && ok2 && picked.family == got.family && picked.oneM == got.oneM &&
			strings.Join(picked.version, ".") == strings.Join(got.version, ".")
	}
	return modelMatches(r.cmd.Args, display)
}

// modelPickerRow matches a /model picker entry such as
// "  ❯ 3.  Fable 5.1 ✔            For your toughest challenges" or
// "  ↓ 10. Opus 4.8               Best for everyday, complex tasks".
var modelPickerRow = regexp.MustCompile(`^\s*(?:[↓↑]\s*)?(❯\s*)?(\d+)\.\s+(.+?)(?:\s+✔)?(?:\s{2,}\S.*)?$`)

type pickerRowT struct {
	num      int
	label    string
	selected bool
}

// modelPickerRows parses the visible rows of the /model picker. Only the
// picker region counts: the lines after its "Select model" header, so a
// numbered list in the transcript above is never read as picker rows.
func modelPickerRows(content string) []pickerRowT {
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "Select model") {
			lines = lines[i+1:]
			break
		}
	}
	var rows []pickerRowT
	for _, line := range lines {
		m := modelPickerRow.FindStringSubmatch(strings.TrimRight(line, " "))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		rows = append(rows, pickerRowT{num: n, label: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[3]), "✔")), selected: m[1] != ""})
	}
	return rows
}

// maxPickerSteps bounds the arrow keys sent while looking for the entry:
// up to the top, then once down the whole list.
const maxPickerSteps = 80

// pickerMatches reports whether a picker label is the requested model: the
// "default" alias is the Default entry; anything else is matched by family,
// version and 1M variant on token boundaries (model.go).
func (r *runner) pickerMatches(label string) bool {
	if isDefaultModel(r.cmd.Args) {
		spec, ok := parseModelSpec(label)
		return ok && spec.family == "default"
	}
	want, ok := parseModelSpec(r.cmd.Args)
	got, ok2 := parseModelSpec(label)
	return ok && ok2 && want.matches(got)
}

// navigatePicker moves the /model picker to its first row, then walks it
// down one row at a time to the first entry that matches the requested
// model (so an alias such as "opus" picks the first Opus entry, as Claude
// lists them), and presses "s" (use this session only) once that entry has
// been highlighted on two consecutive frames. It never presses Enter in the
// picker: Enter saves the model as the default for new sessions. When the
// whole list was walked without a match it cancels the picker and names the
// entries it saw.
func (r *runner) navigatePicker(content string) (Result, bool) {
	rows := modelPickerRows(content)
	var sel *pickerRowT
	for i := range rows {
		if rows[i].selected {
			sel = &rows[i]
		}
	}
	if sel == nil || r.picked {
		return Result{}, false // frame still drawing, or "s" sent and the picker is closing
	}
	moved := sel.num != r.lastSel
	if r.steps > 0 && !moved {
		// The highlight has not moved yet: wait a few frames before
		// treating it as an end of the list that does not wrap.
		if r.stalled++; r.stalled < 4 {
			return Result{}, false
		}
	}
	stalled := r.steps > 0 && !moved
	r.stalled = 0
	if !r.scanning {
		if sel.num != 1 && !stalled && r.steps < maxPickerSteps {
			return r.pickerKey(sel, "Up")
		}
		r.scanning = true // at the top (or the top does not move further)
		r.stableSel = 0
	}
	if r.pickerMatches(sel.label) {
		if r.stableSel != sel.num {
			r.stableSel = sel.num // re-read once before pressing "s"
			return Result{}, false
		}
		r.picked, r.pickedLabel = true, sel.label
		_ = r.p.SendKeys("s")
		r.step(fmt.Sprintf("picker: %q highlighted, pressed s (this session only)", sel.label))
		return Result{}, false
	}
	r.stableSel = 0
	if !slices.Contains(r.seen, sel.label) {
		r.seen = append(r.seen, sel.label)
	}
	wrapped := len(r.seen) > 1 && sel.num == 1 && r.steps > 0 && r.lastSel != 0 && r.lastSel != 1
	if wrapped || (stalled && r.scanning && len(r.seen) > 1) || r.steps >= maxPickerSteps {
		_ = r.p.SendNamedKey("Escape")
		r.step("cancelled the model picker (Escape)")
		return r.fail(CodeUnknownModel, fmt.Sprintf("the /model picker has no entry for %s; it lists: %s. Cancelled, nothing changed", r.cmd.Args, strings.Join(r.seen, ", "))), true
	}
	return r.pickerKey(sel, "Down")
}

func (r *runner) pickerKey(sel *pickerRowT, key string) (Result, bool) {
	r.lastSel = sel.num
	r.steps++
	_ = r.p.SendNamedKey(key)
	return Result{}, false
}

// transcriptChanged reports whether the lines above the composer differ
// between two frames.
func transcriptChanged(content, baseline string) bool {
	return strings.Join(transcriptLines(content), "\n") != strings.Join(transcriptLines(baseline), "\n")
}

func verifyDescription(k Kind) string {
	switch k {
	case KindModel:
		return `"Set model to <name> for this session only" echo (a "saved as your default" echo fails)`
	case KindCompact:
		return `"Compacted" notice`
	case KindClear:
		return "transcript redrawn holding only the /clear echo"
	case KindExit:
		return "Claude exits and the pane goes away"
	default:
		return "command echo in the transcript and the composer no longer holding it"
	}
}

// afterEcho returns the transcript lines after the last echo of our command
// and whether the echo was found.
func (r *runner) afterEcho(lines []string) ([]string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "❯ "+r.cmd.Name) || strings.HasPrefix(t, "> "+r.cmd.Name) {
			return lines[i+1:], true
		}
	}
	return nil, false
}

func (r *runner) verify(baseline string) Result {
	deadline := time.Now().Add(r.opts.Timeout)
	goneStreak := 0
	for {
		r.opts.Sleep(r.opts.Poll)
		raw, err := r.p.CapturePaneFresh()
		if err != nil {
			if r.cmd.Kind == KindExit {
				return r.ok("exited", "pane closed after /exit")
			}
			return r.fail(CodePaneGone, "pane went away after submit: "+err.Error())
		}
		content := tmux.StripANSI(raw)
		if r.cmd.Kind == KindExit {
			if _, visible := send.CurrentComposerPrompt(content); !visible {
				goneStreak++
				if goneStreak >= 2 {
					return r.ok("exited", "Claude composer gone after /exit")
				}
			} else {
				goneStreak = 0
			}
		} else if res, done := r.check(raw, content, baseline); done {
			return res
		}
		if !time.Now().Before(deadline) {
			if composerInput(raw) == send.NormalizePromptText(r.typed()) {
				// Enter never took: do not leave our command for the next Enter.
				return r.fail(CodeEnterIgnored, "Enter did not submit the command; "+removedPhrase(r.eraseOwnLine()))
			}
			return r.fail(CodeVerifyTimeout, "submitted, but the expected outcome did not appear within "+r.opts.Timeout.String()+": "+verifyDescription(r.cmd.Kind))
		}
	}
}

func (r *runner) ok(after string, evidence ...string) Result {
	r.res.OK, r.res.After = true, after
	r.res.Evidence = append(r.res.Evidence, evidence...)
	r.step("verified")
	return r.res
}

// rejection finds Claude's own error for the command in the result lines.
// "Error ..." counts only for client commands; a skill's tool output may
// legitimately start with it.
func (r *runner) rejection(after []string) (string, bool) {
	for _, l := range after {
		t := resultText(l)
		clientErr := strings.HasPrefix(t, "Error") && r.cmd.Kind != KindGeneric
		if strings.HasPrefix(t, "Unknown command") || strings.HasPrefix(t, "Unknown skill") || clientErr {
			return t, true
		}
	}
	return "", false
}

// check inspects one post-submit frame. done=false means keep polling.
func (r *runner) check(raw, content, baseline string) (Result, bool) {
	lines := transcriptLines(content)
	after, echoed := r.afterEcho(lines)
	if send.DialogOpen(content) {
		switch {
		case r.cmd.Kind == KindModel:
			return r.modelDialog(content)
		case !echoed:
			// Our echo is not on screen above the dialog, so nothing shows
			// the command opened it: it may be a skill's own permission
			// prompt or the owner's. Never answer or cancel a dialog we
			// cannot prove is ours; report it.
			r.step("a dialog is open and the command echo is not visible; left untouched")
			return r.fail(CodeDialogUnowned, "a dialog is open after submitting "+r.cmd.Text+" and the command echo is not visible, so it may not be ours; left it untouched. Check the session"), true
		case r.cmd.Kind == KindGeneric:
			// A skill's own dialog (e.g. a tool permission prompt) belongs
			// to the owner; the command itself was submitted.
			return r.ok("submitted", "transcript shows "+r.cmd.Text+"; a dialog it opened is left for the owner"), true
		default:
			return Result{}, false
		}
	}
	if composerInput(raw) == send.NormalizePromptText(r.typed()) {
		return Result{}, false // not submitted yet
	}
	if !echoed {
		if r.cmd.Kind == KindGeneric && transcriptChanged(content, baseline) {
			// A long first response can scroll the echo off before the
			// first poll. Our text left the composer and the transcript
			// moved after Enter: the command ran.
			return r.ok("submitted", "composer cleared and the transcript changed after submit (the echo scrolled off)"), true
		}
		return Result{}, false
	}
	if t, bad := r.rejection(after); bad {
		return r.fail(CodeCommandError, "Claude rejected the command: "+t, t), true
	}
	switch r.cmd.Kind {
	case KindModel:
		for _, l := range after {
			t := resultText(l)
			m := setModel.FindStringSubmatch(t)
			if m == nil {
				continue
			}
			if strings.Contains(t, "saved as your default") {
				return r.fail(CodeDefaultSaved, "Claude saved "+m[2]+" as the default for new sessions (settings.json \"model\"); restore it", t), true
			}
			if !r.isRequestedModel(m[2]) || !strings.Contains(t, "for this session only") {
				return r.fail(CodeCommandError, "Claude reports "+t+", not a session-only switch to the requested model", t), true
			}
			return r.ok(m[2], t), true
		}
	case KindCompact:
		for _, l := range after {
			if t := resultText(l); strings.HasPrefix(t, "Compacted") {
				return r.ok("compacted", t), true
			}
		}
	case KindClear:
		if content == baseline {
			return Result{}, false
		}
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "⏺") || (strings.HasPrefix(t, "❯ ") && !strings.HasPrefix(t, "❯ "+r.cmd.Name)) {
				return Result{}, false // older transcript still on screen
			}
		}
		return r.ok("cleared", "transcript redrawn with only the /clear echo"), true
	default:
		// Claude may render "Unknown command" a frame after the echo:
		// confirm on one settled frame before reporting success.
		r.opts.Sleep(r.opts.Settle)
		if raw2, err := r.p.CapturePaneFresh(); err == nil {
			if after2, ok := r.afterEcho(transcriptLines(tmux.StripANSI(raw2))); ok {
				if t, bad := r.rejection(after2); bad {
					return r.fail(CodeCommandError, "Claude rejected the command: "+t, t), true
				}
			}
		}
		return r.ok("submitted", "transcript shows "+r.cmd.Text+"; composer cleared"), true
	}
	return Result{}, false
}

// modelDialog handles the dialogs after /model: the model picker (navigated
// to the requested entry, then "s"), and with a cached conversation the
// "Switch model?" confirmation, answered only with "Yes, switch to <the
// requested model>" after our session-only pick. Anything else is cancelled.
func (r *runner) modelDialog(content string) (Result, bool) {
	if strings.Contains(content, "Select model") && !strings.Contains(content, "Switch model?") {
		return r.navigatePicker(content)
	}
	sel, _ := send.DialogSelection(content)
	target, isSwitch := strings.CutPrefix(sel, "Yes, switch to ")
	if isSwitch && r.picked && r.isRequestedModel(target) {
		if !r.confirmed {
			r.confirmed = true
			_ = r.p.SendNamedKey("Enter")
			r.step("confirmed the Switch model? dialog: " + sel)
			r.res.Evidence = append(r.res.Evidence, "Switch model? -> "+sel)
		}
		return Result{}, false // the dialog may still be on screen for a frame
	}
	_ = r.p.SendNamedKey("Escape")
	r.step("cancelled the dialog /model opened (Escape)")
	if isSwitch {
		return r.fail(CodeCommandError, "Claude offered to switch to "+target+", not the requested model; cancelled", sel), true
	}
	return r.fail(CodeInteractive, r.cmd.Name+" opened an interactive dialog, which cannot be driven remotely; cancelled it. Pass a model, e.g. /model claude-haiku-5-5"), true
}
