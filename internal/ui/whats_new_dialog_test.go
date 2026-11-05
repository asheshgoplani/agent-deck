package ui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/reportloop"
)

const whatsNewTestChangelog = `# Changelog

## [Unreleased]

## [1.16.37] - 2026-11-09

### Highlights

- Queued messages are delivered after a restart.
- Forks finish in seconds again.

### Fixes

- Letters were doubled when typing into a session (#4242, #4250).

## [1.16.36] - 2026-11-02

### Fixes

- Old fix (#9003).
`

const whatsNewTestExe = "/opt/agent-deck/bin/agent-deck"

// whatsNewFakes replaces the dialog's store path, changelog fetch and the
// `agent-deck feedback fixed` subprocess so no test touches the network,
// the real data dir or a real binary.
type whatsNewFakes struct {
	store      *reportloop.Store
	changelog  string
	fetchErr   error
	fetchCalls int
	runs       [][]string
	// out is what the subprocess prints for each call, in order; a call past
	// the end gets the last entry.
	out    []string
	runErr error
}

func (f *whatsNewFakes) install(d *WhatsNewDialog, version string) {
	d.version = version
	d.openStore = func() (*reportloop.Store, error) { return f.store, nil }
	d.fetch = func(context.Context) (string, error) {
		f.fetchCalls++
		return f.changelog, f.fetchErr
	}
	d.exe = func() (string, error) { return whatsNewTestExe, nil }
	d.run = func(_ context.Context, argv []string) ([]byte, error) {
		f.runs = append(f.runs, append([]string(nil), argv...))
		out := ""
		if len(f.out) > 0 {
			out = f.out[min(len(f.runs), len(f.out))-1]
		}
		return []byte(out), f.runErr
	}
}

func newWhatsNewFakes(t *testing.T, lastSeen string, reports ...reportloop.Report) *whatsNewFakes {
	t.Helper()
	s := &reportloop.Store{Path: filepath.Join(t.TempDir(), "reports.json")}
	for _, r := range reports {
		if err := s.Record(r); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if lastSeen != "" {
		if err := s.MarkSeen(lastSeen); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
	}
	return &whatsNewFakes{store: s, changelog: whatsNewTestChangelog}
}

func newWhatsNewTestHome(t *testing.T, f *whatsNewFakes, version string) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height = 160, 60
	h.initialLoading = false
	f.install(h.whatsNewDialog, version)
	return h
}

// runWhatsNewCmd runs a command chain, feeding the dialog's own messages
// back through Home.Update the way the Bubble Tea runtime does.
func runWhatsNewCmd(h *Home, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		switch m := msg.(type) {
		case whatsNewLoadedMsg, whatsNewPreparedMsg, whatsNewPostedMsg, whatsNewSeenMsg:
			_, cmd = h.Update(m)
		case tea.BatchMsg:
			for _, c := range m {
				runWhatsNewCmd(h, c)
			}
			return
		default:
			return
		}
	}
}

// pressWN sends a key and runs the dialog's async steps (never the main
// screen's commands, which would touch tmux).
func pressWN(h *Home, k tea.KeyMsg) {
	wasVisible := h.whatsNewDialog.IsVisible()
	_, cmd := h.Update(k)
	if wasVisible || h.whatsNewDialog.IsVisible() {
		runWhatsNewCmd(h, cmd)
	}
}

