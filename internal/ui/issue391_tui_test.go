package ui

// Issue #391 — per-session color tint rendered in the TUI row.
//
// PR #650 (shipped in v1.7.27) added the Instance.Color field, TOML
// validation, CLI plumbing, SQLite persistence, and `list --json`
// exposure. The field was plumbed end-to-end but NOT rendered: a user
// running `agent-deck session set <id> color '#FF0000'` saw the value
// round-trip through persistence yet the TUI dashboard still drew every
// session row in the default palette.
//
// This file pins the render contract at Seam A (model-level, see
// internal/ui/TUI_TESTS.md). When Instance.Color is a valid lipgloss
// color spec, renderSessionItem MUST emit an ANSI escape for that
// foreground in the row's title. When Color is empty, rendering must
// remain byte-identical to the v1.7.29 baseline.
//
// Seam choice: Seam A over Seam B because the only observable here is
// the output of a single render function on a single row. No teatest
// runtime needed; no tmux needed. Fastest seam that catches the bug.

import (
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// forceTrueColorProfileOnce guarantees the lipgloss global profile is
// TrueColor for any test in this file. Go tests don't run under a TTY,
// so lipgloss' auto-detect falls back to Ascii and strips every escape —
// which would make an "assert ANSI escape present" check vacuous.
var forceTrueColorProfileOnce sync.Once

func forceTrueColorProfile() {
	forceTrueColorProfileOnce.Do(func() {
		lipgloss.SetColorProfile(termenv.TrueColor)
	})
}

// renderSingleSessionRow drives the minimal inputs required for
// renderSessionItem in isolation. It deliberately avoids newSeamATestHome
// (which wires every dialog pointer) because renderSessionItem only
// reads width + style globals + the snapshot map. Keeping the helper
// narrow makes the assertion below easy to reason about.
func renderSingleSessionRow(t *testing.T, inst *session.Instance) string {
	t.Helper()
	forceTrueColorProfile()

	h := &Home{width: 140}
	item := session.Item{
		Type:          session.ItemTypeSession,
		Session:       inst,
		Level:         1,
		Path:          "test",
		IsLastInGroup: true,
	}
	snapshot := map[string]sessionRenderState{
		inst.ID: {
			status:    session.StatusRunning,
			tool:      "claude",
			paneTitle: "",
		},
	}

	var b strings.Builder
	h.renderSessionItem(&b, item, false, snapshot, h.width)
	return b.String()
}

// TestIssue391_SessionRow_HexColorRenderedAsForeground is the primary
// regression pin. A session with Color="#FF0000" must cause the row
// output to include the truecolor ANSI escape `38;2;255;0;0` (lipgloss'
// encoding for red foreground under TrueColor profile).
//
// Failure mode on v1.7.29 (pre-fix): the row contains no red escape —
// only the default title-style foreground — because renderSessionItem
// ignores Instance.Color entirely.
//
// This test does NOT assert on byte-exact output because surrounding
// escape sequences (status icon, tool badge, tree connectors) share the
// row. It asserts only on the presence of the expected foreground
// sequence — the load-bearing signal for "the tint made it to output."
func TestIssue391_SessionRow_HexColorRenderedAsForeground(t *testing.T) {
	inst := &session.Instance{
		ID:    "sess-red",
		Title: "red-session",
		Color: "#FF0000",
	}

	row := renderSingleSessionRow(t, inst)

	// TrueColor hex "#FF0000" → lipgloss renders as `\x1b[38;2;255;0;0m`.
	// We check for the decimal triple embedded in the row, not the full
	// escape, so we survive future SGR-combining changes in lipgloss.
	const wantFgSig = "38;2;255;0;0"
	if !strings.Contains(row, wantFgSig) {
		t.Fatalf("issue #391 regression: session row for Color=%q did not contain the truecolor foreground escape %q. "+
			"Got raw row: %q. "+
			"This means renderSessionItem is not applying Instance.Color to the title — the field round-trips through "+
			"persistence but never reaches the render layer.",
			inst.Color, wantFgSig, row)
	}
}

// TestIssue391_SessionRow_ANSIIndexColorRendered pins the second
// accepted color format from isValidSessionColor: a decimal 0..255 ANSI
// palette index. Under TrueColor profile, lipgloss emits `38;5;<idx>` for
// ANSI256-palette colors.
func TestIssue391_SessionRow_ANSIIndexColorRendered(t *testing.T) {
	inst := &session.Instance{
		ID:    "sess-ansi",
		Title: "ansi-session",
		Color: "196", // bright red in the 256-palette
	}

	row := renderSingleSessionRow(t, inst)

	const wantFgSig = "38;5;196"
	if !strings.Contains(row, wantFgSig) {
		t.Fatalf("issue #391 regression: session row for Color=%q did not contain the 256-palette foreground escape %q. "+
			"Got raw row: %q.",
			inst.Color, wantFgSig, row)
	}
}

// TestIssue391_SessionRow_EmptyColorLeavesRowUntinted guards the
// fall-through invariant: when Instance.Color is "", the render output
// must be byte-identical to the pre-#391 baseline. We can't compare
// against a frozen fixture (too brittle across style tweaks), but we
// CAN prove the red signature doesn't appear by accident — i.e. the
// tint is opt-in and invisible to users who don't set it.
func TestIssue391_SessionRow_EmptyColorLeavesRowUntinted(t *testing.T) {
	inst := &session.Instance{
		ID:    "sess-default",
		Title: "default-session",
		Color: "",
	}

	row := renderSingleSessionRow(t, inst)

	// If this fires, something leaked a red tint into the default path.
	// The default palette has no 255,0,0 foreground; seeing it means a
	// regression that turned the opt-in tint into a mandatory one.
	if strings.Contains(row, "38;2;255;0;0") {
		t.Fatalf("issue #391 regression: session row with Color=\"\" unexpectedly contains a red truecolor escape. "+
			"The tint must be fully opt-in. Got: %q", row)
	}
}
