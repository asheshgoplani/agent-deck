package ui

// Scaling tests for the remote preview panel's stats block: no field may
// widen the block past the pane or shift the other fields (the rc.6 defect:
// a 7-slot accounts line centred the whole block against itself and pushed
// every other line off the right edge), and the accounts field lists one
// row per slot with aligned columns, capped to the pane height.
//
// Golden frames: testdata/remote_preview/accounts-<n>slots-w<width>.txt.
// Regenerate with UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestRemotePreview_AccountsScaleGolden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// scaleTestSlotNames are fleet-shaped slot names: 12 of them, the first 7
// being the maintainer's real fleet (item 1: the 7-slot/100-wide frame must
// show every name in full).
var scaleTestSlotNames = []string{
	"alice-team-a", "alice-personal", "alice-team-b",
	"bob-team-a", "bob-team-b",
	"carol-team-a", "carol-team-b",
	"dave-team-a", "dave-team-b", "erin-team-a", "erin-team-b", "frank-personal",
}

// scaleTestAccounts builds n slots with a deterministic mix of fresh, stale
// and unknown usage so sorting (most-loaded first, unknown last) and every
// reason clause show up in the frames.
func scaleTestAccounts(n int, now time.Time) []session.AccountUsage {
	out := make([]session.AccountUsage, 0, n)
	for i := 0; i < n; i++ {
		u := session.AccountUsage{Name: scaleTestSlotNames[i]}
		switch i % 4 {
		case 0:
			u.Known, u.HasUpdatedAt, u.UpdatedAt = true, true, now.Add(-time.Duration(i+1)*time.Minute)
			u.FiveHour = session.AccountUsageWindow{Known: true, Percent: float64(90 - i*7)}
			u.SevenDay = session.AccountUsageWindow{Known: true, Percent: float64(40 + i)}
		case 1:
			u.Known, u.HasUpdatedAt, u.UpdatedAt = true, true, now.Add(-2*time.Hour)
			u.FiveHour = session.AccountUsageWindow{Known: true, Percent: float64(30 + i)}
			u.SevenDay = session.AccountUsageWindow{Known: true, Percent: float64(12 + i)}
		case 2:
			u.UnknownReason = session.AccountUsageNoFeed
		case 3:
			u.Known, u.HasUpdatedAt, u.UpdatedAt = true, true, now.Add(-time.Duration(i+1)*time.Minute)
			u.FiveHour = session.AccountUsageWindow{Known: true, Percent: float64(8 + i)}
			u.SevenDay = session.AccountUsageWindow{Known: true, Percent: float64(60 - i)}
		}
		out = append(out, u)
	}
	return out
}

// scaleGoldenHome parks a Home on the remote group with an accounts-only
// field list and n slots reported by the remote.
func scaleGoldenHome(t *testing.T, n int, fixedPollTime time.Time) *Home {
	t.Helper()
	home := goldenRemotePreviewHome(t)
	writeXDGTestConfig(t, os.Getenv("HOME"), `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"

[ui.remote_preview]
fields = ["version", "sessions_by_status", "harnesses", "accounts"]
`)
	home.remoteVersions = map[string]session.RemoteVersionState{
		"lab": {Version: "1.16.10", Found: true, CheckedAt: fixedPollTime},
	}
	home.remoteHostStats = map[string]remoteHostStatsResult{
		"lab": {
			Stats: session.RemoteHostStats{
				Ok:                true,
				AccountsAvailable: true,
				Accounts:          scaleTestAccounts(n, fixedPollTime),
			},
			Latency:   1200 * time.Millisecond,
			FetchedAt: fixedPollTime,
		},
	}
	return home
}

// assertFrameFits fails when any rendered line is wider than width: the
// structural guarantee behind item 1.
func assertFrameFits(t *testing.T, frame string, width int) {
	t.Helper()
	for i, line := range strings.Split(frame, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d is %d wide, pane is %d: %q", i, w, width, line)
		}
	}
}

