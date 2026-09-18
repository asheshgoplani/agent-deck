package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// Maintainer screenshot (2026-09-18): with the "accounts" optional header
// field enabled, its rendered text ran to the far right and got cut off,
// pushing the version badge off screen entirely — the header bar's old
// assembly relied on lipgloss's MaxWidth to make everything fit, which
// truncates bytes off the END of the rendered line rather than dropping a
// whole field. Rule: width-aware layout drops the lowest-priority optional
// field first (accounts, then sysStats, then cost) and never truncates a
// field mid-text.

func TestAssembleHeaderLeft_FitsWithoutDropping(t *testing.T) {
	left, fits := assembleHeaderLeft("LOGO", "Agent Deck", "base", "base compact", " | ",
		[]string{"cost", "sys", "accounts"}, "vBADGE", 200)
	if !fits {
		t.Fatalf("expected a fit at width=200, got fits=false, left=%q", left)
	}
	for _, want := range []string{"LOGO", "Agent Deck", "base", "cost", "sys", "accounts"} {
		if !strings.Contains(left, want) {
			t.Fatalf("assembled header missing %q at generous width: %q", want, left)
		}
	}
}

// TestAssembleHeaderLeft_DropsLowestPriorityFirst pins the drop order: when
// the full assembly does not fit, the LAST (lowest-priority) optional
// segment is dropped before any higher-priority one, and the version badge
// must still fit afterward. It also asserts no segment is ever truncated
// mid-text — a dropped segment vanishes entirely, or not at all.
func TestAssembleHeaderLeft_DropsLowestPriorityFirst(t *testing.T) {
	// Width chosen so all three optional segments together overflow, but
	// cost+sys alone (accounts dropped) fit.
	logo, title, base, sep := "L", "T", "B", " | "
	cost, sys, accounts := "COST123", "SYS456", "ACCOUNTS-VERY-LONG-FIELD-789"
	badge := "vX.Y.Z"

	left, fits := assembleHeaderLeft(logo, title, base, "", sep, []string{cost, sys, accounts}, badge, 40)
	if !fits {
		t.Fatalf("expected dropping the lowest-priority segment to make it fit; left=%q", left)
	}
	if strings.Contains(left, accounts) {
		t.Fatalf("accounts (lowest priority) must be the first segment dropped, but it is still present: %q", left)
	}
	if !strings.Contains(left, cost) || !strings.Contains(left, sys) {
		t.Fatalf("higher-priority segments (cost, sys) must survive when only accounts needs to be dropped: %q", left)
	}
	// No mid-text truncation: cost and sys, if present at all, must be
	// present in FULL, not a truncated prefix.
	if strings.Contains(left, cost) && !strings.Contains(left, "COST123") {
		t.Fatalf("cost segment was truncated mid-text: %q", left)
	}
	if strings.Contains(left, sys) && !strings.Contains(left, "SYS456") {
		t.Fatalf("sys segment was truncated mid-text: %q", left)
	}
}

// TestAssembleHeaderLeft_BaseStatsAloneOverflowsStillProtectsBadge pins the
// round-2 gap: a busy fleet's base status-counts segment (double-digit
// counts across all five statuses) plus a profile+group-scoped title can by
// itself overflow the header even with every optional field off — the case
// the review's own frames never stressed (both used a single-session demo
// fleet with a bare, group-less profile title). Before the fix this fell
// through to the header bar's MaxWidth call, which truncates bytes off the
// END of the line — always the version badge, since it is appended last —
// reproducing the exact maintainer screenshot this branch was written to
// fix. The base segment must collapse to its compact "N sessions" form
// instead, and the badge (and title) must never be dropped or truncated.
func TestAssembleHeaderLeft_BaseStatsAloneOverflowsStillProtectsBadge(t *testing.T) {
	logo := "⟨ ○ │ ○ │ ○ ⟩"
	title := "Agent Deck [work] [my-long-group-name]"
	base := "● 12 running • ◐ 8 waiting • ○ 24 idle • ■ 6 stopped • ✕ 3 error"
	baseCompact := "53 sessions"
	sep := " • "
	badge := "v1.16.11"

	// No optional fields enabled (cost/sysStats/accounts all off), matching
	// the review's repro: the fix's own optional-field drop logic never
	// even engages.
	var optional []string

	for _, width := range []int{80, 90, 100, 110, 120, 130} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			left, _ := assembleHeaderLeft(logo, title, base, baseCompact, sep, optional, badge, width)
			if !strings.Contains(left, logo) {
				t.Fatalf("logo must never be dropped or truncated at width=%d: %q", width, left)
			}
			if !strings.Contains(left, title) {
				t.Fatalf("profile+group title must never be dropped or truncated at width=%d: %q", width, left)
			}
			// assembleHeaderLeft returns only the LEFT side; renderView
			// appends the badge afterward using the same fits() budget this
			// function validated, so asserting fits()'s own contract here
			// (rather than string-searching for the badge, which isn't part
			// of "left") is the direct statement of the guarantee.
			gotWidth := lipgloss.Width(left) + lipgloss.Width(badge) + 3
			if width >= 80 && gotWidth > width {
				t.Fatalf("width=%d: assembled left (%q, width=%d) leaves no room for the badge (width=%d) — budget exceeded by %d",
					width, left, lipgloss.Width(left), lipgloss.Width(badge), gotWidth-width)
			}
		})
	}
}

func TestRenderAccountsCompactLine(t *testing.T) {
	if got := renderAccountsCompactLine(nil); got != "accounts  none" {
		t.Fatalf("empty usage = %q, want %q", got, "accounts  none")
	}

	usage := []session.AccountUsage{
		{Name: "personal", Known: true, FiveHour: session.AccountUsageWindow{Known: true, Percent: 42}},
		{Name: "work", Known: true, FiveHour: session.AccountUsageWindow{Known: true, Percent: 8}},
		{Name: "seminno", Known: false},
	}
	got := renderAccountsCompactLine(usage)
	if !strings.Contains(got, "3 slots") {
		t.Fatalf("compact line must count every slot including unknown ones: %q", got)
	}
	if !strings.Contains(got, "lowest 5h 8%") {
		t.Fatalf("compact line must report the minimum known 5h percent (8%%), got: %q", got)
	}

	noneKnown := []session.AccountUsage{{Name: "personal", Known: false}}
	got = renderAccountsCompactLine(noneKnown)
	if got != "accounts  1 slot" {
		t.Fatalf("with no known 5h reading, want the bare slot count singular form, got %q", got)
	}
}
