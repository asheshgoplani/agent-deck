package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
)

// reportTestKey turns a key name into the KeyMsg Bubble Tea would deliver.
func reportTestKey(k string) tea.KeyMsg {
	switch k {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// reportFakes replaces the dialog's diagnostics, gh check and filer so no
// test reads the real log, runs gh or opens a browser.
type reportFakes struct {
	diag     issuereport.Diagnostics
	calls    int
	gotIssue issuereport.Issue
	gotOpts  issuereport.FileOptions
	res      issuereport.FileResult
	err      error
	noGh     bool // checkGh reports false (browser fallback)
	headless bool // canOpen reports false (no browser on this machine)
}

func (f *reportFakes) install(d *ReportDialog) {
	d.collect = func(version string, logLines int) issuereport.Diagnostics { return f.diag }
	d.checkGh = func(context.Context) bool { return !f.noGh }
	d.canOpen = func() bool { return !f.headless }
	d.copyText = func(string, bool) (string, error) { return "test", nil }
	d.filer = func(_ context.Context, is issuereport.Issue, opts issuereport.FileOptions) (issuereport.FileResult, error) {
		f.calls++
		f.gotIssue = is
		f.gotOpts = opts
		return f.res, f.err
	}
}

func newReportTestHome(t *testing.T, f *reportFakes) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height = 160, 60
	f.install(h.reportDialog)
	return h
}

// press sends one key through Home.Update and runs the returned command
// chain, feeding report messages back the way the Bubble Tea runtime does.
func (f *reportFakes) press(t *testing.T, h *Home, k string) {
	t.Helper()
	before := h.reportDialog.step
	_, cmd := h.Update(reportTestKey(k))
	// Only the dialog's own async steps are run; other commands (cursor
	// blink ticks, main-screen refreshes) would sleep or touch tmux.
	if after := h.reportDialog.step; after != before && (after == reportStepBuilding || after == reportStepSending) {
		runReportCmd(h, cmd)
	}
}

func runReportCmd(h *Home, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		switch m := msg.(type) {
		case reportPreviewMsg, reportFiledMsg:
			_, cmd = h.Update(m)
		default:
			return
		}
	}
}

func reportTypeInto(t *testing.T, f *reportFakes, h *Home, s string) {
	t.Helper()
	for _, r := range s {
		f.press(t, h, string(r))
	}
}

// fillBugForm opens the form with the report key and types a bug report:
// what -> expected -> steps (the form opens on "what happened").
func fillBugForm(t *testing.T, f *reportFakes, h *Home) {
	t.Helper()
	f.press(t, h, defaultHotkeyBindings[hotkeyReportProblem])
	if !h.reportDialog.IsVisible() {
		t.Fatalf("report key %q did not open the form", defaultHotkeyBindings[hotkeyReportProblem])
	}
	reportTypeInto(t, f, h, "letters doubled")
	f.press(t, h, "enter")
	reportTypeInto(t, f, h, "each letter once")
	f.press(t, h, "enter")
	reportTypeInto(t, f, h, "type abc")
}

func TestReportKeyOpensFormFromMainScreen(t *testing.T) {
	f := &reportFakes{}
	h := newReportTestHome(t, f)
	if h.reportDialog.IsVisible() {
		t.Fatal("report form visible before the key was pressed")
	}
	f.press(t, h, defaultHotkeyBindings[hotkeyReportProblem])
	if !h.reportDialog.IsVisible() || h.reportDialog.step != reportStepForm {
		t.Fatalf("pressing %q on the main screen: visible=%v step=%v, want the form", defaultHotkeyBindings[hotkeyReportProblem], h.reportDialog.IsVisible(), h.reportDialog.step)
	}
	h.initialLoading = false
	view := ansi.Strip(h.View()) // the Home frame routes to the dialog
	for _, want := range []string{"Report a problem", "What happened?", "What did you expect?", "Steps to reproduce (optional)", "Esc: cancel"} {
		if !strings.Contains(view, want) {
			t.Errorf("form view lacks %q:\n%s", want, view)
		}
	}
	if h.reportDialog.kind != issuereport.KindBug {
		t.Fatalf("form opens as %q, want bug", h.reportDialog.kind)
	}
}

