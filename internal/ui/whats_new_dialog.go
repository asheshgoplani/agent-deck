package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// WhatsNewDialog is the TUI's "What's new" dialog (ASK-205). It opens on
// the first start after the version changed (and on demand with the
// whats_new hotkey): your fixed reports on top, each with "Is it fixed for
// you?", then the releases' highlights. The data comes from
// internal/reportloop, the same core `agent-deck whats-new` prints.
//
// An answer is never posted from here directly: y/n -> optional comment ->
// preview of the exact public comment (from `agent-deck feedback fixed
// --dry-run --json`) -> explicit y/Enter -> the same command without
// --dry-run, run as a subprocess. The CLI owns scrubbing, gh and recording
// the answer, so there is one posting path.
//
// Steps: loading -> list -> comment -> preparing -> preview -> sending ->
// result -> list. Esc on the list closes the dialog and marks the version
// seen; Esc on the later steps goes back to the list without posting.
// While sending Esc does nothing (gh may already have posted).
type WhatsNewDialog struct {
	visible bool
	step    whatsNewStep
	width   int
	height  int

	// seq tags every async message so a late result after Esc is dropped.
	seq    int
	cancel context.CancelFunc

	wn       *reportloop.WhatsNew
	loadNote string // on-demand load failure
	selected int    // index into wn.Fixed of the report the list points at
	// answering is the report the comment/preview/result steps are about
	// (selected moves on to the next unanswered report after a post).
	answering int
	answer    string // "yes" or "no"
	comment   textinput.Model
	body      string // exact text from the dry run
	errLine   string // preparing/sending failure
	resultOK  bool
	result    string
	resultURL string // the posted comment's URL, shown on its own line
	offset    int    // first visible body line

	// Seams (tests replace them): the version shown, the reports store,
	// the changelog fetch, the agent-deck executable and the subprocess.
	version   string // "" means the package Version
	openStore func() (*reportloop.Store, error)
	fetch     func(ctx context.Context) (string, error)
	exe       func() (string, error)
	run       func(ctx context.Context, argv []string) ([]byte, error)
}

type whatsNewStep int

const (
	whatsNewStepLoading whatsNewStep = iota
	whatsNewStepList
	whatsNewStepComment
	whatsNewStepPreparing
	whatsNewStepPreview
	whatsNewStepSending
	whatsNewStepResult
)

// whatsNewLoadedMsg carries the computed notes. auto is the startup check
// (Home shows it when the screen is free); wn nil means nothing to show.
type whatsNewLoadedMsg struct {
	seq  int
	auto bool
	wn   *reportloop.WhatsNew
	err  error
}

// whatsNewPreparedMsg carries the dry run's exact comment body.
type whatsNewPreparedMsg struct {
	seq  int
	body string
	err  error
}

// whatsNewPostedMsg carries the result of posting the answer.
type whatsNewPostedMsg struct {
	seq int
	url string
	err error
}

// whatsNewSeenMsg reports that MarkSeen ran (logged on failure only).
type whatsNewSeenMsg struct{ err error }

// whatsNewTimeout bounds the changelog fetch and each subprocess.
const whatsNewTimeout = 60 * time.Second

// NewWhatsNewDialog returns a hidden dialog wired to internal/reportloop and
// the running agent-deck binary.
func NewWhatsNewDialog() *WhatsNewDialog {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 500
	ti.Placeholder = "optional: anything the maintainer should know"
	return &WhatsNewDialog{
		comment:   ti,
		openStore: reportloop.Default,
		fetch:     reportloop.FetchChangelog,
		exe:       os.Executable,
		run:       runWhatsNewCLI,
	}
}

// runWhatsNewCLI runs agent-deck with a childenv environment and returns
// its stdout; on failure stderr is folded into the error.
func runWhatsNewCLI(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- our own executable, fixed subcommand
	cmd.Env = append(childenv.ForLaunch(""), "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return out, fmt.Errorf("running agent-deck feedback fixed: %w: %s", err, s)
		}
		return out, fmt.Errorf("running agent-deck feedback fixed: %w", err)
	}
	return out, nil
}

