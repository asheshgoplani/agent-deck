package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// CodeBlock is a single fenced code block extracted from a session's output.
// Lang is the info-string after the opening fence (may be empty); Content is
// the block body with the fences stripped and no trailing newline.
type CodeBlock struct {
	Lang    string
	Content string
}

// FirstLine returns the first non-empty line of the block, trimmed — used as
// the picker label so the user can tell blocks apart at a glance.
func (b CodeBlock) FirstLine() string {
	for _, ln := range strings.Split(b.Content, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// LineCount returns the number of lines in the block.
func (b CodeBlock) LineCount() int {
	if b.Content == "" {
		return 0
	}
	return strings.Count(b.Content, "\n") + 1
}

// extractCodeBlocks pulls fenced code blocks (``` … ```) out of text, newest
// last (in source order). This is the #1412 "Run this SQL / run this command"
// extractor: agents constantly emit runnable snippets inside fences, and the
// pain point is getting them out of a tmux pane cleanly.
//
// Rules (deliberately lenient, matching how agents format output):
//   - A fence opens on a line whose first non-space run is ``` (3+ backticks).
//   - The optional info-string after the backticks becomes Lang.
//   - The block closes on the next line whose trimmed content is only
//     backticks. An unterminated fence (still streaming) is closed at EOF so
//     the latest, half-finished command is still copyable.
//   - Empty blocks (no content) are skipped.
func extractCodeBlocks(text string) []CodeBlock {
	if text == "" {
		return nil
	}

	var blocks []CodeBlock
	var (
		inBlock bool
		lang    string
		openLen int // backtick count of the opening fence (CommonMark: closer must be >=)
		body    []string
	)

	flush := func() {
		content := strings.TrimRight(strings.Join(body, "\n"), "\n")
		if strings.TrimSpace(content) != "" {
			blocks = append(blocks, CodeBlock{Lang: lang, Content: content})
		}
		inBlock = false
		lang = ""
		openLen = 0
		body = nil
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		ticks, info, isFence := parseFence(trimmed)

		if !inBlock {
			// Only an OPENING fence may carry an info string (the language).
			if isFence {
				inBlock = true
				lang = info
				openLen = ticks
				body = nil
			}
			continue
		}

		// Inside a block: a CLOSER is a bare fence (no info string) of at least
		// the opening length (CommonMark). Anything else — including a longer
		// or info-bearing fence — is block content, so nested ``` fences and
		// "```foo" lines do not prematurely close an outer block.
		if isFence && info == "" && ticks >= openLen {
			flush()
			continue
		}
		body = append(body, line)
	}
	// Unterminated fence at EOF: keep the partial block so a streaming command
	// is still copyable.
	if inBlock {
		flush()
	}

	return blocks
}

// parseFence inspects a trimmed line and reports whether it is a code-fence
// delimiter. A fence is a run of >=3 backticks optionally followed by an
// info string. It returns the backtick count, the trimmed info string, and
// whether the line is a fence at all. An inline-code line like `foo` (a single
// backtick run that is not >=3) or a line whose info string contains a backtick
// (e.g. ``` `inline` ```) is NOT a fence.
func parseFence(trimmed string) (ticks int, info string, ok bool) {
	n := 0
	for n < len(trimmed) && trimmed[n] == '`' {
		n++
	}
	if n < 3 {
		return 0, "", false
	}
	rest := strings.TrimSpace(trimmed[n:])
	// An info string containing a backtick means this is not a clean fence
	// (CommonMark forbids backticks in an info string).
	if strings.Contains(rest, "`") {
		return 0, "", false
	}
	return n, rest, true
}

// CodeBlockDialog presents the fenced code blocks extracted from a session's
// recent output so the user can pick one and copy it (OSC52, SSH-safe). It is
// the #1412 copy/paste extractor. Mirrors SessionPickerDialog's shape.
type CodeBlockDialog struct {
	visible       bool
	width, height int
	blocks        []CodeBlock
	cursor        int
	sessionTitle  string
}

// NewCodeBlockDialog creates an empty code-block picker dialog.
func NewCodeBlockDialog() *CodeBlockDialog {
	return &CodeBlockDialog{}
}

// Show opens the picker with the given blocks for sessionTitle. Returns false
// (and does not open) when there are no blocks to pick.
func (d *CodeBlockDialog) Show(sessionTitle string, blocks []CodeBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	d.visible = true
	d.sessionTitle = sessionTitle
	d.blocks = blocks
	d.cursor = 0
	return true
}

// Hide closes the dialog and clears its state.
func (d *CodeBlockDialog) Hide() {
	d.visible = false
	d.cursor = 0
	d.blocks = nil
	d.sessionTitle = ""
}

// IsVisible reports whether the dialog is shown.
func (d *CodeBlockDialog) IsVisible() bool { return d.visible }

// SetSize updates the dialog dimensions for centering.
func (d *CodeBlockDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// GetSelected returns the block at the cursor, or nil when none.
func (d *CodeBlockDialog) GetSelected() *CodeBlock {
	if d.cursor < 0 || d.cursor >= len(d.blocks) {
		return nil
	}
	return &d.blocks[d.cursor]
}

// Update handles navigation keys (j/k/up/down). enter/esc are handled by the
// parent so it can act on the selection (mirrors SessionPickerDialog).
func (d *CodeBlockDialog) Update(msg tea.KeyMsg) (*CodeBlockDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	switch msg.String() {
	case "j", "down":
		if len(d.blocks) > 0 {
			d.cursor = (d.cursor + 1) % len(d.blocks)
		}
	case "k", "up":
		if len(d.blocks) > 0 {
			d.cursor = (d.cursor - 1 + len(d.blocks)) % len(d.blocks)
		}
	case "esc":
		d.Hide()
	}
	return d, nil
}

// View renders the code-block picker.
func (d *CodeBlockDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	// No MarginBottom here: the explicit blank line below provides the single
	// header gap, so chrome row counting in visibleRows stays deterministic.
	sourceStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	selectedStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)
	footerStyle := lipgloss.NewStyle().Foreground(ColorComment).Italic(true)

	dialogWidth := 60
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 36 {
			dialogWidth = 36
		}
	}
	// On a genuinely tiny terminal, never let the box (content + RoundedBorder's
	// 2 cols) exceed the screen — that would wrap the whole dialog and break the
	// height accounting. Clamp to the available width as a last resort, all the
	// way down to a minimal box, so the box+border invariant holds even on a
	// pathologically narrow terminal (#1412, Codex review).
	if d.width > 0 && dialogWidth > d.width-2 {
		dialogWidth = d.width - 2
	}
	if dialogWidth < 1 {
		dialogWidth = 1
	}

	// innerWidth is the content width INSIDE DialogBoxStyle's Padding(1,2):
	// dialogWidth minus 2 columns of padding on each side. EVERY rendered row is
	// hard-truncated to innerWidth (cell-width-aware) so nothing wraps onto a
	// second terminal line, which would break the one-row-per-block height
	// accounting in visibleRows (#1412, Codex review). cellTruncate is keycap /
	// CJK / emoji aware, so multibyte content can't overflow the budget either.
	// The floor is derived from dialogWidth (never a fixed 12) so it can never
	// exceed the box interior on a tiny terminal.
	innerWidth := dialogWidth - 4
	if innerWidth < 1 {
		innerWidth = 1
	}
	// fit truncates a (possibly styled) line to exactly innerWidth cells.
	fit := func(s string) string { return cellTruncate(s, innerWidth, "…") }

	var lines []string
	lines = append(lines, fit(titleStyle.Render("Copy Code Block")))
	if d.sessionTitle != "" {
		lines = append(lines, fit(sourceStyle.Render(fmt.Sprintf("Session: %q", d.sessionTitle))))
	}
	lines = append(lines, "")

	// Windowed rendering: a session with many code blocks would otherwise
	// produce a dialog taller than the terminal, pushing the selection and
	// footer off-screen. Show at most maxVisible rows scrolled around the
	// cursor, with ↑/↓ overflow markers (#1412, Codex review).
	maxVisible := d.visibleRows()
	start, end := windowBounds(d.cursor, len(d.blocks), maxVisible)

	if start > 0 {
		lines = append(lines, fit(dimStyle.Render(fmt.Sprintf("  ↑ %d more", start))))
	}
	for i := start; i < end; i++ {
		b := d.blocks[i]
		lang := b.Lang
		if lang == "" {
			lang = "text"
		}
		meta := dimStyle.Render(fmt.Sprintf("[%s, %d line(s)]", lang, b.LineCount()))
		label := fmt.Sprintf("%s  %s", b.FirstLine(), meta)
		if i == d.cursor {
			lines = append(lines, fit("> "+selectedStyle.Render(label)))
		} else {
			lines = append(lines, fit("  "+normalStyle.Render(label)))
		}
	}
	if end < len(d.blocks) {
		lines = append(lines, fit(dimStyle.Render(fmt.Sprintf("  ↓ %d more", len(d.blocks)-end))))
	}

	lines = append(lines, "")
	// Footer: compact form when the full hint would not fit, then a final
	// cell-width truncation as a backstop so it is always exactly one row.
	footer := "Enter copy | Esc cancel | j/k navigate"
	if cellWidth(footer) > innerWidth {
		footer = "Enter copy | Esc | j/k"
	}
	lines = append(lines, fit(footerStyle.Render(footer)))

	box := DialogBoxStyle.Width(dialogWidth).Render(strings.Join(lines, "\n"))
	return centerInScreen(box, d.width, d.height)
}

// codeBlockDialogChrome is the number of non-block rows the rendered dialog
// occupies, so the windowed block list never overflows the terminal:
//
//	DialogBoxStyle border top+bottom ......... 2
//	DialogBoxStyle padding top+bottom (1,2) .. 2
//	title line ............................... 1
//	session line ............................. 1
//	blank after header ....................... 1
//	"↑ N more" overflow marker (worst case) .. 1
//	"↓ N more" overflow marker (worst case) .. 1
//	blank before footer ...................... 1
//	footer line .............................. 1
//
// Total = 11. Kept slightly conservative on purpose; underestimating chrome is
// what lets the picker spill off-screen (#1412, Codex review).
const codeBlockDialogChrome = 11

// visibleRows returns how many block rows fit given the screen height. It
// subtracts the full rendered chrome (border + padding + header/footer +
// overflow markers) so the dialog stays within the terminal even when many
// blocks exist. Falls back to a small default when height is unknown, and never
// returns fewer than one row (on a terminal too short to fit even one block
// the dialog cannot help being clipped, but it must still render a row).
func (d *CodeBlockDialog) visibleRows() int {
	const def = 12
	if d.height <= 0 {
		return def
	}
	rows := d.height - codeBlockDialogChrome
	if rows < 1 {
		return 1
	}
	if rows > def {
		return def
	}
	return rows
}

// windowBounds returns the [start, end) slice of indices to render so that
// cursor stays visible within a window of at most size rows.
func windowBounds(cursor, total, size int) (int, int) {
	if total <= size {
		return 0, total
	}
	start := cursor - size/2
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > total {
		end = total
		start = end - size
	}
	return start, end
}

// copyCodeBlock returns a tea.Cmd that copies the given code block's content to
// the clipboard, reusing the same OSC52-aware fallback chain as the other copy
// actions so it works over SSH.
func (h *Home) copyCodeBlock(block CodeBlock, sessionTitle string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(block.Content) == "" {
			return copyResultMsg{err: fmt.Errorf("empty code block")}
		}
		termInfo := tmux.GetTerminalInfo()
		result, err := clipboard.Copy(block.Content, termInfo.SupportsOSC52)
		if err != nil {
			return copyResultMsg{err: fmt.Errorf("clipboard: %w", err)}
		}
		return copyResultMsg{
			sessionTitle: sessionTitle,
			lineCount:    result.LineCount,
		}
	}
}

