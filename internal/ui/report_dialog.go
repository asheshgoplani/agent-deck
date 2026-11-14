package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ReportDialog is the TUI's "Report a problem" form (hotkey report_problem,
// "Z" by default). It is a front end for internal/issuereport, the same core
// `agent-deck feedback bug|idea` uses: the dialog collects the report,
// shows the exact issue issuereport.Build produced, and only after an
// explicit confirm key hands that issue to issuereport.File. There is no
// second filing path here.
//
// Steps: form -> preview (full issue text, scrollable) -> sending -> result.
// Esc closes the dialog at the form, building, preview and result steps;
// nothing is filed unless the user pressed the confirm key on the preview.
// Once sending has started Esc does nothing: gh may already have created
// the issue, so the dialog waits for the result instead of pretending the
// send was cancelled (gh is bounded by issuereport's own timeout).
type ReportDialog struct {
	visible bool
	step    reportStep
	width   int
	height  int

	kind     issuereport.Kind
	focus    int // reportField* index while in the form
	title    textinput.Model
	what     textarea.Model
	expected textarea.Model
	steps    textarea.Model
	tool     string // tool of the session selected when the dialog opened
	formErr  string

	// seq tags every async message so a result that arrives after the
	// dialog was cancelled or reopened is dropped.
	seq     int
	issue   issuereport.Issue
	ghReady bool
	// browserHere: a browser can open on this machine (false on a
	// headless or SSH host, where the send only yields the URL).
	browserHere bool
	// urlTrimmed: the prefilled browser URL carries a shorter body than
	// the preview (only matters when gh is not ready).
	urlTrimmed bool
	lines      []string // wrapped preview lines
	offset     int      // first visible preview line
	cancel     context.CancelFunc

	result    issuereport.FileResult
	resultErr error
	// copyNote is the outcome of the last "c: copy URL" on the result.
	copyNote string

	// Injected for tests; production uses the issuereport functions.
	collect func(version string, logLines int) issuereport.Diagnostics
	checkGh func(ctx context.Context) bool
	filer   func(ctx context.Context, is issuereport.Issue, opts issuereport.FileOptions) (issuereport.FileResult, error)
	canOpen func() bool
	// copyText copies the prefilled URL; browserHere says whether a
	// browser can open on this machine. It returns the method used.
	copyText func(text string, browserHere bool) (string, error)
}

type reportStep int

const (
	reportStepForm reportStep = iota
	reportStepBuilding
	reportStepPreview
	reportStepSending
	reportStepResult
)

const (
	reportFieldKind = iota
	reportFieldTitle
	reportFieldWhat
	reportFieldExpected
	reportFieldSteps
	reportFieldCount
)

// reportLogLines is how many debug-log lines a TUI bug report carries; the
// CLI uses the same number (feedbackReportLogLines).
const reportLogLines = 40

// reportSurface is the footer surface name ("Filed from agent-deck tui").
const reportSurface = "tui"

// reportPreviewMsg carries the built issue back to the dialog.
type reportPreviewMsg struct {
	seq         int
	issue       issuereport.Issue
	ghReady     bool
	urlTrimmed  bool
	browserHere bool
}

// reportFiledMsg carries the result of issuereport.File back to the dialog.
type reportFiledMsg struct {
	seq int
	res issuereport.FileResult
	err error
}

// reportCopiedMsg carries the result of copying the prefilled URL.
type reportCopiedMsg struct {
	seq    int
	method string // "osc52" or the native tool (xclip, pbcopy, ...)
	err    error
}

// reportCopyTimeout bounds a native clipboard tool so the copy note never
// stays on "Copying…" when, say, xclip hangs on a dead X display.
const reportCopyTimeout = 2 * time.Second

// reportCopier copies the prefilled URL. Without a browser here (SSH or
// headless host) it tries OSC 52 first: a native tool such as xclip with a
// leftover DISPLAY would fill the remote machine's clipboard, not the
// user's.
type reportCopier struct {
	osc52Supported bool
	osc52          func(text string) error
	native         func(ctx context.Context, text string, osc52 bool) (string, error)
	timeout        time.Duration
}

func newReportCopier() reportCopier {
	return reportCopier{
		osc52Supported: tmux.GetTerminalInfo().SupportsOSC52,
		osc52:          clipboard.CopyOSC52,
		native: func(ctx context.Context, text string, osc52 bool) (string, error) {
			r, err := clipboard.CopyContext(ctx, text, osc52)
			if err != nil {
				return "", err
			}
			return r.Method, nil
		},
		timeout: reportCopyTimeout,
	}
}

