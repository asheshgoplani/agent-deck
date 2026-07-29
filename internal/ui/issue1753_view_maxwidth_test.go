// Issue #1753 — the TUI held a high steady-state CPU share and session
// switching lagged on a deck with ~55 live sessions.
//
// pprof against a sandboxed TUI (isolated HOME/profile/tmux socket, 60
// synthetic `--cmd sleep` sessions, 220x50) during a `j`/`k` switching workload
// put Home.View at 51% of all CPU samples and renderDualColumnLayout at 37%. At
// line level the single hottest statement was the closing "safety net":
//
//	mainContent = lipgloss.NewStyle().MaxWidth(h.width).Render(mainContent)
//
// 170ms of the function's 450ms (38%), ~14% of total process CPU, spent
// re-truncating and rebuilding every line of the whole composed frame on EVERY
// View() — every keystroke and every 2s tick — while on a normal terminal
// changing nothing at all.
//
// The fix runs that pass only when the frame actually overflows, decided by a
// single lipgloss.Width measurement of the joined output. These tests gate both
// halves:
//
//   - CORRECTNESS: when the frame already fits, running MaxWidth would change
//     nothing, so skipping it is unobservable; and when the frame does NOT fit,
//     the pass still runs, so behaviour there is byte-for-byte what it was.
//   - PERFORMANCE: on the widths and content users actually run, the frame does
//     fit, so the hot path stays off.
//
// A note on why the guard is a measurement and not arithmetic. An earlier
// attempt predicted the joined width from leftWidth + paneSeparatorWidth +
// rightWidth, reasoning that splitPaneWidths guarantees that sum equals h.width
// and ensureExactWidth makes every panel line exactly its pane width. The second
// half is false: ensureExactWidth pads a too-short line but never re-truncates a
// too-wide one, and MaxWidth truncation of a keycap grapheme cluster can land
// one cell OVER the target. JoinHorizontal then pads every row to the inflated
// block width and the frame overflows by a column.
// TestIssue1753_NarrowPaneKeycapStillRunsSafetyNet pins that case.

package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// dualColumnFixture is panel content shaped like a real frame: plain rows, wide
// CJK, emoji, ANSI colour, empty rows, and rows far wider than any pane so
// ensureExactWidth's truncating branch is exercised.
type dualColumnFixture struct {
	leftRows  []string
	rightRows []string
}

func realisticFixture() dualColumnFixture {
	return dualColumnFixture{
		leftRows: []string{
			"",
			"  ▾ perf (60)",
			"   ├─ ○ perf1 shell",
			"   ├─ ● 会議セッション 日本語タイトル",
			"   ├─ ◐ deploy 🚀 build ✅",
			lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render("   ├─ ○ styled-row-with-ansi"),
			strings.Repeat("very-long-session-title-", 20),
		},
		rightRows: []string{
			"📁 perf",
			"",
			"60 sessions",
			strings.Repeat("preview line that overflows the pane ", 12),
			"日本語のプレビュー内容がここに表示されます",
			lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("waiting for input…"),
			"idle",
		},
	}
}

// joinDualColumn reproduces exactly what renderDualColumnLayout composes: real
// ensureExactWidth on both panels, one pre-rendered separator cell per row, real
// lipgloss.JoinHorizontal. Everything downstream of this is the code under test.
func (f dualColumnFixture) join(leftWidth, rightWidth int) string {
	left := ensureExactWidth(strings.Join(f.leftRows, "\n"), leftWidth)
	right := ensureExactWidth(strings.Join(f.rightRows, "\n"), rightWidth)

	separatorCell := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(" │ ")
	separatorLines := make([]string, len(f.leftRows))
	for i := range separatorLines {
		separatorLines[i] = separatorCell
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Join(separatorLines, "\n"), right)
}

// assertMaxWidthIsNoOp fails unless running the MaxWidth pass over frame leaves
// it equivalent: same row count, same per-row display width, same visible text.
// That equivalence is what makes skipping the pass unobservable.
func assertMaxWidthIsNoOp(t *testing.T, label, frame string, width int) {
	t.Helper()

	netted := lipgloss.NewStyle().MaxWidth(width).Render(frame)
	frameRows := strings.Split(frame, "\n")
	nettedRows := strings.Split(netted, "\n")

	if len(frameRows) != len(nettedRows) {
		t.Fatalf("%s: MaxWidth changed row count: %d -> %d", label, len(frameRows), len(nettedRows))
	}
	for i := range frameRows {
		got, want := lipgloss.Width(frameRows[i]), lipgloss.Width(nettedRows[i])
		if got != want {
			t.Fatalf("%s: row %d is %d cells without the MaxWidth pass and %d with it — "+
				"skipping the pass is NOT unobservable", label, i, got, want)
		}
		if got > width {
			t.Fatalf("%s: row %d is %d cells wide, exceeds terminal width %d — it would wrap",
				label, i, got, width)
		}
		if gotText, wantText := ansi.Strip(frameRows[i]), ansi.Strip(nettedRows[i]); gotText != wantText {
			t.Fatalf("%s: row %d visible text differs with/without the MaxWidth pass:\n got: %q\nwant: %q",
				label, i, gotText, wantText)
		}
	}
}