func TestReportEscAtEveryStepSendsNothing(t *testing.T) {
	cases := []struct {
		name  string
		reach func(t *testing.T, f *reportFakes, h *Home)
	}{
		{"form", func(t *testing.T, f *reportFakes, h *Home) { fillBugForm(t, f, h) }},
		{"building", func(t *testing.T, f *reportFakes, h *Home) {
			fillBugForm(t, f, h)
			// Do not run the build command: Esc arrives while it is in flight,
			// then its late result is delivered and must be dropped.
			_, cmd := h.Update(reportTestKey("ctrl+p"))
			h.Update(reportTestKey("esc"))
			runReportCmd(h, cmd)
			if h.reportDialog.IsVisible() || h.reportDialog.step == reportStepPreview {
				t.Fatalf("a build result that arrived after Esc reopened the dialog (visible=%v step=%v)", h.reportDialog.IsVisible(), h.reportDialog.step)
			}
		}},
		{"preview", func(t *testing.T, f *reportFakes, h *Home) {
			fillBugForm(t, f, h)
			f.press(t, h, "ctrl+p")
			if h.reportDialog.step != reportStepPreview {
				t.Fatalf("ctrl+p did not reach the preview (step %v)", h.reportDialog.step)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &reportFakes{}
			h := newReportTestHome(t, f)
			tc.reach(t, f, h)
			f.press(t, h, "esc")
			if h.reportDialog.IsVisible() {
				t.Fatal("Esc left the report dialog open")
			}
			// Keys after cancelling go to the main screen, never the filer.
			f.press(t, h, "y")
			if f.calls != 0 {
				t.Fatalf("filer called %d times after Esc, want 0", f.calls)
			}
		})
	}
}

func TestReportPreviewIsExactlyTheBuiltIssue(t *testing.T) {
	f := &reportFakes{diag: issuereport.Diagnostics{
		Version: "1.2.3", OS: "Linux", Arch: "amd64", Terminal: "xterm-256color",
		LogExcerpt: "line one\nline two",
	}}
	h := newReportTestHome(t, f)
	fillBugForm(t, f, h)
	f.press(t, h, "ctrl+p")
	if h.reportDialog.step != reportStepPreview {
		t.Fatalf("step = %v, want preview", h.reportDialog.step)
	}

	want := issuereport.Build(issuereport.Report{
		Kind: issuereport.KindBug, WhatHappened: "letters doubled", Expected: "each letter once",
		Steps: "type abc", Surface: "tui",
	}, f.diag)
	if got := h.reportDialog.PreviewText(); got != want.Text() {
		t.Fatalf("preview text differs from issuereport.Build output\n got: %q\nwant: %q", got, want.Text())
	}
	if !reflect.DeepEqual(h.reportDialog.issue, want) {
		t.Fatalf("issue held for sending = %+v, want %+v", h.reportDialog.issue, want)
	}

	// The pane shows the whole text: scroll through it and collect every
	// rendered line; each line of the built issue must appear.
	var seen strings.Builder
	for i := 0; i <= len(h.reportDialog.lines); i++ {
		seen.WriteString(ansi.Strip(h.reportDialog.View()))
		f.press(t, h, "j")
	}
	for _, line := range strings.Split(want.Text(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(seen.String(), line) {
			t.Errorf("preview pane never showed %q", line)
		}
	}
	if !strings.Contains(seen.String(), "Filed from agent-deck tui") {
		t.Error("preview lacks the tui footer")
	}
}

func TestReportConfirmFilesExactlyOnce(t *testing.T) {
	f := &reportFakes{res: issuereport.FileResult{Method: "gh", URL: "https://github.com/asheshgoplani/agent-deck/issues/4242"}}
	h := newReportTestHome(t, f)
	fillBugForm(t, f, h)
	f.press(t, h, "ctrl+p")
	if f.calls != 0 {
		t.Fatalf("filer called %d times before the confirm key", f.calls)
	}
	previewed := h.reportDialog.issue

	// The confirm key fires a command; a second press while it is in flight
	// and another after the result must not file again.
	_, cmd := h.Update(reportTestKey("y"))
	if cmd == nil {
		t.Fatal("confirm key returned no command (File must run off the UI goroutine)")
	}
	_, cmd2 := h.Update(reportTestKey("y"))
	runReportCmd(h, cmd)
	runReportCmd(h, cmd2)
	f.press(t, h, "y")

	if f.calls != 1 {
		t.Fatalf("filer called %d times, want exactly 1", f.calls)
	}
	if !reflect.DeepEqual(f.gotIssue, previewed) {
		t.Fatalf("filed issue differs from the previewed one\n got: %+v\nwant: %+v", f.gotIssue, previewed)
	}
	if !f.gotOpts.OpenBrowser {
		t.Fatal("TUI must file with OpenBrowser true (the browser fallback opens the prefilled page)")
	}
	if got := h.reportDialog.ResultLine(); got != "Filed: https://github.com/asheshgoplani/agent-deck/issues/4242" {
		t.Fatalf("result line = %q", got)
	}
	if !strings.Contains(ansi.Strip(h.reportDialog.View()), "issues/4242") {
		t.Fatal("result URL not on screen")
	}
}

func TestReportBrowserResultLine(t *testing.T) {
	f := &reportFakes{res: issuereport.FileResult{Method: "browser", URL: "https://github.com/asheshgoplani/agent-deck/issues/new?title=x", Opened: true}}
	h := newReportTestHome(t, f)
	fillBugForm(t, f, h)
	f.press(t, h, "ctrl+p")
	f.press(t, h, "y")
	if got := h.reportDialog.ResultLine(); !strings.HasPrefix(got, "Opened in your browser") {
		t.Fatalf("result line = %q, want the browser line", got)
	}
}

func TestReportFormNeedsADescription(t *testing.T) {
	f := &reportFakes{}
	h := newReportTestHome(t, f)
	f.press(t, h, defaultHotkeyBindings[hotkeyReportProblem])
	f.press(t, h, "ctrl+p")
	if h.reportDialog.step != reportStepForm || h.reportDialog.formErr == "" {
		t.Fatalf("empty form went to step %v (err %q), want to stay on the form with an error", h.reportDialog.step, h.reportDialog.formErr)
	}
}

func TestReportKeyInHelpAndOnboarding(t *testing.T) {
	key := defaultHotkeyBindings[hotkeyReportProblem]

	overlay := NewHelpOverlay()
	overlay.SetSize(200, 400)
	overlay.Show()
	helpView := ansi.Strip(overlay.View())
	found := false
	for _, line := range strings.Split(helpView, "\n") {
		if strings.Contains(line, " "+key+" ") && strings.Contains(line, "Report a problem") {
			found = true
		}
	}
	if !found {
		t.Fatalf("help overlay has no %q row for Report a problem:\n%s", key, helpView)
	}

	h := NewHome()
	h.width, h.height = 200, 50
	if bar := ansi.Strip(h.renderHelpBarFull()); !strings.Contains(bar, key+" Report") {
		t.Fatalf("full help bar lacks %q:\n%s", key+" Report", bar)
	}

	w := NewSetupWizard()
	w.SetReportKey(h.actionKey(hotkeyReportProblem))
	w.SetSize(120, 50)
	w.Show()
	// Welcome (Esc there skips the wizard) and Ready both carry the tip.
	for _, step := range []int{stepWelcome, stepReady} {
		w.currentStep = step
		view := ansi.Strip(w.View())
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(view, "│", " ")), " "), "Press "+key+" on the main screen to report it") {
			t.Fatalf("first-run wizard step %d does not tell users about %q:\n%s", step, key, view)
		}
	}
}

