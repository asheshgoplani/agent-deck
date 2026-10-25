package ui

// Session reference pick/drop (ASK-111).
//
// alt+l on a session picks it as the reference source A. The list keeps
// working for navigation while a hint line above the help bar shows
// "A -> <cursor session>". Enter (or alt+l again) on another session B drops:
// sessionref.Hand, the same function `agent-deck session ref` calls, types a
// short pointer to A into B's composer. It never presses Enter in B. Esc
// cancels. Nothing moves: group rows are not targets and the move-to-group
// flow (M) is untouched.

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
)

// hotkeySessionRef is the pick/drop action. Every plain letter is taken and
// "Z" is report_problem in the parallel feedback lane, so it sits on a free
// alt chord like alt+a, alt+d and alt+t ("l" for link). Rebindable through
// [hotkeys].session_ref.
const hotkeySessionRef = "session_ref"

// sessionRefState is the live pick plus the last toast it raised.
type sessionRefState struct {
	// srcID is A's instance id while picking; "" when not picking.
	srcID    string
	srcTitle string
	// toast is the result or refusal shown over the help bar's key row. It
	// is also h.err, so the normal 5s auto-dismiss clears it.
	toast   error
	toastOK bool
	// hand runs the drop. nil uses sessionref.Hand. Tests inject a fake;
	// it is called inside a tea.Cmd, never on the update loop.
	hand func(src, dst sessionref.Endpoint, opts sessionref.Options) (*sessionref.Result, error)
	// insert, when set, replaces the tmux insert in Options (tests).
	insert func(*session.Instance, string) error
	// capture, when set, replaces the pane read of the core's menu guard
	// in Options (tests). nil keeps the tmux read.
	capture func(*session.Instance) (string, error)
}

// sessionRefResultMsg carries a finished drop back to the update loop.
type sessionRefResultMsg struct {
	srcID    string
	srcTitle string
	dstTitle string
	res      *sessionref.Result
	err      error
}

// sessionRefPassKeys are the normalized keys that keep their normal meaning
// while picking: movement, folding and the title search. Everything else is
// ignored so a live pick cannot delete, move, rename or attach by accident.
var sessionRefPassKeys = map[string]bool{
	"up": true, "k": true, "ctrl+p": true,
	"down": true, "j": true, "ctrl+n": true,
	"ctrl+u": true, "pgup": true, "ctrl+d": true, "pgdown": true,
	"ctrl+b": true, "ctrl+f": true, "home": true, "end": true,
	"alt+j": true, "alt+k": true, "alt+g": true, "alt+G": true,
	"alt+1": true, "alt+2": true, "alt+3": true, "alt+4": true, "alt+5": true,
	"alt+6": true, "alt+7": true, "alt+8": true, "alt+9": true,
	"tab": true, "l": true, "right": true, "h": true, "left": true,
	"/": true, "ctrl+c": true,
}

// sessionRefPicking reports whether a pick is live.
func (h *Home) sessionRefPicking() bool { return h.sessionRef.srcID != "" }

func (h *Home) sessionRefToast(ok bool, format string, args ...any) {
	err := fmt.Errorf(format, args...)
	h.setError(err)
	h.sessionRef.toast = err
	h.sessionRef.toastOK = ok
}

func (h *Home) cancelSessionRefPick() {
	h.sessionRef.srcID = ""
	h.sessionRef.srcTitle = ""
}

// startSessionRefPick handles the pick key outside picking mode.
func (h *Home) startSessionRefPick() {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	switch {
	case item.IsCreatingPlaceholder():
		h.setError(errSessionStillCreating)
		return
	case item.Type == session.ItemTypeRemoteSession:
		h.sessionRefToast(false, "remote sessions can't give a reference yet")
		return
	case item.Type != session.ItemTypeSession && item.Type != session.ItemTypeWindow:
		h.sessionRefToast(false, "pick a session to reference, not a group")
		return
	}
	src := h.getSelectedSession()
	if src == nil {
		return
	}
	if strings.TrimSpace(src.SSHHost) != "" {
		h.sessionRefToast(false, "%s runs on %s: remote sessions can't give a reference yet", src.Title, src.SSHHost)
		return
	}
	h.sessionRef.srcID = src.ID
	h.sessionRef.srcTitle = src.Title
}