func (d *WhatsNewDialog) currentVersion() string {
	if d.version != "" {
		return strings.TrimPrefix(d.version, "v")
	}
	return strings.TrimPrefix(Version, "v")
}

// IsVisible reports whether the dialog is on screen.
func (d *WhatsNewDialog) IsVisible() bool { return d != nil && d.visible }

// SetSize records the terminal size.
func (d *WhatsNewDialog) SetSize(width, height int) {
	if d == nil {
		return
	}
	d.width, d.height = width, height
	d.comment.Width = max(10, d.innerWidth()-3)
}

func (d *WhatsNewDialog) stopInFlight() {
	d.seq++
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

// StartupCmd is the first-start check: a fresh install (no last seen
// version) is marked seen silently; a newer version than last seen loads
// the notes, and Home shows them when they are not empty. An empty result
// is marked seen so the next start does not fetch again.
func (d *WhatsNewDialog) StartupCmd() tea.Cmd {
	version := d.currentVersion()
	openStore, fetch := d.openStore, d.fetch
	seq := d.seq
	return func() tea.Msg {
		store, err := openStore()
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: err}
		}
		st, err := store.Load()
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: err}
		}
		if st.LastSeenVersion == "" {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: store.MarkSeen(version)}
		}
		if update.CompareVersions(version, st.LastSeenVersion) <= 0 {
			return whatsNewLoadedMsg{seq: seq, auto: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), whatsNewTimeout)
		defer cancel()
		changelog, err := fetch(ctx)
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: fmt.Errorf("fetching the changelog: %w", err)}
		}
		wn, err := store.WhatsNew(changelog, version, "")
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: err}
		}
		if wn.Empty() {
			return whatsNewLoadedMsg{seq: seq, auto: true, err: store.MarkSeen(version)}
		}
		return whatsNewLoadedMsg{seq: seq, auto: true, wn: wn}
	}
}

// Open shows the dialog on demand and loads this version's notes: since
// the last seen version when that is older, else since the release before
// this one, so the current release's notes always show.
func (d *WhatsNewDialog) Open() tea.Cmd {
	d.stopInFlight()
	d.visible = true
	d.step = whatsNewStepLoading
	d.wn = nil
	d.loadNote = ""
	d.offset = 0
	version := d.currentVersion()
	openStore, fetch := d.openStore, d.fetch
	seq := d.seq
	return func() tea.Msg {
		store, err := openStore()
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, err: err}
		}
		st, err := store.Load()
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), whatsNewTimeout)
		defer cancel()
		changelog, err := fetch(ctx)
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, err: fmt.Errorf("fetching the changelog: %w", err)}
		}
		since := st.LastSeenVersion
		if since == "" || update.CompareVersions(version, since) <= 0 {
			since = previousRelease(changelog, version)
		}
		wn, err := store.WhatsNew(changelog, version, since)
		if err != nil {
			return whatsNewLoadedMsg{seq: seq, err: err}
		}
		return whatsNewLoadedMsg{seq: seq, wn: wn}
	}
}

// previousRelease is the newest changelog version older than v ("0.0.0"
// when there is none, so v's own entry still shows).
func previousRelease(changelog, v string) string {
	prev := ""
	for _, e := range update.ParseChangelog(changelog) {
		ev := strings.TrimPrefix(e.Version, "v")
		if strings.EqualFold(ev, "unreleased") || update.CompareVersions(ev, v) >= 0 {
			continue
		}
		if prev == "" || update.CompareVersions(ev, prev) > 0 {
			prev = ev
		}
	}
	if prev == "" {
		return "0.0.0"
	}
	return prev
}

// ShowNotes opens the list for already computed notes (the startup path).
func (d *WhatsNewDialog) ShowNotes(wn *reportloop.WhatsNew) {
	d.stopInFlight()
	d.visible = true
	d.wn = wn
	d.loadNote = ""
	d.toList()
	d.selected = d.firstUnanswered(0)
}