// openCodeBlockPicker reads the selected session's recent output, extracts
// fenced code blocks, and either copies the single block directly or opens the
// picker for the user to choose. It is the entry point for the #1412 `Y`
// hotkey. sessionContent is injected so the core is testable without tmux.
func (h *Home) startCodeBlockCopy(inst *session.Instance) tea.Cmd {
	content, err := getSessionContent(inst)
	if err != nil {
		h.setError(fmt.Errorf("no output to extract code from: %w", err))
		return nil
	}
	blocks := extractCodeBlocks(content)
	switch len(blocks) {
	case 0:
		h.setError(fmt.Errorf("no code blocks found in this session's output"))
		return nil
	case 1:
		return h.copyCodeBlock(blocks[0], inst.Title)
	default:
		h.codeBlockDialog.SetSize(h.width, h.height)
		h.codeBlockDialog.Show(inst.Title, blocks)
		return nil
	}
}

// handleCodeBlockDialogKey handles key events while the code-block picker is
// visible. Mirrors handleSessionPickerDialogKey.
func (h *Home) handleCodeBlockDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.codeBlockDialog.GetSelected()
		title := h.codeBlockDialog.sessionTitle
		h.codeBlockDialog.Hide()
		if selected != nil {
			return h, h.copyCodeBlock(*selected, title)
		}
		return h, nil
	case "esc":
		h.codeBlockDialog.Hide()
		return h, nil
	default:
		h.codeBlockDialog.Update(msg)
		return h, nil
	}
}
