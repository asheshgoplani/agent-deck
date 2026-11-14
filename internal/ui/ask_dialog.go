package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/ask"
)

// The TUI side of ASK-117 ("ask the owner", docs/ask.md): a modal for the
// oldest pending ask request of this TUI's profile that the Mac app has not
// shown within askAppGrace. The secret value lives only in the masked
// textinput until Save; it then goes, as a []byte, straight to the in-process
// ask.Deliver (vault stdin) and the input is reset. Nothing here logs: Home
// routes every key to the modal before its keypress log, and this file has no
// logging at all. The modal has no "show" toggle and never renders the value:
// the TUI may run inside tmux, where an agent can capture-pane.

const (
	askAppGrace     = 3 * time.Second  // the app gets this long to mark a request seen
	askCancelCool   = 30 * time.Second // no new modal from a session this long after a Cancel
	askPollEvery    = time.Second      // minimum gap between ask dir polls
	askTickEvery    = time.Second      // countdown / answered-elsewhere check while open
	askStoredLinger = time.Second      // "Stored" stays this long
	askTypingGuard  = time.Second      // no modal while the user is typing elsewhere
	askReasonLines  = 2
	askQuestionLns  = 4
)

// askAPI is the in-process ask core the dialog calls; tests wrap it.
type askAPI struct {
	list     func(profile string) ([]ask.Record, error)
	show     func(profile, id string) (ask.Record, error)
	markSeen func(profile, id, by string) (string, error)
	deliver  func(profile string, h ask.Header, value []byte) (ask.Result, error)
	cancel   func(profile, id, by, nonce string) (ask.Result, error)
}

func defaultAskAPI() askAPI {
	return askAPI{list: ask.List, show: ask.Show, markSeen: ask.MarkSeen, deliver: ask.Deliver, cancel: ask.Cancel}
}

type askPhase int

const (
	askPhaseEditing askPhase = iota // field / options active
	askPhaseSaving                  // Deliver running
	askPhaseStored                  // delivered by us; closes after askStoredLinger
	askPhaseFinal                   // a notice (answered elsewhere, expired, refused); any key closes
)

// Messages. None carries a value.
type (
	askPollMsg struct {
		pending []ask.Record
		openID  string     // the request open when the poll started
		open    ask.Record // its current public state
		openErr error
	}
	askSeenMsg struct {
		id    string
		nonce string
		err   error
	}
	askDeliveredMsg struct {
		id  string
		res ask.Result
		err error
		now ask.Record // the record after a not-pending refusal
	}
	askCancelledMsg struct {
		id  string
		err error
	}
	askTickMsg  struct{ id string }
	askCloseMsg struct{ id string }
)

// AskDialog is the ask modal. One request at a time.
type AskDialog struct {
	api     askAPI
	profile string
	now     func() time.Time

	visible bool
	phase   askPhase
	rec     ask.Record
	nonce   string // from MarkSeen; empty until it returns (Save disabled)
	input   textinput.Model
	choice  int // focused row for a choice: options, then Cancel
	errMsg  string
	notice  string
	sub     string
	queued  int

	cooldown     map[string]time.Time // session id -> no modal before
	done         map[string]bool      // requests this TUI closed; never reopened
	pollInFlight bool
	lastPoll     time.Time
	width        int
	height       int
}

// NewAskDialog returns a hidden dialog for profile (already resolved).
func NewAskDialog(profile string) *AskDialog {
	return &AskDialog{
		api:      defaultAskAPI(),
		profile:  profile,
		now:      time.Now,
		cooldown: map[string]time.Time{},
		done:     map[string]bool{},
		input:    newAskInput(false),
	}
}

func newAskInput(secret bool) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = ""
	ti.ShowSuggestions = false
	// Never read the clipboard (ctrl+v runs a clipboard tool); terminal
	// paste still arrives as ordinary key input.
	ti.KeyMap.Paste.SetEnabled(false)
	ti.Cursor.SetMode(cursor.CursorStatic)
	if secret {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
		ti.CharLimit = ask.MaxSecretBytes
	} else {
		ti.CharLimit = ask.MaxTextAnswer
	}
	return ti
}

// IsVisible is nil-safe (Home values built without NewHome have no dialog).
func (d *AskDialog) IsVisible() bool { return d != nil && d.visible }

func (d *AskDialog) SetSize(width, height int) {
	if d == nil {
		return
	}
	d.width, d.height = width, height
}