// OnLoaded receives the on-demand load (the startup load goes through Home).
func (d *WhatsNewDialog) OnLoaded(msg whatsNewLoadedMsg) {
	if !d.visible || msg.seq != d.seq || d.step != whatsNewStepLoading {
		return
	}
	if msg.err != nil {
		d.loadNote = "Could not load what's new: " + msg.err.Error()
		uiLog.Warn("whats_new_load_failed", slog.String("error", msg.err.Error()))
	}
	d.wn = msg.wn
	d.toList()
	d.selected = d.firstUnanswered(0)
}

func (d *WhatsNewDialog) toList() {
	d.step = whatsNewStepList
	d.offset = 0
	d.errLine = ""
	d.comment.Blur()
}

// firstUnanswered is the first fixed report at or after i without an answer
// (wrapping), or -1.
func (d *WhatsNewDialog) firstUnanswered(i int) int {
	if d.wn == nil {
		return -1
	}
	n := len(d.wn.Fixed)
	for k := 0; k < n; k++ {
		j := (i + k) % n
		if d.wn.Fixed[j].Feedback == nil {
			return j
		}
	}
	return -1
}

// Hide closes the dialog and returns the MarkSeen command for the version.
func (d *WhatsNewDialog) Hide() tea.Cmd {
	d.stopInFlight()
	d.visible = false
	d.comment.Blur()
	version := d.currentVersion()
	openStore := d.openStore
	return func() tea.Msg {
		store, err := openStore()
		if err != nil {
			return whatsNewSeenMsg{err: err}
		}
		return whatsNewSeenMsg{err: store.MarkSeen(version)}
	}
}

// Update handles a key while the dialog is visible.
func (d *WhatsNewDialog) Update(msg tea.KeyMsg) (*WhatsNewDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	key := msg.String()
	switch d.step {
	case whatsNewStepLoading:
		if key == "esc" || key == "q" {
			d.stopInFlight()
			d.visible = false
		}
	case whatsNewStepList:
		return d, d.updateList(key)
	case whatsNewStepComment:
		switch key {
		case "esc":
			d.toList()
		case "enter":
			return d, d.prepare()
		default:
			// The cursor-blink command is dropped: the input is redrawn on
			// every key anyway.
			d.comment, _ = d.comment.Update(msg)
		}
	case whatsNewStepPreparing:
		if key == "esc" {
			d.stopInFlight()
			d.toList()
		}
	case whatsNewStepPreview:
		switch key {
		case "esc":
			d.toList()
		case "e", "backspace":
			d.step = whatsNewStepComment
			d.errLine = ""
			d.comment.Focus()
		case "y", "Y", "enter":
			return d, d.send()
		default:
			d.scroll(key)
		}
	case whatsNewStepSending:
		// Too late to cancel honestly; wait for the result.
	case whatsNewStepResult:
		switch key {
		case "esc", "enter", "q":
			d.toList()
		}
	}
	return d, nil
}

func (d *WhatsNewDialog) updateList(key string) tea.Cmd {
	switch key {
	case "esc", "q":
		return d.Hide()
	case "tab":
		if d.selected >= 0 {
			d.selected = d.firstUnanswered(d.selected + 1)
		}
	case "y", "Y", "n", "N":
		if d.selected < 0 {
			return nil
		}
		d.answer = "yes"
		if strings.EqualFold(key, "n") {
			d.answer = "no"
		}
		d.answering = d.selected
		d.comment.SetValue("")
		d.comment.Focus()
		d.errLine = ""
		d.step = whatsNewStepComment
	default:
		d.scroll(key)
	}
	return nil
}

func (d *WhatsNewDialog) scroll(key string) {
	page := max(1, d.bodyRows()-1)
	switch key {
	case "up", "k":
		d.offset--
	case "down", "j":
		d.offset++
	case "pgup", "ctrl+u":
		d.offset -= page
	case "pgdown", "ctrl+d", " ":
		d.offset += page
	case "home", "g":
		d.offset = 0
	case "end", "G":
		d.offset = 1 << 30
	}
}