func (c reportCopier) copy(text string, browserHere bool) (string, error) {
	osc := c.osc52Supported
	if !browserHere && osc {
		if err := c.osc52(text); err == nil {
			return "osc52", nil
		}
		osc = false // already failed; do not retry it after the native tools
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	type outcome struct {
		method string
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		m, err := c.native(ctx, text, osc)
		done <- outcome{m, err}
	}()
	select {
	case o := <-done:
		return o.method, o.err
	case <-ctx.Done():
		return "", fmt.Errorf("clipboard tool timed out after %s", c.timeout)
	}
}

// NewReportDialog returns a hidden dialog wired to internal/issuereport.
func NewReportDialog() *ReportDialog {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.Placeholder = "short summary (optional)"
	d := &ReportDialog{
		title:    ti,
		what:     newReportTextarea(),
		expected: newReportTextarea(),
		steps:    newReportTextarea(),
		collect:  issuereport.CollectDiagnostics,
		checkGh:  issuereport.GhReady,
		filer:    issuereport.File,
		canOpen:  issuereport.BrowserAvailable,
		copyText: func(text string, browserHere bool) (string, error) {
			return newReportCopier().copy(text, browserHere)
		},
	}
	d.applyKindPlaceholders()
	return d
}

func newReportTextarea() textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 4000
	ta.SetHeight(2)
	// Enter moves to the next field (the form is short); Ctrl+J or Alt+Enter
	// adds a line break inside a field.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")
	ta.Blur()
	return ta
}

// IsVisible reports whether the dialog is on screen.
func (d *ReportDialog) IsVisible() bool { return d != nil && d.visible }

// Show opens an empty form. tool is the selected session's tool (may be "").
func (d *ReportDialog) Show(tool string) {
	d.stopInFlight()
	d.visible = true
	d.step = reportStepForm
	d.kind = issuereport.KindBug
	d.tool = strings.TrimSpace(tool)
	d.formErr = ""
	d.issue = issuereport.Issue{}
	d.lines = nil
	d.offset = 0
	d.result = issuereport.FileResult{}
	d.resultErr = nil
	d.copyNote = ""
	d.title.SetValue("")
	d.what.SetValue("")
	d.expected.SetValue("")
	d.steps.SetValue("")
	d.applyKindPlaceholders()
	d.setFocus(reportFieldWhat)
	d.layout()
}

// Hide closes the dialog and abandons any in-flight build or send.
func (d *ReportDialog) Hide() {
	d.stopInFlight()
	d.visible = false
	d.title.Blur()
	d.what.Blur()
	d.expected.Blur()
	d.steps.Blur()
}