func typeWN(h *Home, s string) {
	for _, r := range s {
		pressWN(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

var whatsNewKey = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true}

func fixedReport() reportloop.Report {
	return reportloop.Report{Number: 4242, Title: "letters doubled when typing", Kind: "bug", FiledVersion: "1.16.36"}
}

// startUp runs the TUI's startup check as Init does.
func startUp(t *testing.T, h *Home) {
	t.Helper()
	runWhatsNewCmd(h, h.whatsNewStartupCmd())
}

func viewLines(s string) []string {
	var out []string
	for _, l := range strings.Split(ansi.Strip(s), "\n") {
		l = strings.Trim(l, " │╭╮╰╯─")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func indexOfLine(lines []string, sub string) int {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func TestWhatsNewDialogShowsFixedReportOnTop(t *testing.T) {
	f := newWhatsNewFakes(t, "1.16.36", fixedReport())
	h := newWhatsNewTestHome(t, f, "1.16.37")
	startUp(t, h)
	if !h.whatsNewDialog.IsVisible() {
		t.Fatal("first start on a newer version did not open What's new")
	}
	lines := viewLines(h.View()) // the Home frame routes to the dialog
	title := indexOfLine(lines, "What's new in v1.16.37")
	report := indexOfLine(lines, "Your report #4242: fixed in v1.16.37 — letters doubled when typing")
	ask := indexOfLine(lines, "Is it fixed for you?  y yes · n no")
	hl := indexOfLine(lines, "Queued messages are delivered after a restart.")
	if title < 0 || report < 0 || ask < 0 || hl < 0 {
		t.Fatalf("view lacks title=%d report=%d ask=%d highlight=%d:\n%s", title, report, ask, hl, strings.Join(lines, "\n"))
	}
	if title != 0 || report != title+1 || !(ask > report && hl > ask) {
		t.Fatalf("want title, then the fixed report, then the question, then highlights; got title=%d report=%d ask=%d highlight=%d:\n%s", title, report, ask, hl, strings.Join(lines, "\n"))
	}
	if len(f.runs) != 0 {
		t.Fatalf("opening the dialog ran %v", f.runs)
	}
}

func TestWhatsNewDialogFeedbackSendsExpectedArgv(t *testing.T) {
	body := "Fixed for me in v1.16.37.\n\nworks now\n\n(linux/amd64, via agent-deck feedback fixed)"
	for _, tc := range []struct {
		name, key, comment, flag string
		wantBody                 string
	}{
		{"fixed with comment", "y", "works now", "--fixed", body},
		{"not fixed, no comment", "n", "", "--not-fixed", "Still broken for me in v1.16.37.\n\n(linux/amd64, via agent-deck feedback fixed)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWhatsNewFakes(t, "1.16.36", fixedReport())
			f.out = []string{
				`{"ok":true,"dry_run":true,"number":4242,"body":` + jsonQuote(tc.wantBody) + `}`,
				`{"ok":true,"number":4242,"answer":"yes","url":"https://github.com/asheshgoplani/agent-deck/issues/4242#issuecomment-1"}`,
			}
			h := newWhatsNewTestHome(t, f, "1.16.37")
			startUp(t, h)

			pressWN(h, reportTestKey(tc.key))
			if h.whatsNewDialog.step != whatsNewStepComment {
				t.Fatalf("%s did not open the comment input (step %v)", tc.key, h.whatsNewDialog.step)
			}
			typeWN(h, tc.comment)
			pressWN(h, reportTestKey("enter"))
			if h.whatsNewDialog.step != whatsNewStepPreview {
				t.Fatalf("Enter on the comment did not reach the preview (step %v, err %q)", h.whatsNewDialog.step, h.whatsNewDialog.errLine)
			}
			base := []string{whatsNewTestExe, "feedback", "fixed", "4242", tc.flag}
			if tc.comment != "" {
				base = append(base, "--comment", tc.comment)
			}
			wantDry := append(append([]string(nil), base...), "--dry-run", "--json", "--yes")
			if len(f.runs) != 1 || !reflect.DeepEqual(f.runs[0], wantDry) {
				t.Fatalf("preview ran %q, want one dry run %q", f.runs, wantDry)
			}

			view := ansi.Strip(h.whatsNewDialog.View())
			flat := strings.Join(strings.Fields(strings.ReplaceAll(view, "│", " ")), " ")
			for _, want := range []string{
				"posted PUBLICLY on GitHub",
				"https://github.com/asheshgoplani/agent-deck/issues/4242",
				"Exact text that will be posted (no more, no less):",
				"Esc: back (nothing is posted)",
			} {
				if !strings.Contains(flat, want) {
					t.Errorf("preview lacks %q:\n%s", want, view)
				}
			}
			for _, line := range strings.Split(tc.wantBody, "\n") {
				if line != "" && !strings.Contains(view, line) {
					t.Errorf("preview lacks body line %q:\n%s", line, view)
				}
			}

			pressWN(h, reportTestKey("y"))
			wantPost := append(append([]string(nil), base...), "--json", "--yes")
			if len(f.runs) != 2 || !reflect.DeepEqual(f.runs[1], wantPost) {
				t.Fatalf("send ran %q, want %q", f.runs, wantPost)
			}
			if v := ansi.Strip(h.whatsNewDialog.View()); h.whatsNewDialog.step != whatsNewStepResult || !strings.Contains(v, "Posted, thank you.") || !strings.Contains(v, "https://github.com/asheshgoplani/agent-deck/issues/4242#issuecomment-1") {
				t.Fatalf("result not shown (step %v):\n%s", h.whatsNewDialog.step, ansi.Strip(h.whatsNewDialog.View()))
			}
			// A second y on the result never posts again.
			pressWN(h, reportTestKey("y"))
			if len(f.runs) != 2 {
				t.Fatalf("posted again: %q", f.runs)
			}
			// Back on the list the question is answered, not asked again.
			pressWN(h, reportTestKey("enter"))
			if h.whatsNewDialog.step != whatsNewStepList {
				t.Fatalf("Enter on the result: step %v, want the list", h.whatsNewDialog.step)
			}
			if v := ansi.Strip(h.whatsNewDialog.View()); strings.Contains(v, "Is it fixed for you?") || !strings.Contains(v, "Your answer:") {
				t.Fatalf("list still asks after answering:\n%s", v)
			}
		})
	}
}

func jsonQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestWhatsNewDialogSendFailureIsShown(t *testing.T) {
	f := newWhatsNewFakes(t, "1.16.36", fixedReport())
	f.out = []string{
		`{"ok":true,"dry_run":true,"number":4242,"body":"Fixed for me in v1.16.37."}`,
		`{"ok":false,"error":{"code":"gh_failed","message":"gh is not logged in"}}`,
	}
	h := newWhatsNewTestHome(t, f, "1.16.37")
	startUp(t, h)
	pressWN(h, reportTestKey("y"))
	pressWN(h, reportTestKey("enter"))
	f.runErr = errors.New("exit status 1")
	pressWN(h, reportTestKey("enter")) // Enter posts too
	if len(f.runs) != 2 {
		t.Fatalf("runs = %q, want dry run + post", f.runs)
	}
	if v := ansi.Strip(h.whatsNewDialog.View()); !strings.Contains(v, "Not posted: gh is not logged in") {
		t.Fatalf("failure not shown:\n%s", v)
	}
}

func TestWhatsNewDialogEscMarksSeen(t *testing.T) {
	f := newWhatsNewFakes(t, "1.16.36", fixedReport())
	h := newWhatsNewTestHome(t, f, "1.16.37")
	startUp(t, h)
	if !h.whatsNewDialog.IsVisible() {
		t.Fatal("dialog not shown")
	}
	st, _ := f.store.Load()
	if st.LastSeenVersion != "1.16.36" {
		t.Fatalf("opening marked seen already: %q", st.LastSeenVersion)
	}
	pressWN(h, reportTestKey("esc"))
	if h.whatsNewDialog.IsVisible() {
		t.Fatal("Esc left the dialog open")
	}
	st, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSeenVersion != "1.16.37" {
		t.Fatalf("last seen = %q after Esc, want 1.16.37", st.LastSeenVersion)
	}
	if len(f.runs) != 0 {
		t.Fatalf("Esc ran %q", f.runs)
	}
	// The next start shows nothing.
	h2 := newWhatsNewTestHome(t, f, "1.16.37")
	startUp(t, h2)
	if h2.whatsNewDialog.IsVisible() {
		t.Fatal("What's new shown again for a version already seen")
	}
}

func TestWhatsNewDialogFreshInstallMarksSeenSilently(t *testing.T) {
	f := newWhatsNewFakes(t, "")
	h := newWhatsNewTestHome(t, f, "1.16.37")
	startUp(t, h)
	if h.whatsNewDialog.IsVisible() {
		t.Fatal("fresh install opened What's new")
	}
	if f.fetchCalls != 0 {
		t.Fatalf("fresh install fetched the changelog %d times", f.fetchCalls)
	}
	st, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSeenVersion != "1.16.37" {
		t.Fatalf("last seen = %q, want 1.16.37", st.LastSeenVersion)
	}
}

// The startup dialog waits while another dialog is up (or the splash is on)
// and opens once the screen is free.
func TestWhatsNewDialogWaitsForOtherDialogs(t *testing.T) {
	f := newWhatsNewFakes(t, "1.16.36", fixedReport())
	h := newWhatsNewTestHome(t, f, "1.16.37")
	h.reportDialog.Show("")
	startUp(t, h)
	if h.whatsNewDialog.IsVisible() {
		t.Fatal("What's new opened on top of another dialog")
	}
	h.reportDialog.Hide()
	h.maybeShowWhatsNew()
	if !h.whatsNewDialog.IsVisible() {
		t.Fatal("pending What's new did not open once the screen was free")
	}
}

func TestWhatsNewDialogKeyOpensOnDemand(t *testing.T) {
	f := newWhatsNewFakes(t, "1.16.37", fixedReport())
	h := newWhatsNewTestHome(t, f, "1.16.37")
	pressWN(h, whatsNewKey)
	if !h.whatsNewDialog.IsVisible() {
		t.Fatalf("%q did not open What's new", whatsNewKey.String())
	}
	view := ansi.Strip(h.whatsNewDialog.View())
	if !strings.Contains(view, "What's new in v1.16.37") || !strings.Contains(view, "Forks finish in seconds again.") {
		t.Fatalf("on-demand view lacks this version's notes:\n%s", view)
	}
}

// TestWhatsNewDialogKeyHasNoMainScreenConflict: exactly one main-screen
// dispatcher clause takes the key and no other action's binding reaches it.
func TestWhatsNewDialogKeyHasNoMainScreenConflict(t *testing.T) {
	key := defaultHotkeyBindings[hotkeyWhatsNew]
	if key == "" {
		t.Fatal("whats_new has no default key")
	}
	if whatsNewKey.String() != key {
		t.Fatalf("test key %q is not the bound key %q", whatsNewKey.String(), key)
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
		if action == hotkeyWhatsNew {
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
		t.Fatalf("normalizeMainKey(%q) = %q", key, got)
	}
	for _, reserved := range []string{"ctrl+e", "?", "q", "esc"} {
		if key == reserved {
			t.Fatalf("whats_new key collides with reserved key %q", reserved)
		}
	}

	overlay := NewHelpOverlay()
	overlay.SetSize(200, 400)
	overlay.Show()
	found := false
	for _, line := range strings.Split(ansi.Strip(overlay.View()), "\n") {
		if strings.Contains(line, key) && strings.Contains(line, "What's new") {
			found = true
		}
	}
	if !found {
		t.Fatalf("help has no %q row for What's new", key)
	}
}

// TestWhatsNewDialogFitsIn80Columns renders every step at 80x24 and checks
// that no line is wider than the terminal. With -v it logs the renders.
func TestWhatsNewDialogFitsIn80Columns(t *testing.T) {
	long := reportloop.Report{Number: 4250, Title: "a very long report title that goes on and on about letters being doubled in the embedded terminal pane", FiledVersion: "1.16.36"}
	f := newWhatsNewFakes(t, "1.16.36", fixedReport(), long)
	f.out = []string{
		`{"ok":true,"dry_run":true,"number":4242,"body":"Fixed for me in v1.16.37.\n\nworks now, thanks; this comment is long enough to need wrapping at eighty columns for sure\n\n(linux/amd64, via agent-deck feedback fixed)"}`,
		`{"ok":true,"number":4242,"answer":"yes","url":"https://github.com/asheshgoplani/agent-deck/issues/4242#issuecomment-123456789"}`,
	}
	h := newWhatsNewTestHome(t, f, "1.16.37")
	h.width, h.height = 80, 24
	startUp(t, h)
	h.whatsNewDialog.SetSize(80, 24)

	check := func(name string) {
		t.Helper()
		view := h.whatsNewDialog.View()
		for i, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("%s: line %d is %d cells wide (> 80): %q", name, i, w, ansi.Strip(line))
			}
		}
		if n := len(strings.Split(strings.TrimRight(view, "\n"), "\n")); n > 24 {
			t.Errorf("%s: %d lines tall (> 24)", name, n)
		}
		if testing.Verbose() {
			t.Logf("render %s (80x24):\n%s", name, ansi.Strip(view))
		}
	}
	check("list")
	pressWN(h, reportTestKey("y"))
	typeWN(h, "works now, thanks; this comment is long enough to need wrapping at eighty columns for sure")
	check("comment")
	pressWN(h, reportTestKey("enter"))
	check("preview")
	pressWN(h, reportTestKey("y"))
	check("result")
}