// feedbackArgv is `agent-deck feedback fixed N --fixed|--not-fixed
// [--comment C]` plus extra flags.
func (d *WhatsNewDialog) feedbackArgv(exe string, extra ...string) []string {
	f := d.wn.Fixed[d.answering]
	flag := "--fixed"
	if d.answer == "no" {
		flag = "--not-fixed"
	}
	argv := []string{exe, "feedback", "fixed", strconv.Itoa(f.Number), flag}
	if c := strings.TrimSpace(d.comment.Value()); c != "" {
		argv = append(argv, "--comment", c)
	}
	return append(argv, extra...)
}

// prepare runs the dry run that returns the exact comment body.
func (d *WhatsNewDialog) prepare() tea.Cmd {
	if d.wn == nil || d.answering < 0 || d.answering >= len(d.wn.Fixed) {
		return nil
	}
	d.stopInFlight()
	d.step = whatsNewStepPreparing
	d.comment.Blur()
	d.body, d.errLine = "", ""
	d.offset = 0
	seq, exe, run := d.seq, d.exe, d.run
	argv := d.feedbackArgv("", "--dry-run", "--json", "--yes")
	ctx, cancel := context.WithTimeout(context.Background(), whatsNewTimeout)
	d.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		path, err := exe()
		if err != nil {
			return whatsNewPreparedMsg{seq: seq, err: fmt.Errorf("finding the agent-deck executable: %w", err)}
		}
		argv[0] = path
		res, err := runFeedbackCLI(ctx, run, argv)
		if err != nil {
			return whatsNewPreparedMsg{seq: seq, err: err}
		}
		if strings.TrimSpace(res.Body) == "" {
			return whatsNewPreparedMsg{seq: seq, err: errors.New("the dry run returned no comment text")}
		}
		return whatsNewPreparedMsg{seq: seq, body: res.Body}
	}
}

// OnPrepared receives the dry run.
func (d *WhatsNewDialog) OnPrepared(msg whatsNewPreparedMsg) {
	if !d.visible || msg.seq != d.seq || d.step != whatsNewStepPreparing {
		return
	}
	d.cancel = nil
	d.step = whatsNewStepPreview
	d.offset = 0
	d.body = msg.body
	if msg.err != nil {
		d.errLine = "Could not prepare the comment: " + msg.err.Error()
	}
}

// send posts the previewed answer, exactly once.
func (d *WhatsNewDialog) send() tea.Cmd {
	if d.step != whatsNewStepPreview || d.errLine != "" || d.body == "" {
		return nil
	}
	d.stopInFlight()
	d.step = whatsNewStepSending
	seq, exe, run := d.seq, d.exe, d.run
	ctx, cancel := context.WithTimeout(context.Background(), whatsNewTimeout)
	d.cancel = cancel
	argv := d.feedbackArgv("", "--json", "--yes")
	return func() tea.Msg {
		defer cancel()
		path, err := exe()
		if err != nil {
			return whatsNewPostedMsg{seq: seq, err: fmt.Errorf("finding the agent-deck executable: %w", err)}
		}
		argv[0] = path
		res, err := runFeedbackCLI(ctx, run, argv)
		if err != nil {
			return whatsNewPostedMsg{seq: seq, err: err}
		}
		return whatsNewPostedMsg{seq: seq, url: res.URL}
	}
}

// OnPosted receives the result of posting.
func (d *WhatsNewDialog) OnPosted(msg whatsNewPostedMsg) {
	if !d.visible || msg.seq != d.seq || d.step != whatsNewStepSending {
		return
	}
	d.cancel = nil
	d.step = whatsNewStepResult
	d.offset = 0
	d.resultURL = ""
	if msg.err != nil {
		d.resultOK = false
		d.result = "Not posted: " + msg.err.Error()
		return
	}
	d.resultOK = true
	d.result = "Posted, thank you."
	d.resultURL = msg.url
	if d.wn != nil && d.answering >= 0 && d.answering < len(d.wn.Fixed) {
		d.wn.Fixed[d.answering].Feedback = &reportloop.Feedback{Answer: d.answer, Version: d.currentVersion(), At: time.Now().UTC()}
		d.selected = d.firstUnanswered(d.answering)
	}
}