// PollCmd lists the profile's pending requests off the UI goroutine. At most
// one poll runs at a time and polls are at least askPollEvery apart.
func (d *AskDialog) PollCmd(now time.Time) tea.Cmd {
	if d == nil || d.profile == "" || d.pollInFlight || (!d.lastPoll.IsZero() && now.Sub(d.lastPoll) < askPollEvery) {
		return nil
	}
	d.pollInFlight = true
	d.lastPoll = now
	api, profile := d.api, d.profile
	openID := ""
	if d.visible && d.phase == askPhaseEditing {
		openID = d.rec.RequestID
	}
	return func() tea.Msg {
		recs, _ := api.list(profile)
		msg := askPollMsg{pending: recs, openID: openID}
		if openID != "" {
			msg.open, msg.openErr = api.show(profile, openID)
		}
		return msg
	}
}

// HandleMsg handles the dialog's own messages; it returns nil for others.
// canShow reports whether Home can put a modal up now.
func (d *AskDialog) HandleMsg(msg tea.Msg, canShow func() bool) tea.Cmd {
	if d == nil {
		return nil
	}
	switch m := msg.(type) {
	case askPollMsg:
		return d.onPoll(m, canShow)
	case askSeenMsg:
		if !d.visible || m.id != d.rec.RequestID {
			return nil
		}
		if m.err != nil {
			d.finish(d.refusalNotice(m.err), "")
			return nil
		}
		d.nonce = m.nonce
		return d.tickCmd()
	case askTickMsg:
		if !d.visible || m.id != d.rec.RequestID {
			return nil
		}
		if d.phase == askPhaseEditing && !d.now().Before(d.rec.ExpiresAt) {
			d.finish("Expired, nothing was sent", "")
			return nil
		}
		if d.phase == askPhaseFinal {
			return nil
		}
		return d.tickCmd()
	case askDeliveredMsg:
		if !d.visible || m.id != d.rec.RequestID {
			return nil
		}
		return d.onDelivered(m)
	case askCancelledMsg:
		return nil
	case askCloseMsg:
		if d.visible && m.id == d.rec.RequestID && d.phase == askPhaseStored {
			d.hide()
		}
		return nil
	}
	return nil
}

func (d *AskDialog) onPoll(m askPollMsg, canShow func() bool) tea.Cmd {
	d.pollInFlight = false
	now := d.now()
	if d.visible {
		d.queued = 0
		for _, r := range m.pending {
			if r.RequestID != d.rec.RequestID && d.eligible(r, now) {
				d.queued++
			}
		}
		if m.openID != "" && m.openID == d.rec.RequestID && d.phase == askPhaseEditing {
			switch {
			case m.openErr != nil:
				d.finish("This request is gone, nothing was sent", "")
			case m.open.State != ask.StatePending:
				d.finishElsewhere(m.open)
			}
		}
		return nil
	}
	if canShow == nil || !canShow() {
		return nil
	}
	var pick *ask.Record
	queued := 0
	for i := range m.pending {
		r := m.pending[i]
		if !d.eligible(r, now) {
			continue
		}
		if pick == nil {
			pick = &m.pending[i]
		} else {
			queued++
		}
	}
	if pick == nil {
		return nil
	}
	d.open(*pick)
	d.queued = queued
	api, profile, id := d.api, d.profile, pick.RequestID
	return func() tea.Msg {
		nonce, err := api.markSeen(profile, id, "tui")
		return askSeenMsg{id: id, nonce: nonce, err: err}
	}
}

// eligible: pending, this profile, not expired, not closed here, the app has
// had askAppGrace to show it and has no fresh seen (ask.AppShowing: a seen
// with no deliver or cancel for ask.SeenHold no longer hides it), no
// cooldown for its session.
func (d *AskDialog) eligible(r ask.Record, now time.Time) bool {
	switch {
	case r.State != ask.StatePending, d.done[r.RequestID], !now.Before(r.ExpiresAt):
		return false
	case r.Profile != "" && r.Profile != d.profile:
		return false
	case ask.AppShowing(r, now):
		return false
	case now.Sub(r.RequestedAt) < askAppGrace:
		return false
	case now.Before(d.cooldown[r.SessionID]):
		return false
	}
	return true
}

// open shows rec (MarkSeen runs separately; Save waits for its nonce).
func (d *AskDialog) open(rec ask.Record) {
	d.visible = true
	d.phase = askPhaseEditing
	d.rec = rec
	d.nonce = ""
	d.errMsg, d.notice, d.sub = "", "", ""
	d.choice = 0
	d.input = newAskInput(rec.Kind == ask.KindSecret)
	if rec.Kind != ask.KindChoice {
		d.input.Focus()
	}
}

