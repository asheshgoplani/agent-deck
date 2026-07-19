package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ScrollbackPager is a full-screen, read-only pager over a session's tmux
// scrollback history. It exists because the deck's Enter-attach renders the
// session through tmux control mode, where agent-deck owns the viewport and
// tmux's own copy-mode / mouse-wheel scrollback is unreachable (#1491). The
// user presses the scrollback trigger (PageUp by default) while attached; the
// attach loop hands control back to the TUI, which captures the pane history
// and opens this pager so the start of a long session is reachable without
// leaving agent-deck.
//
// It is deliberately display-only: it never writes state.db or config and holds
// a snapshot, so there is no data-loss surface.
type ScrollbackPager struct {
	visible       bool
	width, height int
	title         string // session display title, shown in the header
	sessionID     string // guards async capture against a stale session
	loading       bool   // capture in flight
	errText       string // non-empty => capture failed, shown in the body
	lines         []string
	offset        int // index of the top visible line within lines
}

// NewScrollbackPager returns a hidden pager.
func NewScrollbackPager() *ScrollbackPager { return &ScrollbackPager{} }

// IsVisible reports whether the pager is open.
func (p *ScrollbackPager) IsVisible() bool { return p != nil && p.visible }

// SessionID returns the session the pager is bound to (for stale-capture guards).
func (p *ScrollbackPager) SessionID() string {
	if p == nil {
		return ""
	}
	return p.sessionID
}

// SetSize records the terminal dimensions and re-clamps the scroll offset so a
// resize can never leave the view scrolled past the end.
func (p *ScrollbackPager) SetSize(width, height int) {
	if p == nil {
		return
	}
	p.width, p.height = width, height
	p.clamp()
}

// Show opens the pager in a loading state bound to the given session. Content
// arrives asynchronously via SetContent / SetError.
func (p *ScrollbackPager) Show(title, sessionID string, width, height int) {
	if p == nil {
		return
	}
	p.visible = true
	p.title = title
	p.sessionID = sessionID
	p.width, p.height = width, height
	p.loading = true
	p.errText = ""
	p.lines = nil
	p.offset = 0
}

// Hide closes the pager and releases the captured content.
func (p *ScrollbackPager) Hide() {
	if p == nil {
		return
	}
	p.visible = false
	p.loading = false
	p.errText = ""
	p.lines = nil
	p.offset = 0
	p.sessionID = ""
}

// SetContent installs captured history and pins the view to the live end (the
// bottom), which is where the user was looking when they opened the pager. A
// trailing empty line (tmux capture-pane often emits one) is dropped so the
// initial view isn't a blank screen.
func (p *ScrollbackPager) SetContent(content string) {
	if p == nil {
		return
	}
	p.loading = false
	p.errText = ""
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], " \t") == "" {
		lines = lines[:len(lines)-1]
	}
	p.lines = lines
	p.Bottom()
}

// SetError records a capture failure to render in the body.
func (p *ScrollbackPager) SetError(msg string) {
	if p == nil {
		return
	}
	p.loading = false
	p.errText = msg
	p.lines = nil
	p.offset = 0
}

// bodyHeight is the number of content rows between the header and footer.
func (p *ScrollbackPager) bodyHeight() int {
	// header (1) + footer (1) are fixed chrome rows.
	h := p.height - 2
	if h < 1 {
		h = 1
	}
	return h
}

// maxOffset is the largest valid top-line index.
func (p *ScrollbackPager) maxOffset() int {
	m := len(p.lines) - p.bodyHeight()
	if m < 0 {
		return 0
	}
	return m
}

// clamp keeps offset within [0, maxOffset].
func (p *ScrollbackPager) clamp() {
	if p.offset < 0 {
		p.offset = 0
	}
	if m := p.maxOffset(); p.offset > m {
		p.offset = m
	}
}

// ScrollUp moves the view toward the start of the session by n lines.
func (p *ScrollbackPager) ScrollUp(n int) {
	if p == nil || n < 1 {
		return
	}
	p.offset -= n
	p.clamp()
}

