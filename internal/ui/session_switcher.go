package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// switcherIdleCommit is how long the switcher waits after the last Ctrl+S /
// Ctrl+A before auto-committing to the highlighted session. It approximates
// "switch when I let go of the key" — terminals do not deliver key-release
// events, so we commit on a brief idle instead. Enter commits immediately; Esc
// cancels; arrow-key navigation cancels the auto-commit (manual mode).
const switcherIdleCommit = 1 * time.Second

// switcherRepeatGuard is the minimum gap between accepted Ctrl+S / Ctrl+A
// advances. Terminal auto-repeat fires far faster than this (~15–40ms), so
// holding the key down advances at most a step or two instead of spinning
// through every session; deliberate taps (~100ms+ apart) all register.
const switcherRepeatGuard = 80 * time.Millisecond

// SessionSwitcher is the session switcher overlay. It opens on Ctrl+S — both
// while attached (the tmux attach loop hands control back to the TUI) and from
// the overview — pre-highlighted on the session you came from. Ctrl+S / Ctrl+A
// cycle forward / backward, arrow keys browse, and the highlight is attached on
// Enter or after a brief idle once you've cycled.
type SessionSwitcher struct {
	visible          bool
	width, height    int
	sessions         []*session.Instance // active sessions, MRU-ordered
	cursor           int
	fromID           string            // session the picker was opened from
	subtitles        map[string]string // sessionID -> dim conversation/pane title (matches the overview)
	reattachOnCancel bool              // Esc re-attaches to fromID (opened while attached) vs. just closing (opened from the overview)
	// commitGen is bumped on every open/cycle/cancel so a stale idle-commit
	// timer (scheduled before a later keypress) is ignored when it fires. It is
	// intentionally monotonic — never reset — so a timer from a previous
	// switcher session can never collide with a new one.
	commitGen int
	// lastCycleAt is the time of the last accepted Ctrl+S / Ctrl+A advance, used
	// to swallow terminal key-repeat (see switcherRepeatGuard).
	lastCycleAt time.Time
}

// bumpCommitGen advances and returns the commit generation. armSwitcherCommit
// schedules a timer tagged with the returned value; calling it WITHOUT
// scheduling a new timer (e.g. on arrow navigation) simply invalidates any
// pending auto-commit. Only a timer carrying the current generation commits
// (see Home.handleSwitcherCommit).
func (s *SessionSwitcher) bumpCommitGen() int {
	s.commitGen++
	return s.commitGen
}

// cycle advances the highlight one step (forward => next, else prev) unless the
// previous accepted advance was within switcherRepeatGuard, which swallows
// key-repeat from a held Ctrl+S / Ctrl+A. It reports whether it moved.
func (s *SessionSwitcher) cycle(forward bool, now time.Time) bool {
	if !s.lastCycleAt.IsZero() && now.Sub(s.lastCycleAt) < switcherRepeatGuard {
		return false
	}
	s.lastCycleAt = now
	if forward {
		s.next()
	} else {
		s.prev()
	}
	return true
}

// NewSessionSwitcher creates a new (hidden) session switcher.
func NewSessionSwitcher() *SessionSwitcher { return &SessionSwitcher{} }

// Show builds the MRU-ordered list of switchable sessions and pre-selects the
// session the picker was opened from (fromID), so an immediate Enter drops the
// user right back where they were and Ctrl+S/Ctrl+A step away from there.
// subtitles maps a session ID to its dim conversation/pane title (the same text
// the overview shows next to an entry); a nil map renders no subtitles. It
// returns false (and stays hidden) when fewer than two sessions are available —
// there is nothing to switch between, so the caller falls back to a normal detach.
//
// Scope: the switcher is local-only by design — it takes local
// *session.Instance rows and re-attaches via the local tmux attach loop. Remote
// (SSH) sessions reach a session over a different attach path, so they are
// intentionally excluded from the picker for now (see
// TestSessionSwitcher_RemoteSessionsUnsupported); supporting them needs a remote
// re-attach path and is tracked as a follow-up.
func (s *SessionSwitcher) Show(fromID string, allInstances []*session.Instance, subtitles map[string]string) bool {
	list := make([]*session.Instance, 0, len(allInstances))
	for _, inst := range allInstances {
		if inst == nil {
			continue
		}
		// Mirror the send-output picker: only switchable (live) sessions.
		switch inst.GetStatusThreadSafe() {
		case session.StatusError, session.StatusStopped:
			continue
		}
		list = append(list, inst)
	}
	if len(list) < 2 {
		// Nothing to switch between. Clear any prior selection so a switcher that
		// was already open (e.g. live-session count just dropped below two) does
		// not linger on stale state — Show's contract is "stays hidden" here.
		s.Hide()
		return false
	}

	// Most-recently-accessed first. The just-detached session was
	// MarkAccessed'd on detach, so it sorts to the front — pre-selecting it
	// means the first Ctrl+S step lands on the most-recent other session.
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].LastAccessedAt.After(list[j].LastAccessedAt)
	})

	cursor := 0
	for i, inst := range list {
		if inst.ID == fromID {
			cursor = i
			break
		}
	}

	s.visible = true
	s.sessions = list
	s.cursor = cursor
	s.fromID = fromID
	s.subtitles = subtitles
	return true
}

// Hide closes the switcher and resets state. commitGen is intentionally left
// untouched (monotonic) so a pending timer from this session can't commit after
// a future re-open.
func (s *SessionSwitcher) Hide() {
	s.visible = false
	s.cursor = 0
	s.sessions = nil
	s.fromID = ""
	s.subtitles = nil
	s.reattachOnCancel = false
	s.lastCycleAt = time.Time{}
}