// clearValue drops what was typed. Best effort: Go strings cannot be wiped.
func (d *AskDialog) clearValue() {
	d.input.Reset()
	d.input.Blur()
}

func (d *AskDialog) hide() {
	d.clearValue()
	d.visible = false
	d.done[d.rec.RequestID] = true
	d.nonce = ""
}

func (d *AskDialog) finish(notice, sub string) {
	d.clearValue()
	d.phase = askPhaseFinal
	d.notice, d.sub = notice, sub
	d.errMsg = ""
	d.done[d.rec.RequestID] = true
}

func (d *AskDialog) finishElsewhere(r ask.Record) {
	by := ask.SanitizeText(r.AnsweredBy, 20)
	if by == "" {
		by = "unknown"
	}
	at := ""
	if !r.FinishedAt.IsZero() {
		at = "at " + r.FinishedAt.Local().Format("15:04:05")
	}
	switch r.State {
	case ask.StateStored, ask.StateAnswered:
		d.finish(fmt.Sprintf("Answered elsewhere (%s), not by you", by), at)
	case ask.StateCancelled:
		d.finish(fmt.Sprintf("Cancelled elsewhere (%s), not by you", by), at)
	default:
		d.finish("Expired, nothing was sent", at)
	}
}

func (d *AskDialog) refusalNotice(err error) string {
	if errors.Is(err, ask.ErrExpired) {
		return "Expired, nothing was sent"
	}
	if ask.IsRefusal(err) {
		return "Not sent: " + ask.SanitizeText(err.Error(), 120)
	}
	return "Not sent: could not save (internal error)"
}

func (d *AskDialog) onDelivered(m askDeliveredMsg) tea.Cmd {
	if m.err == nil {
		d.phase = askPhaseStored
		if d.rec.Kind == ask.KindSecret {
			d.notice = fmt.Sprintf("Stored %s (replaced=%s)", d.rec.Name, ask.SanitizeText(m.res.Replaced, 4))
		} else {
			d.notice = "Sent to the agent"
		}
		d.done[d.rec.RequestID] = true
		id := d.rec.RequestID
		return tea.Tick(askStoredLinger, func(time.Time) tea.Msg { return askCloseMsg{id: id} })
	}
	switch {
	case errors.Is(m.err, ask.ErrNotPending) && m.now.RequestID != "":
		d.finishElsewhere(m.now)
	case errors.Is(m.err, ask.ErrExpired), errors.Is(m.err, ask.ErrNotPending), errors.Is(m.err, ask.ErrNotFound),
		errors.Is(m.err, ask.ErrHeaderMismatch), errors.Is(m.err, ask.ErrReplaceMismatch), errors.Is(m.err, ask.ErrScope),
		errors.Is(m.err, ask.ErrDisabled):
		d.finish(d.refusalNotice(m.err), "")
	default:
		// Retryable (vault refused, empty, not an option, looks like a
		// secret, ...): back to the field, which is already empty.
		d.phase = askPhaseEditing
		if ask.IsRefusal(m.err) {
			d.errMsg = ask.SanitizeText(m.err.Error(), 120)
		} else {
			d.errMsg = "could not save (internal error)"
		}
		d.input.Focus()
		return d.tickCmd()
	}
	return nil
}

func (d *AskDialog) tickCmd() tea.Cmd {
	id := d.rec.RequestID
	return tea.Tick(askTickEvery, func(time.Time) tea.Msg { return askTickMsg{id: id} })
}

func (d *AskDialog) header() ask.Header {
	return ask.Header{
		RequestID: d.rec.RequestID, Kind: d.rec.Kind, Name: d.rec.Name, Replace: d.rec.Replace,
		Nonce: d.nonce, AnsweredBy: "tui",
	}
}

func (d *AskDialog) canSave() bool {
	return d.phase == askPhaseEditing && d.nonce != "" && d.input.Value() != ""
}

// save hands the value to Deliver in a tea.Cmd (Deliver holds the ask lock
// while the vault runs). The input is reset before the command runs.
func (d *AskDialog) save(value []byte) tea.Cmd {
	d.clearValue()
	d.phase = askPhaseSaving
	d.errMsg = ""
	api, profile, h := d.api, d.profile, d.header()
	return func() tea.Msg {
		res, err := api.deliver(profile, h, value)
		clear(value)
		msg := askDeliveredMsg{id: h.RequestID, res: res, err: err}
		if errors.Is(err, ask.ErrNotPending) {
			msg.now, _ = api.show(profile, h.RequestID)
		}
		return msg
	}
}