// feedbackCLIResult is the --json output of `agent-deck feedback fixed`.
type feedbackCLIResult struct {
	OK      bool            `json:"ok"`
	DryRun  bool            `json:"dry_run"`
	Number  int             `json:"number"`
	Body    string          `json:"body"`
	Answer  string          `json:"answer"`
	URL     string          `json:"url"`
	Error   json.RawMessage `json:"error"`
	Message string          `json:"message"`
}

func (r feedbackCLIResult) errorText() string {
	if len(r.Error) > 0 {
		var s string
		if json.Unmarshal(r.Error, &s) == nil && s != "" {
			return s
		}
		var o struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(r.Error, &o) == nil {
			if o.Message != "" {
				return o.Message
			}
			if o.Code != "" {
				return o.Code
			}
		}
	}
	return r.Message
}

// runFeedbackCLI runs the command and decodes its JSON. The JSON's own
// error message wins over the exit status.
func runFeedbackCLI(ctx context.Context, run func(context.Context, []string) ([]byte, error), argv []string) (feedbackCLIResult, error) {
	out, runErr := run(ctx, argv)
	var res feedbackCLIResult
	decoded := false
	// The whole output is the JSON object; failing that, its last line.
	text := strings.TrimSpace(string(out))
	if json.Unmarshal([]byte(text), &res) == nil {
		decoded = true
	} else if i := strings.LastIndex(text, "\n"); i >= 0 {
		res = feedbackCLIResult{}
		decoded = json.Unmarshal([]byte(text[i+1:]), &res) == nil
	}
	switch {
	case decoded && !res.OK:
		if m := res.errorText(); m != "" {
			return res, errors.New(m)
		}
		if runErr != nil {
			return res, runErr
		}
		return res, errors.New("agent-deck feedback fixed reported a failure")
	case runErr != nil:
		return res, runErr
	case !decoded:
		return res, errors.New("agent-deck feedback fixed printed no JSON result")
	}
	return res, nil
}

func (d *WhatsNewDialog) dialogWidth() int { return fitDialogWidth(80, 40, d.width) }

// innerWidth is the content width inside border (2) and padding (4).
func (d *WhatsNewDialog) innerWidth() int { return max(20, d.dialogWidth()-6) }

// bodyRows is how many scrollable lines fit: the box takes border 2,
// padding 2, title + blank, blank + position, blank + footer (2).
func (d *WhatsNewDialog) bodyRows() int {
	if d.height <= 0 {
		return 40
	}
	rows := d.height - 12
	switch d.step {
	case whatsNewStepSending:
		rows -= 2
	case whatsNewStepResult:
		rows -= 1 + len(wrapTo(d.result, d.innerWidth(), ""))
		if d.resultURL != "" {
			rows -= len(hardwrapTo(d.resultURL, d.innerWidth()))
		}
	}
	return max(3, rows)
}

// FixedLine is the dialog's headline for a fixed report.
func whatsNewFixedLine(f reportloop.FixedReport) string {
	return fmt.Sprintf("Your report #%d: fixed in v%s — %s", f.Number, f.FixedIn, f.Title)
}

// whatsNewAsk is the question under an unanswered fixed report.
const whatsNewAsk = "Is it fixed for you?  y yes · n no"

func wrapTo(s string, w int, indent string) []string {
	out := strings.Split(ansi.Wrap(s, max(1, w-lipgloss.Width(indent)), ""), "\n")
	for i := range out {
		out[i] = indent + out[i]
	}
	return out
}

// hardwrapTo cuts a URL-like line at the width (no word breaks, so a
// hyphen in the URL is never treated as a break point).
func hardwrapTo(s string, w int) []string {
	return strings.Split(ansi.Hardwrap(s, max(1, w), false), "\n")
}

// title is the dialog heading for the current step.
func (d *WhatsNewDialog) title() string {
	v := d.currentVersion()
	if d.wn != nil && d.wn.To != "" {
		v = d.wn.To
	}
	return "What's new in v" + v
}