// handleSessionRefPickKey runs before the main switch while picking.
// handled=false means the key keeps its normal meaning (sessionRefPassKeys).
func (h *Home) handleSessionRefPickKey(key string) (handled bool, cmd tea.Cmd) {
	switch key {
	case "esc":
		h.cancelSessionRefPick()
		return true, nil
	case "enter", defaultHotkeyBindings[hotkeySessionRef]:
		return true, h.dropSessionRef()
	}
	if sessionRefPassKeys[key] {
		// A refusal belongs to the row it was about: moving on clears it so
		// it never sits under the hint for a different row.
		if h.sessionRefToastActive() && !h.sessionRef.toastOK {
			h.clearError()
		}
		return false, nil
	}
	return true, nil
}

// sessionRefTarget resolves the row under the cursor as a drop target. It
// does no tmux work (it runs on every frame): dst is nil with why set when
// the row is refused in place, and both are zero when there is no row.
func (h *Home) sessionRefTarget() (src, dst *session.Instance, why string) {
	src = h.getInstanceByID(h.sessionRef.srcID)
	if src == nil {
		return nil, nil, fmt.Sprintf("%s is gone: Esc and pick again", h.sessionRef.srcTitle)
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return src, nil, ""
	}
	item := h.flatItems[h.cursor]
	switch item.Type {
	case session.ItemTypeGroup:
		name := item.Path
		if item.Group != nil && item.Group.Name != "" {
			name = item.Group.Name
		}
		return src, nil, fmt.Sprintf("%s is a group: drop onto a session, not a group (no move)", name)
	case session.ItemTypeRemoteGroup:
		return src, nil, "drop onto a session, not a group (no move)"
	case session.ItemTypeRemoteSession:
		return src, nil, "remote sessions can't take a reference yet"
	case session.ItemTypeSession, session.ItemTypeWindow:
		if item.IsCreatingPlaceholder() {
			return src, nil, "that session is still being created"
		}
	default:
		return src, nil, ""
	}
	dst = h.getSelectedSession()
	if dst == nil {
		return src, nil, ""
	}
	if r := sessionref.Check(
		sessionref.Endpoint{Inst: src, Profile: h.profile},
		sessionref.Endpoint{Inst: dst, Profile: h.profile},
		false, nil); r != nil {
		return src, nil, sessionRefRefusalText(r, dst)
	}
	if st := dst.GetStatusThreadSafe(); st == session.StatusStopped || st == session.StatusError {
		return src, nil, fmt.Sprintf("%s is not running: start it first (%s)", dst.Title, h.actionKey(hotkeyRestart))
	}
	return src, dst, ""
}

// sessionRefRefusalText is the short TUI wording for a core refusal.
func sessionRefRefusalText(r *sessionref.Refusal, dst *session.Instance) string {
	title := ""
	if dst != nil {
		title = dst.Title
	}
	switch r.Code {
	case sessionref.CodeSameSession:
		return "that is the same session: pick a different one"
	case sessionref.CodeSourceRemote, sessionref.CodeTargetRemote:
		return "remote sessions can't take a reference yet"
	case sessionref.CodeTargetNotRunning:
		return fmt.Sprintf("%s is not running: start it first", title)
	case sessionref.CodeTargetNoPane:
		return fmt.Sprintf("%s has no terminal to type into", title)
	case sessionref.CodeTargetMenuOpen:
		return fmt.Sprintf("%s is showing a menu/prompt; answer it first (nothing typed)", title)
	case sessionref.CodeTargetBusyVim:
		return fmt.Sprintf("%s is working; drop again when it is waiting (nothing typed)", title)
	}
	return r.Message
}

// dropSessionRef handles Enter / the pick key while picking. A refused row
// keeps the pick live; an accepted one ends picking and hands the reference
// in a tea.Cmd (the insert talks to tmux).
func (h *Home) dropSessionRef() tea.Cmd {
	src, dst, why := h.sessionRefTarget()
	if src == nil {
		h.cancelSessionRefPick()
		h.sessionRefToast(false, "%s", why)
		return nil
	}
	if dst == nil {
		if why == "" {
			why = "move to the session that should get the reference"
		}
		h.sessionRefToast(false, "%s", why)
		return nil
	}
	srcID, srcTitle := h.sessionRef.srcID, h.sessionRef.srcTitle
	h.cancelSessionRefPick()

	hand := h.sessionRef.hand
	if hand == nil {
		hand = sessionref.Hand
	}
	opts := sessionref.Options{Insert: h.sessionRef.insert, Capture: h.sessionRef.capture, Running: h.sessionExists}
	srcEP := sessionref.Endpoint{Inst: src, Profile: h.profile}
	dstEP := sessionref.Endpoint{Inst: dst, Profile: h.profile}
	dstTitle := dst.Title
	return func() tea.Msg {
		res, err := hand(srcEP, dstEP, opts)
		return sessionRefResultMsg{srcID: srcID, srcTitle: srcTitle, dstTitle: dstTitle, res: res, err: err}
	}
}