// cancelReq cancels the open request, starts the session's cooldown and
// closes the modal at once.
func (d *AskDialog) cancelReq() tea.Cmd {
	api, profile, id, nonce := d.api, d.profile, d.rec.RequestID, d.nonce
	d.cooldown[d.rec.SessionID] = d.now().Add(askCancelCool)
	d.hide()
	return func() tea.Msg {
		_, err := api.cancel(profile, id, "tui", nonce)
		return askCancelledMsg{id: id, err: err}
	}
}

// Update handles a key while the modal is open. Every key ends here; none is
// logged or passed on.
func (d *AskDialog) Update(msg tea.KeyMsg) tea.Cmd {
	if !d.IsVisible() {
		return nil
	}
	switch d.phase {
	case askPhaseSaving:
		return nil
	case askPhaseStored, askPhaseFinal:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyEnter, tea.KeyCtrlC, tea.KeySpace:
			d.hide()
		}
		return nil
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		return d.cancelReq()
	}
	if d.rec.Kind == ask.KindChoice {
		rows := len(d.rec.Options) + 1
		switch msg.Type {
		case tea.KeyUp, tea.KeyShiftTab:
			d.choice = (d.choice + rows - 1) % rows
		case tea.KeyDown, tea.KeyTab:
			d.choice = (d.choice + 1) % rows
		case tea.KeyEnter:
			if d.choice == len(d.rec.Options) {
				return d.cancelReq()
			}
			if d.nonce == "" {
				return nil
			}
			return d.save([]byte(d.rec.Options[d.choice]))
		}
		return nil
	}
	switch msg.Type {
	case tea.KeyEnter:
		if !d.canSave() {
			return nil
		}
		return d.save([]byte(d.input.Value()))
	case tea.KeyTab, tea.KeyShiftTab, tea.KeyUp, tea.KeyDown:
		return nil // no show toggle, no suggestions
	}
	d.errMsg = ""
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return cmd
}

// buttons renders Save and Cancel at the same width and weight.
func (d *AskDialog) buttons() (save, cancel string) {
	base := lipgloss.NewStyle().Width(12).Align(lipgloss.Center).Bold(true).
		Border(lipgloss.RoundedBorder())
	saveStyle := base.Foreground(ColorTextDim).BorderForeground(ColorTextDim)
	if d.canSave() {
		saveStyle = base.Foreground(ColorGreen).BorderForeground(ColorGreen)
	}
	cancelStyle := base.Foreground(ColorAccent).BorderForeground(ColorAccent)
	return saveStyle.Render("Save"), cancelStyle.Render("Cancel")
}

func askClip(s string, width, lines int) []string {
	if width < 10 {
		width = 10
	}
	wrapped := strings.Split(ansi.Wrap(s, width, ""), "\n")
	if len(wrapped) > lines {
		wrapped = wrapped[:lines]
		last := strings.TrimRight(wrapped[lines-1], " ")
		wrapped[lines-1] = ansi.Truncate(last, width-1, "") + "…"
	}
	return wrapped
}

func askCountdown(left time.Duration) string {
	if left < 0 {
		left = 0
	}
	return left.Truncate(time.Second).String()
}