func (d *ReportDialog) stopInFlight() {
	d.seq++ // results of anything already running are now stale
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

// SetSize records the terminal size so the dialog can center and fit.
func (d *ReportDialog) SetSize(width, height int) {
	if d == nil {
		return
	}
	d.width = width
	d.height = height
	d.layout()
}

// Report returns what the form holds, as the issuereport.Report it files.
func (d *ReportDialog) Report() issuereport.Report {
	return issuereport.Report{
		Kind:         d.kind,
		Title:        d.title.Value(),
		WhatHappened: d.what.Value(),
		Expected:     d.expected.Value(),
		Steps:        d.steps.Value(),
		Tool:         d.tool,
		Surface:      reportSurface,
	}
}

// PreviewText is the exact text shown in the preview pane: the built issue's
// title, labels and body (issuereport.Issue.Text), or "" before a preview.
func (d *ReportDialog) PreviewText() string {
	if d.step != reportStepPreview && d.step != reportStepSending && d.step != reportStepResult {
		return ""
	}
	return d.issue.Text()
}

func (d *ReportDialog) dialogWidth() int { return fitDialogWidth(84, 40, d.width) }

// innerWidth is the content width inside border (2) and padding (4).
func (d *ReportDialog) innerWidth() int { return max(20, d.dialogWidth()-6) }

func (d *ReportDialog) layout() {
	w := d.innerWidth()
	d.title.Width = w - 3
	rows := 3
	if d.height > 0 {
		// The form's fixed rows (border, padding, headings, labels, hints)
		// take about 20 lines; the three text fields share the rest.
		rows = min(4, max(1, (d.height-20)/3))
	}
	for _, ta := range []*textarea.Model{&d.what, &d.expected, &d.steps} {
		ta.SetWidth(w - 2)
		ta.SetHeight(rows)
	}
	if d.step == reportStepResult && d.resultURL() != "" {
		d.wrapResultURL()
	} else if d.issue.Body != "" {
		d.wrapPreview()
	}
}

func (d *ReportDialog) labels() (what, expected, steps string) {
	if d.kind == issuereport.KindIdea {
		return "What problem would this solve?", "Proposed behavior", "Alternatives considered (optional)"
	}
	return "What happened?", "What did you expect?", "Steps to reproduce (optional)"
}

func (d *ReportDialog) applyKindPlaceholders() {
	if d.kind == issuereport.KindIdea {
		d.what.Placeholder = "the problem this idea solves"
		d.expected.Placeholder = "how it should work"
		d.steps.Placeholder = "other ways you considered"
		return
	}
	d.what.Placeholder = "what went wrong"
	d.expected.Placeholder = "what should have happened"
	d.steps.Placeholder = "1. ... 2. ..."
}

func (d *ReportDialog) setFocus(f int) {
	d.focus = (f + reportFieldCount) % reportFieldCount
	d.title.Blur()
	d.what.Blur()
	d.expected.Blur()
	d.steps.Blur()
	switch d.focus {
	case reportFieldTitle:
		d.title.Focus()
	case reportFieldWhat:
		d.what.Focus()
	case reportFieldExpected:
		d.expected.Focus()
	case reportFieldSteps:
		d.steps.Focus()
	}
}

func (d *ReportDialog) toggleKind() {
	if d.kind == issuereport.KindBug {
		d.kind = issuereport.KindIdea
	} else {
		d.kind = issuereport.KindBug
	}
	d.applyKindPlaceholders()
}

// Update handles a key while the dialog is visible.
func (d *ReportDialog) Update(msg tea.KeyMsg) (*ReportDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	key := msg.String()
	if key == "esc" {
		if d.step == reportStepSending {
			// Too late to cancel honestly: gh may already have filed the
			// issue. Stay on screen until the result arrives.
			return d, nil
		}
		// Before the confirm key nothing was sent; after the result there
		// is nothing left to cancel.
		d.Hide()
		return d, nil
	}

	switch d.step {
	case reportStepForm:
		return d.updateForm(msg)
	case reportStepPreview:
		return d.updatePreview(key)
	case reportStepResult:
		return d.updateResult(key)
	case reportStepBuilding, reportStepSending:
		// Waiting on the async step; only Esc (above) does anything.
	}
	return d, nil
}

func (d *ReportDialog) updateForm(msg tea.KeyMsg) (*ReportDialog, tea.Cmd) {
	key := msg.String()
	switch key {
	case "tab", "down":
		d.setFocus(d.focus + 1)
		return d, nil
	case "shift+tab", "up":
		d.setFocus(d.focus - 1)
		return d, nil
	case "ctrl+p":
		return d, d.startPreview()
	case "enter":
		if d.focus == reportFieldSteps {
			return d, d.startPreview()
		}
		d.setFocus(d.focus + 1)
		return d, nil
	}

	if d.focus == reportFieldKind {
		switch key {
		case "left", "right", " ":
			d.toggleKind()
		case "b":
			d.kind = issuereport.KindBug
			d.applyKindPlaceholders()
		case "i":
			d.kind = issuereport.KindIdea
			d.applyKindPlaceholders()
		}
		return d, nil
	}

	d.formErr = ""
	var cmd tea.Cmd
	switch d.focus {
	case reportFieldTitle:
		d.title, cmd = d.title.Update(msg)
	case reportFieldWhat:
		d.what, cmd = d.what.Update(msg)
	case reportFieldExpected:
		d.expected, cmd = d.expected.Update(msg)
	case reportFieldSteps:
		d.steps, cmd = d.steps.Update(msg)
	}
	return d, cmd
}

// startPreview validates the form and builds the issue off the UI goroutine
// (diagnostics read the debug log and the gh check runs a process).
func (d *ReportDialog) startPreview() tea.Cmd {
	r := d.Report()
	if strings.TrimSpace(r.WhatHappened) == "" && strings.TrimSpace(r.Title) == "" {
		if r.Kind == issuereport.KindIdea {
			d.formErr = "Describe the problem the idea solves (or give a title) first."
		} else {
			d.formErr = "Describe what happened (or give a title) first."
		}
		d.setFocus(reportFieldWhat)
		return nil
	}
	d.formErr = ""
	d.stopInFlight()
	d.step = reportStepBuilding
	seq := d.seq
	collect, checkGh, canOpen := d.collect, d.checkGh, d.canOpen
	logLines := 0
	if r.Kind == issuereport.KindBug {
		logLines = reportLogLines
	}
	return func() tea.Msg {
		is := issuereport.Build(r, collect(Version, logLines))
		_, trimmed := issuereport.NewIssueURL(is)
		return reportPreviewMsg{seq: seq, issue: is, ghReady: checkGh(context.Background()), urlTrimmed: trimmed, browserHere: canOpen()}
	}
}

// OnPreview receives the built issue.
func (d *ReportDialog) OnPreview(msg reportPreviewMsg) {
	if !d.visible || msg.seq != d.seq || d.step != reportStepBuilding {
		return
	}
	d.issue = msg.issue
	d.ghReady = msg.ghReady
	d.urlTrimmed = msg.urlTrimmed
	d.browserHere = msg.browserHere
	d.offset = 0
	d.wrapPreview()
	d.step = reportStepPreview
}

func (d *ReportDialog) wrapPreview() {
	w := d.innerWidth()
	var out []string
	for _, line := range strings.Split(d.issue.Text(), "\n") {
		out = append(out, strings.Split(ansi.Wrap(line, w, ""), "\n")...)
	}
	d.lines = out
	d.clampOffset()
}

// browserTrimmed reports whether the send will go through the browser
// with a body shorter than the preview.
func (d *ReportDialog) browserTrimmed() bool { return !d.ghReady && d.urlTrimmed }

// reportTrimmedNote is shown under the preview header when browserTrimmed.
const reportTrimmedNote = "Note: the browser page gets a TRIMMED text (GitHub URL limit: the log is cut first)"

// reportSendingLine is shown while the issue is being filed.
const reportSendingLine = "Sending… (can't be cancelled once started)"

// previewRows is how many preview lines fit on screen.
func (d *ReportDialog) previewRows() int {
	if d.height <= 0 {
		return 20
	}
	// border 2 + padding 2 + header 2 + blank + rule + rule + position + blank + hint
	rows := d.height - 14
	if d.step == reportStepResult && d.resultURL() != "" {
		// Result line (up to 3 rows), copy note, extra hint row.
		rows -= 5
	}
	if d.browserTrimmed() {
		rows -= len(strings.Split(ansi.Wrap(reportTrimmedNote, d.innerWidth(), ""), "\n"))
	}
	return max(3, rows)
}

func (d *ReportDialog) clampOffset() {
	maxOff := max(0, len(d.lines)-d.previewRows())
	d.offset = min(max(0, d.offset), maxOff)
}

func (d *ReportDialog) updatePreview(key string) (*ReportDialog, tea.Cmd) {
	page := max(1, d.previewRows()-1)
	switch key {
	case "up", "k":
		d.offset--
	case "down", "j":
		d.offset++
	case "pgup", "ctrl+u", "b":
		d.offset -= page
	case "pgdown", "ctrl+d", " ", "f":
		d.offset += page
	case "home", "g":
		d.offset = 0
	case "end", "G":
		d.offset = len(d.lines)
	case "e", "backspace", "shift+tab":
		d.step = reportStepForm
		d.setFocus(reportFieldWhat)
		return d, nil
	case "y", "Y":
		return d, d.send()
	}
	d.clampOffset()
	return d, nil
}

// send files the previewed issue, exactly once, off the UI goroutine.
func (d *ReportDialog) send() tea.Cmd {
	if d.step != reportStepPreview {
		return nil
	}
	d.stopInFlight()
	d.step = reportStepSending
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	seq, is, filer := d.seq, d.issue, d.filer
	return func() tea.Msg {
		defer cancel()
		res, err := filer(ctx, is, issuereport.FileOptions{OpenBrowser: true})
		return reportFiledMsg{seq: seq, res: res, err: err}
	}
}

// OnFiled receives the result of the send.
func (d *ReportDialog) OnFiled(msg reportFiledMsg) {
	if !d.visible || msg.seq != d.seq || d.step != reportStepSending {
		return
	}
	d.cancel = nil
	d.result = msg.res
	d.resultErr = msg.err
	d.copyNote = ""
	d.step = reportStepResult
	if d.resultURL() != "" {
		d.offset = 0
		d.wrapResultURL()
	}
}

// resultURL is the prefilled new-issue URL the user has to open by hand,
// or "": a browser result where no browser was opened (headless or SSH
// host), or a failed send that still carries the URL (the browser could
// not open, or gh failed). The result step shows it in full and offers to
// copy it, so nobody has to retype the report.
func (d *ReportDialog) resultURL() string {
	if d.resultErr != nil {
		var ie *issuereport.Error
		if errors.As(d.resultErr, &ie) {
			return ie.URL
		}
		return ""
	}
	if d.result.Method == "browser" && !d.result.Opened {
		return d.result.URL
	}
	return ""
}

// wrapResultURL replaces the preview lines with the URL, cut at the
// dialog width (a URL has no spaces to wrap at).
func (d *ReportDialog) wrapResultURL() {
	d.lines = strings.Split(ansi.Hardwrap(d.resultURL(), d.innerWidth(), false), "\n")
	d.clampOffset()
}

func (d *ReportDialog) updateResult(key string) (*ReportDialog, tea.Cmd) {
	switch key {
	case "enter", "q":
		d.Hide()
		return d, nil
	}
	u := d.resultURL()
	if u == "" {
		return d, nil
	}
	switch key {
	case "c":
		seq, copyText, browserHere := d.seq, d.copyText, d.browserHere
		d.copyNote = "Copying…"
		return d, func() tea.Msg {
			method, err := copyText(u, browserHere)
			return reportCopiedMsg{seq: seq, method: method, err: err}
		}
	case "up", "k":
		d.offset--
	case "down", "j":
		d.offset++
	case "pgup", "ctrl+u", "b":
		d.offset -= max(1, d.previewRows()-1)
	case "pgdown", "ctrl+d", " ", "f":
		d.offset += max(1, d.previewRows()-1)
	}
	d.clampOffset()
	return d, nil
}

// OnCopied receives the result of "c: copy URL".
func (d *ReportDialog) OnCopied(msg reportCopiedMsg) {
	if !d.visible || msg.seq != d.seq || d.step != reportStepResult {
		return
	}
	if msg.err != nil {
		d.copyNote = "Could not copy (" + msg.err.Error() + "); select the URL above instead."
		return
	}
	if msg.method == "osc52" {
		d.copyNote = "Sent the URL to your terminal's clipboard (OSC 52); if pasting gives nothing, select the URL above."
		return
	}
	d.copyNote = "Copied the URL to the clipboard."
}

// ResultLine is the one-line outcome shown after a send ("" before one).
func (d *ReportDialog) ResultLine() string {
	if d.step != reportStepResult {
		return ""
	}
	if d.resultErr != nil {
		msg := d.resultErr.Error()
		var ie *issuereport.Error
		if errors.As(d.resultErr, &ie) {
			msg = ie.Message
			if ie.URL != "" {
				if strings.Contains(msg, ie.URL) {
					msg = strings.Replace(msg, ie.URL, "the URL below, then press Submit", 1)
				} else {
					msg += "; you can still file it: open the URL below and press Submit"
				}
			}
		}
		return "Not filed: " + msg
	}
	if d.result.Method == "gh" {
		return "Filed: " + d.result.URL
	}
	if !d.result.Opened {
		return "No browser can open here (headless or SSH session): open the URL below on any machine and press Submit to file it."
	}
	return "Opened in your browser: press Submit on the prefilled GitHub page to file it."
}

// View renders the dialog centered on the screen, or "" when hidden.
func (d *ReportDialog) View() string {
	if !d.visible {
		return ""
	}
	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorCyan)
	focusLabel := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	okStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	w := d.innerWidth()

	label := func(field int, s string) string {
		if d.focus == field {
			return focusLabel.Render("› " + s)
		}
		return labelStyle.Render("  " + s)
	}

	var rows []string
	switch d.step {
	case reportStepForm:
		heading := "Report a problem"
		if d.kind == issuereport.KindIdea {
			heading = "Share an idea"
		}
		rows = append(rows, titleStyle.Render(heading)+dimStyle.Render("  · becomes a GitHub issue after you review it"), "")
		bug, idea := "( ) Bug", "( ) Idea"
		if d.kind == issuereport.KindIdea {
			idea = "(•) Idea"
		} else {
			bug = "(•) Bug"
		}
		kindLine := label(reportFieldKind, "Kind: ") + textStyle.Render(bug+"   "+idea)
		if d.focus == reportFieldKind {
			kindLine += dimStyle.Render("   ←/→ switch")
		}
		rows = append(rows, kindLine, "")
		rows = append(rows, label(reportFieldTitle, "Title"), "  "+d.title.View(), "")
		wl, el, sl := d.labels()
		indent := lipgloss.NewStyle().PaddingLeft(2)
		rows = append(rows, label(reportFieldWhat, wl), indent.Render(d.what.View()), "")
		rows = append(rows, label(reportFieldExpected, el), indent.Render(d.expected.View()), "")
		rows = append(rows, label(reportFieldSteps, sl), indent.Render(d.steps.View()))
		if d.formErr != "" {
			rows = append(rows, "", errStyle.Render(d.formErr))
		}
		rows = append(rows, "", dimStyle.Render(renderDialogFooterRows(w, 2, "  ", []string{
			"Tab/Enter: next field", "Ctrl+J: new line", "Ctrl+P: preview", "Esc: cancel",
		})))

	case reportStepBuilding:
		rows = append(rows, titleStyle.Render("Report a problem"), "", dimStyle.Render("Building the issue…"), "", dimStyle.Render("Esc: cancel"))

	case reportStepPreview, reportStepSending, reportStepResult:
		how := "How:  in your browser (a prefilled GitHub page; you press Submit there)"
		if d.ghReady {
			how = "How:  with gh, as your GitHub account (logged in on this machine)"
		} else if !d.browserHere {
			how = "How:  a prefilled GitHub URL you open yourself (no browser on this machine); you press Submit there"
		}
		heading := "Preview: this exact text will be filed PUBLICLY"
		showURL := d.step == reportStepResult && d.resultURL() != ""
		if d.browserTrimmed() {
			heading = "Preview: this text will be filed PUBLICLY"
		}
		rows = append(rows,
			titleStyle.Render(heading),
			textStyle.Render("Where: https://github.com/"+issuereport.Repo+"/issues"),
			textStyle.Render(how),
		)
		if d.browserTrimmed() {
			rows = append(rows, errStyle.Render(ansi.Wrap(reportTrimmedNote, w, "")))
		}
		rule := dimStyle.Render(strings.Repeat("┄", w))
		if showURL {
			rows = append(rows, textStyle.Render(fmt.Sprintf("Prefilled issue URL (%d characters):", len(d.resultURL()))))
		}
		rows = append(rows, rule)
		n := d.previewRows()
		end := min(len(d.lines), d.offset+n)
		for _, line := range d.lines[d.offset:end] {
			rows = append(rows, textStyle.Render(line))
		}
		for i := end - d.offset; i < n && d.step == reportStepPreview && len(d.lines) > n; i++ {
			rows = append(rows, "")
		}
		rows = append(rows, rule)
		pos := fmt.Sprintf("lines %d-%d of %d", min(d.offset+1, len(d.lines)), end, len(d.lines))
		if len(d.lines) > n {
			pos += "  ↑/↓ PgUp/PgDn scroll"
		}
		rows = append(rows, dimStyle.Render(pos), "")
		switch d.step {
		case reportStepPreview:
			rows = append(rows, dimStyle.Render(renderDialogFooterRows(w, 2, "  ", []string{
				"y: send", "e: edit", "Esc: cancel (nothing is sent)",
			})))
		case reportStepSending:
			rows = append(rows, dimStyle.Render(reportSendingLine))
		case reportStepResult:
			line := d.ResultLine()
			style := okStyle
			if d.resultErr != nil {
				style = errStyle
			}
			rows = append(rows, style.Render(ansi.Wrap(line, w, "")))
			if showURL {
				if d.copyNote != "" {
					rows = append(rows, textStyle.Render(ansi.Wrap(d.copyNote, w, "")))
				}
				rows = append(rows, "", dimStyle.Render(renderDialogFooterRows(w, 2, "  ", []string{
					"c: copy URL", "Enter/Esc: close",
				})))
			} else {
				rows = append(rows, "", dimStyle.Render("Enter/Esc: close"))
			}
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(d.dialogWidth()).
		Render(lipgloss.JoinVertical(lipgloss.Left, rows...))

	if d.width <= 0 || d.height <= 0 {
		return box
	}
	padLeft := max(0, (d.width-lipgloss.Width(box))/2)
	padTop := max(0, (d.height-lipgloss.Height(box))/2)
	var b strings.Builder
	b.WriteString(strings.Repeat("\n", padTop))
	for _, line := range strings.Split(box, "\n") {
		b.WriteString(strings.Repeat(" ", padLeft))
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