// bodyLines returns the scrollable content (unstyled) for the step, plus a
// style per line.
func (d *WhatsNewDialog) bodyLines() []styledLine {
	w := d.innerWidth()
	var out []styledLine
	add := func(style lipgloss.Style, s string, indent string) {
		for _, l := range wrapTo(s, w, indent) {
			out = append(out, styledLine{l, style})
		}
	}
	text := lipgloss.NewStyle().Foreground(ColorText)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	accent := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	cyan := lipgloss.NewStyle().Foreground(ColorCyan)
	green := lipgloss.NewStyle().Foreground(ColorGreen)
	red := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)

	switch d.step {
	case whatsNewStepList:
		if d.loadNote != "" {
			add(red, d.loadNote, "")
		}
		if d.wn != nil {
			for i, f := range d.wn.Fixed {
				marker := "  "
				style := green
				if i == d.selected {
					marker = "› "
					style = accent
				}
				for k, l := range wrapTo(whatsNewFixedLine(f), w, "    ") {
					if k == 0 {
						l = marker + strings.TrimPrefix(l, "    ")
					}
					out = append(out, styledLine{l, style})
				}
				if f.Feedback == nil {
					add(text, whatsNewAsk, "    ")
				} else {
					add(dim, "Your answer: "+f.Feedback.Answer+" (thank you)", "    ")
				}
			}
			for _, v := range d.wn.Versions {
				if len(v.Highlights) == 0 {
					continue
				}
				if len(out) > 0 {
					out = append(out, styledLine{"", text})
				}
				add(cyan, "v"+v.Version, "")
				for _, h := range v.Highlights {
					for i, l := range wrapTo(h, w, "    ") {
						if i == 0 {
							l = "  • " + strings.TrimPrefix(l, "    ")
						}
						out = append(out, styledLine{l, text})
					}
				}
			}
		}
		if len(out) == 0 && d.loadNote == "" {
			from := ""
			if d.wn != nil {
				from = d.wn.From
			}
			if from == "" || from == "0.0.0" {
				from = d.currentVersion()
			}
			add(dim, "Nothing new since v"+from+".", "")
		}
	case whatsNewStepPreview, whatsNewStepSending, whatsNewStepResult:
		n := 0
		if d.wn != nil && d.answering >= 0 && d.answering < len(d.wn.Fixed) {
			n = d.wn.Fixed[d.answering].Number
		}
		add(accent, "This answer will be posted PUBLICLY on GitHub.", "")
		for _, l := range hardwrapTo(fmt.Sprintf("Where: https://github.com/%s/issues/%d", issuereport.Repo, n), w) {
			out = append(out, styledLine{l, text})
		}
		add(text, "How:   with gh, as your GitHub account (logged in on this machine)", "")
		if d.errLine != "" {
			out = append(out, styledLine{"", text})
			add(red, d.errLine, "")
			break
		}
		add(text, "Exact text that will be posted (no more, no less):", "")
		rule := strings.Repeat("┄", w)
		out = append(out, styledLine{rule, dim})
		for _, line := range strings.Split(d.body, "\n") {
			add(text, line, "")
		}
		out = append(out, styledLine{rule, dim})
	}
	return out
}

type styledLine struct {
	text  string
	style lipgloss.Style
}

