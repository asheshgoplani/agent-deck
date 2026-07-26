package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sgrActiveAt walks s as a stream of CSI m (SGR) sequences and reports
// whether SGR state is non-default at the end of s. A line that ends with
// SGR still active will bleed its highlight/color into whatever the terminal
// renders next — including the adjacent pane when lipgloss.JoinHorizontal
// concatenates rows.
//
// "Reset" tokens in an SGR parameter list are an empty string or "0".
// Any other numeric parameter (e.g. "43" for yellow bg) activates state.
// A mixed list like "0;43" ends active; "43;0" ends reset — this matches
// the ECMA-48 left-to-right application of SGR parameters.
func sgrActiveAt(s string) bool {
	active := false
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
				j++
			}
			if j >= len(s) {
				return active
			}
			if s[j] == 'm' {
				params := s[i+2 : j]
				if params == "" {
					active = false
				} else {
					for _, p := range strings.Split(params, ";") {
						if p == "" || p == "0" {
							active = false
						} else {
							active = true
						}
					}
				}
			}
			i = j + 1
			continue
		}
		i++
	}
	return active
}

// Issue #699 — @javierciccarelli
// Right-pane preview renders a Claude session's captured output. When that
// output contains an unclosed SGR (e.g. a background highlight whose closing
// reset is off-screen or was clipped by width truncation), the right pane's
// line ends with SGR state still active. lipgloss.JoinHorizontal then feeds
// the terminal: left_line + separator + right_line + "\n". The next row is
// laid down immediately under the leaking SGR state — bleeding the right
// pane's highlight onto the left pane's content.
//
// The invariant the fix must establish: every line emitted by renderPreviewPane
// leaves SGR state reset at its newline boundary.
func TestPreviewPane_RightPaneDoesNotLeakSGRState_Issue699(t *testing.T) {
	inst := session.NewInstance("bleed-699", t.TempDir())
	inst.Status = session.StatusRunning
	inst.Tool = "claude"

	h := homeWithSession(inst)

	// Simulate a tmux-captured Claude input line that opens a yellow bg
	// highlight and never closes it (closing reset is past what we captured,
	// or was clipped by the capture window). This is the exact failure mode
	// @javierciccarelli reports in Ghostty.
	h.previewCacheMu.Lock()
	h.previewCache[inst.ID] = "\x1b[43m> highlighted user input\nnormal line follows\n"
	h.previewCacheTime[inst.ID] = time.Now()
	h.previewCacheMu.Unlock()

	rendered := h.renderPreviewPane(80, 30)

	for i, line := range strings.Split(rendered, "\n") {
		if sgrActiveAt(line) {
			t.Fatalf("line %d leaves SGR state active at newline boundary — this bleeds highlight onto the left pane\nline=%q\nrendered=%q", i, line, rendered)
		}
	}
}

// Secondary case: long line that gets width-truncated. Truncation must also
// leave SGR state reset at the newline boundary — otherwise the ellipsis
// tail inherits/propagates the highlight.
func TestPreviewPane_TruncatedLineDoesNotLeakSGRState_Issue699(t *testing.T) {
	inst := session.NewInstance("bleed-699-trunc", t.TempDir())
	inst.Status = session.StatusRunning
	inst.Tool = "claude"

	h := homeWithSession(inst)

	// 200-char highlighted line with no closing reset — wider than any
	// plausible right-pane width, forcing ansi.Truncate to cut it.
	long := "\x1b[43m" + strings.Repeat("x", 200)
	h.previewCacheMu.Lock()
	h.previewCache[inst.ID] = long + "\n"
	h.previewCacheTime[inst.ID] = time.Now()
	h.previewCacheMu.Unlock()

	rendered := h.renderPreviewPane(60, 20)

	for i, line := range strings.Split(rendered, "\n") {
		if sgrActiveAt(line) {
			t.Fatalf("truncated line %d leaves SGR state active at newline boundary\nline=%q", i, line)
		}
	}
}

// Regression for the incremental-rendering variant of #699. The original fix
// closed SGR state at the end of each preview line, which makes a complete
// frame safe. Bubble Tea can skip unchanged rows, however, and later repaint a
// different row while the terminal still carries an SGR background from a
// captured preview line. Every final physical row must therefore start from
// default SGR state as well as leave it in default state.
func TestClampViewToViewport_ResetsEveryPhysicalRowAtBothBoundaries_Issue699(t *testing.T) {
	rendered := clampViewToViewport(
		"plain row\n\x1b[48;5;22mgreen diff row without captured reset",
		80,
		2,
	)

	for i, row := range strings.Split(rendered, "\n") {
		if !strings.HasPrefix(row, "\x1b[0m") {
			t.Fatalf("row %d has no leading SGR reset — incremental repaint can inherit preview background\nrow=%q", i, row)
		}
		if !strings.HasSuffix(row, "\x1b[0m") {
			t.Fatalf("row %d has no trailing SGR reset — next incremental repaint can inherit preview background\nrow=%q", i, row)
		}
		if sgrActiveAt(row) {
			t.Fatalf("row %d leaves SGR active at its boundary\nrow=%q", i, row)
		}
	}
}