// IsVisible reports whether the switcher is currently shown.
func (s *SessionSwitcher) IsVisible() bool { return s != nil && s.visible }

// SetSize updates the dimensions used for centering.
func (s *SessionSwitcher) SetSize(w, h int) {
	s.width = w
	s.height = h
}

// GetSelected returns the highlighted session, or nil.
func (s *SessionSwitcher) GetSelected() *session.Instance {
	if len(s.sessions) == 0 || s.cursor < 0 || s.cursor >= len(s.sessions) {
		return nil
	}
	return s.sessions[s.cursor]
}

func (s *SessionSwitcher) next() {
	if len(s.sessions) > 0 {
		s.cursor = (s.cursor + 1) % len(s.sessions)
	}
}

func (s *SessionSwitcher) prev() {
	if len(s.sessions) > 0 {
		s.cursor = (s.cursor - 1 + len(s.sessions)) % len(s.sessions)
	}
}

// View renders the centered switcher box.
func (s *SessionSwitcher) View() string {
	if !s.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)
	selectedStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	footerStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)

	header := "Switch session"
	// The forward/back cycle keys are fixed (the attach loop and
	// handleSessionSwitcherKey both match Ctrl+S / Ctrl+A regardless of the
	// configurable open binding), so the labels stay literal. Esc, however,
	// re-attaches to the origin only when the picker was opened while attached;
	// from the overview it just closes, so the hint reflects that. Built up front
	// so the footer width feeds the natural-width measurement below.
	escHint := "Esc close"
	if s.reattachOnCancel {
		escHint = "Esc back"
	}
	footerCycle := "Ctrl+S next · Ctrl+A prev"
	footerNav := "↑/↓ browse · Enter attach · " + escHint

	// Precompute each row once so we can measure the widest row (to auto-expand
	// the dialog) and render without recomputing. label/subtitle route through
	// the same helper the overview uses (sessionDisplayLabels) so the two render
	// paths stay consistent: an auto-named session shows Claude's live/persisted
	// task description as the title (and no subtitle), not its random handle.
	type switcherRow struct {
		marker     string
		labelStyle lipgloss.Style
		indicator  string
		title      string // label + tool (plain text)
		subtitle   string
		prefix     int // cells before the title: marker + indicator + space
	}
	rows := make([]switcherRow, len(s.sessions))

	// natural is the widest line's content width (excluding the box border +
	// horizontal padding). The dialog grows to fit it, capped at the terminal.
	natural := max(cellWidth(header), cellWidth(footerCycle), cellWidth(footerNav))
	for i, inst := range s.sessions {
		indicator := statusIndicator(inst.GetStatusThreadSafe())
		tool := ""
		if inst.Tool != "" {
			tool = fmt.Sprintf(" (%s)", inst.Tool)
		}
		label, subtitle := sessionDisplayLabels(inst, s.subtitles[inst.ID])
		title := label + tool

		marker := "  "
		labelStyle := normalStyle
		if i == s.cursor {
			marker = "> "
			labelStyle = selectedStyle
		}
		prefix := cellWidth(marker) + cellWidth(indicator) + 1 // marker + indicator + space
		rows[i] = switcherRow{marker: marker, labelStyle: labelStyle, indicator: indicator, title: title, subtitle: subtitle, prefix: prefix}

		rowWidth := prefix + cellWidth(title)
		if subtitle != "" {
			rowWidth += 1 + cellWidth(subtitle) // space + subtitle
		}
		natural = max(natural, rowWidth)
	}

	// Grow the dialog to fit the widest row, but never below the comfortable
	// default and never past the terminal width (leaving a small margin so the
	// bordered box doesn't touch the screen edges). +4 covers the Padding(1,2).
	const minDialogWidth = 56
	dialogWidth := max(minDialogWidth, natural+4)
	if s.width > 0 {
		// Clamp to the terminal: s.width-4 keeps the bordered box one cell off
		// each edge. The floor matches contentWidth's (10) rather than the
		// comfortable default, so a very narrow terminal still wins the clamp
		// instead of overflowing.
		dialogWidth = min(dialogWidth, max(10, s.width-4))
	}
	// Content area inside the rounded border + Padding(1,2): horizontal padding
	// eats 4 cells. Truncating rows to this keeps long titles/subtitles from
	// wrapping, which would break the centered box layout.
	contentWidth := max(10, dialogWidth-4)

	var lines []string
	lines = append(lines, titleStyle.Render(header))
	lines = append(lines, "")

	for _, r := range rows {
		title := r.title
		// Truncate the title only if it alone would overflow the row — i.e. when
		// the dialog hit the terminal-width cap. Below the cap the box grew to fit.
		if budget := contentWidth - r.prefix; budget > 0 && cellWidth(title) > budget {
			title = cellTruncate(title, budget, "…")
		}
		line := r.marker + r.indicator + " " + r.labelStyle.Render(title)

		// Append the dim conversation/pane title (same text the overview shows
		// next to an entry), truncated to the space left on the row.
		if r.subtitle != "" {
			used := r.prefix + cellWidth(title) // title may have been truncated above
			if remaining := contentWidth - used - 1; remaining >= 6 {
				line += " " + DimStyle.Render(cellTruncate(r.subtitle, remaining, "…"))
			}
		}
		lines = append(lines, line)
	}

	lines = append(lines, "")
	lines = append(lines, footerStyle.Render(footerCycle))
	lines = append(lines, footerStyle.Render(footerNav))

	content := strings.Join(lines, "\n")

	box := DialogBoxStyle.
		Width(dialogWidth).
		Render(content)

	return centerInScreen(box, s.width, s.height)
}