// handleSessionRefResult shows the outcome. A refusal puts the pick back so
// the user can move to another target without picking A again.
func (h *Home) handleSessionRefResult(msg sessionRefResultMsg) {
	if msg.err == nil && msg.res != nil && msg.res.Inserted {
		h.sessionRefToast(true, "reference to %s typed into %s (not sent): Enter there sends it", msg.srcTitle, msg.dstTitle)
		return
	}
	text := "could not hand the reference"
	var r *sessionref.Refusal
	if errors.As(msg.err, &r) {
		text = sessionRefRefusalText(r, &session.Instance{Title: msg.dstTitle})
	} else if msg.err != nil {
		text = msg.err.Error()
	}
	h.sessionRefToast(false, "%s", text)
	if !h.sessionRefPicking() && h.getInstanceByID(msg.srcID) != nil {
		h.sessionRef.srcID = msg.srcID
		h.sessionRef.srcTitle = msg.srcTitle
	}
}

// sessionRefHintText is the plain hint line: "◆ A ───▶ B   ⏎ drop ..." on a
// valid target, "◆ A -> ✗ <why>" on a refused row, "◆ A -> ?" otherwise.
func (h *Home) sessionRefHintText() string {
	src, dst, why := h.sessionRefTarget()
	a := h.sessionRef.srcTitle
	if src != nil {
		a = src.Title
	}
	switch {
	case dst != nil:
		return fmt.Sprintf("◆ %s ───▶ %s   ⏎ drop · esc cancel · nothing is sent", a, dst.Title)
	case why != "":
		return fmt.Sprintf("◆ %s -> ✗ %s", a, why)
	default:
		return fmt.Sprintf("◆ %s -> ?   (move to the target; Enter/%s drops, Esc cancels)", a, h.sessionRefKeyLabel())
	}
}

func (h *Home) sessionRefKeyLabel() string {
	if k := h.actionKey(hotkeySessionRef); k != "" {
		return k
	}
	return "Enter"
}

// sessionRefKeysText is the picking help row (plain).
func (h *Home) sessionRefKeysText() string {
	drop := "⏎"
	if k := h.actionKey(hotkeySessionRef); k != "" {
		drop += "/" + k
	}
	return fmt.Sprintf(" %s Drop on this session │ esc Cancel │ ↑↓ j/k / Move to the target", drop)
}

// renderSessionRefPickBar replaces the help bar while picking. It keeps the
// bar's two lines (frame height math assumes 2): the hint line takes the
// separator's place and the picking keys take the key row.
func (h *Home) renderSessionRefPickBar() string {
	_, dst, why := h.sessionRefTarget()
	hintStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	switch {
	case dst != nil:
		hintStyle = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	case why != "":
		hintStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	}
	hint := " " + h.sessionRefHintText()
	keys := h.sessionRefKeysText()
	if h.width > 0 {
		hint = ansi.Truncate(hint, h.width, "…")
		keys = ansi.Truncate(keys, h.width, "…")
	}
	return hintStyle.Render(hint) + "\n" + lipgloss.NewStyle().Foreground(ColorText).Render(keys)
}

// sessionRefToastActive reports whether h.err is this feature's toast.
func (h *Home) sessionRefToastActive() bool {
	return h.err != nil && h.sessionRef.toast != nil && h.err == h.sessionRef.toast
}

// renderSessionRefToastOverHelpBar draws the toast in the help bar's key row
// (a line below the help bar is clipped by the viewport), keeping line one.
func (h *Home) renderSessionRefToastOverHelpBar(helpBar string) string {
	var line string
	if h.sessionRef.toastOK {
		line = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render(" ✓ " + h.err.Error())
	} else {
		line = ErrorStyle.Render(" ✗ " + h.err.Error())
	}
	if h.width > 0 && cellWidth(line) > h.width {
		line = ansi.Truncate(line, h.width, "…")
	}
	if first, _, ok := strings.Cut(helpBar, "\n"); ok {
		return first + "\n" + line
	}
	return line
}

// overlaySessionRefMark paints ◆ in the left gutter of A's row while picking.
func overlaySessionRefMark(line string) string {
	if cellWidth(line) < leftGutterWidth {
		return line
	}
	mark := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render("◆")
	return replaceVisibleRange(line, 0, leftGutterWidth, mark+strings.Repeat(" ", leftGutterWidth-1))
}