func TestRemotePreview_AccountsScaleGolden(t *testing.T) {
	fixedPollTime := time.Now().Add(-5 * time.Minute)
	type frame struct {
		slots, width, height int
	}
	var frames []frame
	for _, n := range []int{1, 2, 7, 12} {
		for _, w := range []int{60, 100, 200} {
			frames = append(frames, frame{n, w, 30})
		}
	}
	// A short pane: the 12-slot list must be capped with "+N more" rather
	// than pushing the hint (or anything else) off the bottom.
	frames = append(frames, frame{12, 100, 18})

	for _, f := range frames {
		name := fmt.Sprintf("accounts-%dslots-w%d", f.slots, f.width)
		if f.height != 30 {
			name += fmt.Sprintf("-h%d", f.height)
		}
		t.Run(name, func(t *testing.T) {
			home := scaleGoldenHome(t, f.slots, fixedPollTime)
			raw := home.renderRemotePreview(home.flatItems[0], f.width, f.height)
			got := strings.TrimRight(stripAnsi(raw), "\n") + "\n"
			assertFrameFits(t, got, f.width)
			if lines := strings.Count(raw, "\n") + 1; lines != f.height {
				t.Errorf("frame has %d lines, pane height is %d", lines, f.height)
			}
			if !strings.Contains(got, "Press Enter") {
				t.Errorf("hint line was pushed off the pane:\n%s", got)
			}
			if !strings.Contains(got, "Sessions  ") || !strings.Contains(got, "Harnesses  ") {
				t.Errorf("stats lines went missing:\n%s", got)
			}
			if f.slots == 7 && f.width == 100 {
				for _, slot := range scaleTestSlotNames[:7] {
					if !strings.Contains(got, slot) {
						t.Errorf("7-slot/100-wide frame must show %q in full:\n%s", slot, got)
					}
				}
			}
			if f.height == 18 && !strings.Contains(got, " more") {
				t.Errorf("12 slots in an 18-row pane must be capped with +N more:\n%s", got)
			}

			path := filepath.Join("testdata", "remote_preview", name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
			}
			if string(want) != got {
				t.Fatalf("golden %s differs from the rendered preview.\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		})
	}
}

// TestRemotePreview_WideLineNeverShiftsBlock is the rc.6 defect in isolation:
// with one over-wide body line the "Host:" subtitle and the other body lines
// must sit exactly where they sit without it.
func TestRemotePreview_WideLineNeverShiftsBlock(t *testing.T) {
	fixedPollTime := time.Now().Add(-5 * time.Minute)
	const width = 100
	columnOf := func(frame, needle string) int {
		for _, line := range strings.Split(frame, "\n") {
			if idx := strings.Index(line, needle); idx >= 0 {
				return idx
			}
		}
		t.Fatalf("%q not found in frame:\n%s", needle, frame)
		return -1
	}
	narrow := scaleGoldenHome(t, 1, fixedPollTime)
	narrowFrame := stripAnsi(narrow.renderRemotePreview(narrow.flatItems[0], width, 30))
	wide := scaleGoldenHome(t, 12, fixedPollTime)
	wideFrame := stripAnsi(wide.renderRemotePreview(wide.flatItems[0], width, 30))

	assertFrameFits(t, wideFrame, width)
	for _, needle := range []string{"Host: ", "agent-deck v", "Sessions  ", "Harnesses  "} {
		if a, b := columnOf(narrowFrame, needle), columnOf(wideFrame, needle); a != b {
			t.Errorf("%q moved from column %d to %d when the accounts list grew:\n%s", needle, a, b, wideFrame)
		}
	}
}

// TestRenderAccountsPreviewBlock pins the table shape: summary line, rows
// sorted most-loaded first with unknown slots last, aligned columns, reason
// clauses, and the +N more cap.
func TestRenderAccountsPreviewBlock(t *testing.T) {
	now := time.Now()
	usage := []session.AccountUsage{
		{Name: "work", Known: true, HasUpdatedAt: true, UpdatedAt: now.Add(-3 * time.Minute),
			FiveHour: session.AccountUsageWindow{Known: true, Percent: 8}, SevenDay: session.AccountUsageWindow{Known: true, Percent: 24}},
		{Name: "personal", Known: true, HasUpdatedAt: true, UpdatedAt: now.Add(-2 * time.Hour),
			FiveHour: session.AccountUsageWindow{Known: true, Percent: 92}, SevenDay: session.AccountUsageWindow{Known: true, Percent: 61}},
		{Name: "buddii", UnknownReason: session.AccountUsageNoFeed},
		{Name: "seminno", UnknownReason: session.AccountUsageNoData},
		{Name: "old-remote"},
	}

	t.Run("full table", func(t *testing.T) {
		got := renderAccountsPreviewBlock(usage, now, previewLayout{width: 80, rows: 10})
		want := []string{
			"accounts  5 slots · 5h lowest 8% · 1 stale · 3 unknown",
			"  personal    5h 92%  7d 61%  stale, 2 h ago",
			"  work        5h 8%   7d 24%  3 min ago",
			"  buddii      —       —       no feed",
			"  old-remote  —       —       usage unknown",
			"  seminno     —       —       no data yet",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("capped to rows", func(t *testing.T) {
		got := renderAccountsPreviewBlock(usage, now, previewLayout{width: 80, rows: 4})
		want := []string{
			"accounts  5 slots · 5h lowest 8% · 1 stale · 3 unknown",
			"  personal    5h 92%  7d 61%  stale, 2 h ago",
			"  work        5h 8%   7d 24%  3 min ago",
			"  +3 more",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("summary only when one row fits", func(t *testing.T) {
		got := renderAccountsPreviewBlock(usage, now, previewLayout{width: 80, rows: 1})
		if len(got) != 1 || !strings.HasPrefix(got[0], "accounts  5 slots") {
			t.Fatalf("block = %q", got)
		}
	})

	t.Run("narrow pane truncates the name column, never the row", func(t *testing.T) {
		got := renderAccountsPreviewBlock(usage, now, previewLayout{width: 36, rows: 10})
		for _, line := range got {
			if lipgloss.Width(line) > 36 {
				t.Errorf("line wider than 36: %q", line)
			}
		}
		if !strings.Contains(strings.Join(got, "\n"), "…") {
			t.Errorf("expected a truncated name column:\n%s", strings.Join(got, "\n"))
		}
	})

	t.Run("one slot", func(t *testing.T) {
		got := renderAccountsPreviewBlock(usage[:1], now, previewLayout{width: 80, rows: 10})
		if got[0] != "accounts  1 slot · 5h 8%" {
			t.Fatalf("summary = %q", got[0])
		}
	})

	t.Run("no slots", func(t *testing.T) {
		got := renderAccountsPreviewBlock(nil, now, previewLayout{width: 80, rows: 10})
		if len(got) != 1 || got[0] != "accounts  none" {
			t.Fatalf("block = %q", got)
		}
	})
}

// TestRenderAccountUsageEntry_Reasons pins the header's one-line form naming
// the reason a slot is unknown instead of the bare "usage unknown".
func TestRenderAccountUsageEntry_Reasons(t *testing.T) {
	now := time.Now()
	cases := map[string]string{
		session.AccountUsageNoFeed:     "work no feed",
		session.AccountUsageNoData:     "work no data yet",
		session.AccountUsageUnreadable: "work unreadable",
		"":                             "work usage unknown",
	}
	for reason, want := range cases {
		if got := renderAccountUsageEntry(session.AccountUsage{Name: "work", UnknownReason: reason}, now); got != want {
			t.Errorf("reason %q: got %q, want %q", reason, got, want)
		}
	}
}

// TestWrapPreviewLine pins clause-aware wrapping: breaks land between " · "
// clauses with a "· " lead on the continuation, hyphenated names never split,
// and nothing is ever wider than the width.
func TestWrapPreviewLine(t *testing.T) {
	line := "Sessions  1 running · 1 waiting · 1 idle · 1 stopped · 1 error"
	got := wrapPreviewLine(line, 52)
	want := []string{"Sessions  1 running · 1 waiting · 1 idle · 1 stopped", "  · 1 error"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	if got := wrapPreviewLine(line, 200); len(got) != 1 || got[0] != line {
		t.Errorf("a fitting line must come back unchanged: %q", got)
	}
	names := "accounts  bob-team-b no feed · carol-team-b no feed"
	for _, w := range []int{20, 30, 40} {
		for _, l := range wrapPreviewLine(names, w) {
			if lipgloss.Width(l) > w {
				t.Errorf("width %d: line %q too wide", w, l)
			}
			if strings.HasSuffix(l, "-") || strings.Contains(l, "sharjeel-\n") {
				t.Errorf("width %d: hyphenated name split: %q", w, l)
			}
		}
	}
	if got := wrapPreviewLine("abcdefghijklmnopqrstuvwxyz", 10); strings.Join(got, "|") != "abcdefghij|  klmnopqr|  stuvwxyz" {
		t.Errorf("hard wrap of one long word = %q", got)
	}
}