// TestReportKeyHasNoMainScreenConflict walks the real handleMainKey switch
// (mainDispatcherBindings) and the resolved hotkey lookup: exactly one
// dispatcher clause consumes the report key, and no other action's binding
// or alias resolves to it.
func TestReportKeyHasNoMainScreenConflict(t *testing.T) {
	key := defaultHotkeyBindings[hotkeyReportProblem]
	if key == "" {
		t.Fatal("report_problem has no default key")
	}
	clauses := map[string]bool{}
	for _, b := range mainDispatcherBindings(t) {
		for _, alias := range hotkeyAliases(b.key) {
			if alias == key {
				clauses[b.action] = true
			}
		}
	}
	if len(clauses) != 1 {
		t.Fatalf("main screen dispatches %q in %d clauses, want exactly 1", key, len(clauses))
	}
	for action, bound := range defaultHotkeyBindings {
		if action == hotkeyReportProblem {
			continue
		}
		for _, alias := range hotkeyAliases(bound) {
			if alias == key {
				t.Errorf("action %q also reaches %q (via %q)", action, key, bound)
			}
		}
	}
	h := NewHome()
	if got := h.normalizeMainKey(key); got != key {
		t.Fatalf("normalizeMainKey(%q) = %q, want it to stay the report key", key, got)
	}
	for _, reserved := range []string{"ctrl+e", "?", "q", "esc"} {
		if key == reserved {
			t.Fatalf("report key collides with reserved key %q", reserved)
		}
	}
}