// ScrollDown moves the view toward the live end by n lines.
func (p *ScrollbackPager) ScrollDown(n int) {
	if p == nil || n < 1 {
		return
	}
	p.offset += n
	p.clamp()
}

// PageUp / PageDown scroll by a near-full body height, keeping one line of
// overlap for reading continuity.
func (p *ScrollbackPager) PageUp()   { p.ScrollUp(p.pageStep()) }
func (p *ScrollbackPager) PageDown() { p.ScrollDown(p.pageStep()) }

func (p *ScrollbackPager) pageStep() int {
	step := p.bodyHeight() - 1
	if step < 1 {
		step = 1
	}
	return step
}

// Top jumps to the start of the session; Bottom to the live end.
func (p *ScrollbackPager) Top() {
	if p == nil {
		return
	}
	p.offset = 0
}

func (p *ScrollbackPager) Bottom() {
	if p == nil {
		return
	}
	p.offset = p.maxOffset()
}

// View renders the pager: a header (title + position), the visible slice of
// history, and a footer of key hints. Each content line is truncated to the
// terminal width (ANSI-aware) and gets a trailing SGR reset so a colour left
// open by capture-pane -e cannot bleed into the next row or the chrome.
func (p *ScrollbackPager) View() string {
	if p == nil || !p.visible {
		return ""
	}
	width := p.width
	if width < 1 {
		width = 1
	}

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("24"))
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	var b strings.Builder

	// Header
	title := p.title
	if title == "" {
		title = "session"
	}
	pos := ""
	switch {
	case p.loading:
		pos = "loading…"
	case p.errText != "":
		pos = "error"
	case len(p.lines) == 0:
		pos = "empty"
	default:
		last := p.offset + p.bodyHeight()
		if last > len(p.lines) {
			last = len(p.lines)
		}
		pos = fmt.Sprintf("lines %d-%d/%d", p.offset+1, last, len(p.lines))
	}
	header := cellTruncate(fmt.Sprintf(" Scrollback · %s ", title), width-lipgloss.Width(pos)-1, "…")
	header = header + strings.Repeat(" ", max0(width-lipgloss.Width(header)-lipgloss.Width(pos)-1)) + pos + " "
	b.WriteString(headerStyle.Width(width).Render(header))
	b.WriteString("\n")

	// Body
	body := p.bodyHeight()
	switch {
	case p.loading:
		b.WriteString(p.renderMessage(dimStyle, "Capturing session history…", body))
	case p.errText != "":
		b.WriteString(p.renderMessage(dimStyle, "Could not capture history: "+p.errText, body))
	case len(p.lines) == 0:
		b.WriteString(p.renderMessage(dimStyle, "No scrollback history.", body))
	default:
		end := p.offset + body
		if end > len(p.lines) {
			end = len(p.lines)
		}
		rows := 0
		for i := p.offset; i < end; i++ {
			line := cellTruncate(p.lines[i], width, "")
			b.WriteString(line)
			b.WriteString("\x1b[0m") // reset SGR so capture colours don't bleed
			b.WriteString("\n")
			rows++
		}
		for rows < body { // pad short buffers so the footer stays pinned
			b.WriteString("\n")
			rows++
		}
	}

	// Footer
	footer := cellTruncate(" ↑/↓ scroll · PgUp/PgDn · g start · G end · Esc back to session · Ctrl+Q list ", width, "…")
	footer = footer + strings.Repeat(" ", max0(width-lipgloss.Width(footer)))
	b.WriteString(footerStyle.Width(width).Render(footer))

	return b.String()
}

// renderMessage centers a one-line message within the body rows.
func (p *ScrollbackPager) renderMessage(style lipgloss.Style, msg string, body int) string {
	var b strings.Builder
	top := body / 2
	for i := 0; i < body; i++ {
		if i == top {
			b.WriteString(style.Render(cellTruncate(msg, p.width, "…")))
		}
		if i < body-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