// View renders the modal centered. It never renders the value: the secret
// field shows only bullets.
func (d *AskDialog) View() string {
	if !d.IsVisible() {
		return ""
	}
	r := d.rec
	dialogWidth := fitDialogWidth(84, 40, d.width)
	inner := dialogWidth - 6
	san := func(s string, n int) string { return ask.SanitizeText(s, n) }

	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	text := lipgloss.NewStyle().Foreground(ColorText)
	label := lipgloss.NewStyle().Foreground(ColorTextDim).Width(10)
	red := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	yellow := lipgloss.NewStyle().Foreground(ColorYellow)
	green := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	border := ColorAccent
	if r.Kind == ask.KindSecret {
		border = ColorYellow
	}

	title := san(r.SessionTitle, 60)
	if title == "" {
		title = "An agent"
	}
	var heading string
	switch r.Kind {
	case ask.KindSecret:
		heading = title + " asks for a secret"
	case ask.KindChoice:
		heading = title + " asks you to choose"
	default:
		heading = title + " asks a question"
	}

	var rows []string
	add := func(s ...string) { rows = append(rows, s...) }
	fact := func(name, value string) string {
		return label.Render(name) + text.Render(ansi.Truncate(value, inner-10, "…"))
	}
	add(lipgloss.NewStyle().Bold(true).Foreground(border).Render(ansi.Truncate(heading, inner, "…")), "")
	add(lipgloss.NewStyle().Bold(true).Foreground(ColorText).Render(fact("Host", san(r.Host, 60))))
	add(fact("Session", san(r.SessionTitle, 60)+"  ·  profile "+san(r.Profile, 40)))
	if r.Kind == ask.KindSecret {
		add(fact("Name", san(r.Name, 140)))
		if r.Replace {
			add(red.Render("REPLACES an existing secret"))
		}
	}
	if !r.CallerVerified {
		add(red.Render("Unverified caller (outside any agent-deck session)"))
	}
	if reason := san(r.Reason, ask.MaxReason); reason != "" {
		add("", dim.Render("The agent says:"))
		for _, l := range askClip(reason, inner-2, askReasonLines) {
			add(text.Render("  " + l))
		}
	}
	if r.Kind != ask.KindSecret {
		add("", dim.Render("The agent asks:"))
		for _, l := range askClip(san(r.Question, ask.MaxQuestion), inner-2, askQuestionLns) {
			add(text.Render("  " + l))
		}
	}
	add("", dim.Render("Expires in "+askCountdown(r.ExpiresAt.Sub(d.now()))))

	switch d.phase {
	case askPhaseStored:
		add("", green.Render(d.notice))
	case askPhaseFinal:
		add("", yellow.Bold(true).Render(d.notice))
		if d.sub != "" {
			add(dim.Render(d.sub))
		}
		add("", dim.Render("Enter / Esc close"))
	default:
		add("")
		switch r.Kind {
		case ask.KindChoice:
			for i, o := range r.Options {
				add(d.choiceRow(san(o, ask.MaxOption), i == d.choice, ColorText, inner))
			}
			add(d.choiceRow("Cancel", d.choice == len(r.Options), ColorAccent, inner))
		default:
			fieldLabel := "Answer"
			if r.Kind == ask.KindSecret {
				fieldLabel = "Secret"
			} else {
				add(yellow.Render(lipgloss.NewStyle().Width(inner).Render("Your answer goes to the agent and its transcript. Never type a secret here.")))
			}
			d.input.Width = inner - 4
			field := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(ColorTextDim).
				Width(inner - 2).Render(d.input.View())
			add(dim.Render(fieldLabel), field)
			save, cancel := d.buttons()
			add(lipgloss.JoinHorizontal(lipgloss.Top, save, "  ", cancel))
		}
		if d.errMsg != "" {
			add(red.Render(ansi.Truncate(d.errMsg, inner, "…")))
		}
		switch {
		case d.phase == askPhaseSaving:
			add(dim.Render("Saving..."))
		case r.Kind == ask.KindChoice:
			add(dim.Render(glueHintGroups("↑/↓ choose · Enter select · Esc cancel")))
		default:
			add(dim.Render(glueHintGroups("Enter save · Esc cancel")))
		}
	}
	if d.queued > 0 {
		add(dim.Render(fmt.Sprintf("%d more waiting", d.queued)))
	}

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Padding(1, 2).Width(dialogWidth).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
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

func (d *AskDialog) choiceRow(label string, focused bool, color lipgloss.Color, inner int) string {
	style := lipgloss.NewStyle().Foreground(color)
	prefix := "  "
	if focused {
		style = style.Bold(true).Foreground(ColorBg).Background(color)
		prefix = "▸ "
	}
	return style.Render(ansi.Truncate(prefix+label, inner, "…"))
}

// canShowAsk reports whether the ask modal may open now. Never while a
// session is attached or embedded (keys would go to the agent's pane, and
// in embedded mode raw stdin bypasses Bubble Tea), never over another modal
// (it would take keys typed for that one) and never mid-typing.
func (h *Home) canShowAsk(now time.Time) bool {
	return h.askDialog != nil && !h.isAttaching.Load() && !h.insertMode && !h.embeddedMode &&
		!h.hasModalVisible() && h.width >= minTerminalWidth && h.height >= minTerminalHeight &&
		now.Sub(h.lastUserInputTime) >= askTypingGuard
}