// View renders the dialog centered on the screen, or "" when hidden.
func (d *WhatsNewDialog) View() string {
	if !d.visible {
		return ""
	}
	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	textStyle := lipgloss.NewStyle().Foreground(ColorText)
	okStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	w := d.innerWidth()

	rows := []string{titleStyle.Render(truncateDisplay(d.title(), w, "…"))}
	footer := func(items ...string) {
		rows = append(rows, "", dimStyle.Render(renderDialogFooterRows(w, 2, "  ", items)))
	}
	switch d.step {
	case whatsNewStepLoading:
		rows = append(rows, "", dimStyle.Render("Loading…"))
		footer("Esc: close")
	case whatsNewStepComment:
		ans := "yes, it is fixed"
		if d.answer == "no" {
			ans = "no, still broken"
		}
		n := d.wn.Fixed[d.answering].Number
		rows = append(rows, "")
		for _, l := range wrapTo(fmt.Sprintf("Your answer on #%d: %s", n, ans), w, "") {
			rows = append(rows, textStyle.Render(l))
		}
		rows = append(rows, "", textStyle.Render("Comment (optional, public):"), "  "+d.comment.View())
		footer("Enter: preview", "Esc: back")
	case whatsNewStepPreparing:
		rows = append(rows, "", dimStyle.Render("Preparing the exact comment…"))
		footer("Esc: back")
	default:
		lines := d.bodyLines()
		n := d.bodyRows()
		maxOff := max(0, len(lines)-n)
		d.offset = min(max(0, d.offset), maxOff)
		end := min(len(lines), d.offset+n)
		rows = append(rows, "")
		for _, l := range lines[d.offset:end] {
			rows = append(rows, l.style.Render(l.text))
		}
		if len(lines) > n {
			rows = append(rows, dimStyle.Render(fmt.Sprintf("lines %d-%d of %d  ↑/↓ scroll", d.offset+1, end, len(lines))))
		}
		switch d.step {
		case whatsNewStepList:
			items := []string{}
			if d.selected >= 0 {
				items = append(items, "y/n: answer")
				if d.firstUnanswered(d.selected+1) != d.selected {
					items = append(items, "Tab: next report")
				}
			}
			footer(append(items, "Esc: close")...)
		case whatsNewStepPreview:
			if d.errLine != "" {
				footer("e: edit comment", "Esc: back (nothing is posted)")
			} else {
				footer("y/Enter: post", "e: edit comment", "Esc: back (nothing is posted)")
			}
		case whatsNewStepSending:
			rows = append(rows, "", dimStyle.Render("Posting… (can't be cancelled once started)"))
		case whatsNewStepResult:
			style := okStyle
			if !d.resultOK {
				style = errStyle
			}
			rows = append(rows, "")
			for _, l := range wrapTo(d.result, w, "") {
				rows = append(rows, style.Render(l))
			}
			if d.resultURL != "" {
				for _, l := range hardwrapTo(d.resultURL, w) {
					rows = append(rows, textStyle.Render(l))
				}
			}
			footer("Enter/Esc: back to the list")
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

// whatsNewStartupSkipped: AGENTDECK_SKIP_UPDATE_CHECK also skips the
// startup changelog fetch, unless AGENTDECK_CHANGELOG_URL points it
// somewhere explicitly (the sandbox simulation).
func whatsNewStartupSkipped() bool {
	if strings.TrimSpace(os.Getenv(reportloop.ChangelogEnv)) != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(update.SkipUpdateCheckEnv))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// whatsNewStartupCmd is the check Init runs (see WhatsNewDialog.StartupCmd).
func (h *Home) whatsNewStartupCmd() tea.Cmd {
	if h.whatsNewDialog == nil {
		return nil
	}
	return h.whatsNewDialog.StartupCmd()
}

// onWhatsNewLoaded routes a load: the startup result waits in
// whatsNewPending until the screen is free; on demand goes to the dialog.
func (h *Home) onWhatsNewLoaded(msg whatsNewLoadedMsg) {
	if h.whatsNewDialog == nil {
		return
	}
	if !msg.auto {
		h.whatsNewDialog.OnLoaded(msg)
		return
	}
	if msg.err != nil {
		uiLog.Warn("whats_new_startup_failed", slog.String("error", msg.err.Error()))
		return
	}
	if msg.wn == nil || msg.wn.Empty() {
		return
	}
	h.whatsNewPending = msg.wn
	h.maybeShowWhatsNew()
}

// maybeShowWhatsNew opens the pending startup notes once the splash is gone
// and no other dialog or insert mode holds the screen.
func (h *Home) maybeShowWhatsNew() {
	if h.whatsNewPending == nil || h.whatsNewDialog == nil || h.whatsNewDialog.IsVisible() {
		return
	}
	if h.initialLoading || h.insertMode || h.hasModalVisible() {
		return
	}
	wn := h.whatsNewPending
	h.whatsNewPending = nil
	h.whatsNewDialog.ShowNotes(wn)
	h.whatsNewDialog.SetSize(h.width, h.height)
}