// TestIssue1753_FittingFrameSkipsRebuildAndLosesNothing is the main gate: it is
// simultaneously the performance assertion (realistic frames fit, so the hot
// path is skipped) and the correctness assertion (where it is skipped, running
// it would have changed nothing).
//
// It sweeps every terminal width from the dual-column breakpoint through
// ultrawide across the full configurable split range.
func TestIssue1753_FittingFrameSkipsRebuildAndLosesNothing(t *testing.T) {
	fixture := realisticFixture()
	pcts := []int{
		session.MinPreviewPct,
		25,
		session.DefaultPreviewPct,
		75,
		session.MaxPreviewPct,
	}

	skipped := 0
	for width := layoutBreakpointStacked; width <= 400; width++ {
		for _, pct := range pcts {
			h := &Home{width: width, previewPct: pct}
			leftWidth, rightWidth := h.splitPaneWidths()
			frame := fixture.join(leftWidth, rightWidth)
			label := fmt.Sprintf("width=%d pct=%d", width, pct)

			// The guard as it appears in renderDualColumnLayout.
			if lipgloss.Width(frame) > width {
				t.Fatalf("%s: realistic frame measured %d cells and would still pay for the "+
					"full-screen MaxWidth rebuild — the #1753 regression is back",
					label, lipgloss.Width(frame))
			}
			assertMaxWidthIsNoOp(t, label, frame, width)
			skipped++
		}
	}

	if skipped == 0 {
		t.Fatal("swept no widths; the perf gate asserted nothing")
	}
}

// TestIssue1753_NarrowPaneKeycapStillRunsSafetyNet pins the case that proves the
// guard must measure rather than predict, and that the safety net is retained
// rather than removed.
//
// At width=80 with the minimum preview split the preview pane is only 8 cells.
// A keycap row truncated into that pane comes back one cell OVER 8, because
// ensureExactWidth never re-truncates an over-wide result and MaxWidth's own
// truncation cannot split the cluster. JoinHorizontal then pads every row to the
// inflated block width, so the frame measures 81 in an 80-column terminal even
// though leftWidth + paneSeparatorWidth + rightWidth == 80 exactly.
//
// The guard must therefore refuse to skip here. It must NOT assert the net then
// makes the frame fit: MaxWidth cannot shrink that cluster either. That is a
// pre-existing best-effort limitation of the net, unchanged by #1753 — the point
// is only that the same pass still runs on the same frames it always ran on.
func TestIssue1753_NarrowPaneKeycapStillRunsSafetyNet(t *testing.T) {
	fixture := realisticFixture()
	fixture.rightRows[4] = "ok 1️⃣2️⃣3️⃣"

	h := &Home{width: 80, previewPct: session.MinPreviewPct}
	leftWidth, rightWidth := h.splitPaneWidths()

	if sum := leftWidth + paneSeparatorWidth + rightWidth; sum != h.width {
		t.Fatalf("precondition: left(%d)+sep(%d)+right(%d)=%d, want %d — this test needs the "+
			"width budget to add up exactly, so that only a measurement can catch the overflow",
			leftWidth, paneSeparatorWidth, rightWidth, sum, h.width)
	}

	// The guard as it appears in renderDualColumnLayout is
	// `lipgloss.Width(frame) > h.width`, so this measurement IS the guard's
	// decision: it must come out over the terminal width, meaning the net runs.
	frame := fixture.join(leftWidth, rightWidth)
	if measured := lipgloss.Width(frame); measured <= h.width {
		t.Fatalf("keycap row in a %d-cell preview pane no longer inflates the frame "+
			"(measured %d <= %d), so the guard would skip the safety net here; this test "+
			"no longer covers the arithmetic-guard trap",
			rightWidth, measured, h.width)
	}
}

// TestIssue1753_UnnormalizedPaneStillRunsSafetyNet covers the degenerate
// splitPaneWidths branch. Below the chrome budget a pane width can come back
// non-positive, ensureExactWidth short-circuits and leaves that panel's rows
// unbounded, and the frame then measures far past the terminal — so the guard
// must run the net.
func TestIssue1753_UnnormalizedPaneStillRunsSafetyNet(t *testing.T) {
	const chromeBudget = minSessionsPaneWidth + minPreviewPaneWidth + paneSeparatorWidth
	fixture := realisticFixture()

	checked := 0
	for width := 1; width < chromeBudget; width++ {
		for pct := session.MinPreviewPct; pct <= session.MaxPreviewPct; pct += 5 {
			h := &Home{width: width, previewPct: pct}
			leftWidth, rightWidth := h.splitPaneWidths()
			if leftWidth > 0 && rightWidth > 0 {
				continue
			}
			frame := fixture.join(leftWidth, rightWidth)
			if lipgloss.Width(frame) <= width {
				t.Fatalf("width=%d pct=%d: pane widths (%d, %d) left a panel unnormalized, "+
					"yet the frame measured %d <= %d — the guard would skip the safety net",
					width, pct, leftWidth, rightWidth, lipgloss.Width(frame), width)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no non-positive pane width found below the chrome budget; this test no " +
			"longer covers the branch that still needs the safety net")
	}
}
