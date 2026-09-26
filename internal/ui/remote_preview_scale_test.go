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
// structural guarantee behind item 1, under both terminal width conventions
// (#2334).
func assertFrameFits(t *testing.T, frame string, width int) {
	t.Helper()
	assertNoOverwideRows(t, frame, width)
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

// assertWrappedLines is the contract every wrapPreviewLine result keeps: no
// line wider than width, no blank line, and a second pass at the same width
// changes nothing (renderEmptyStateResponsive wraps the body again).
func assertWrappedLines(t *testing.T, got []string, width int) {
	t.Helper()
	for i, l := range got {
		if lipgloss.Width(l) > width {
			t.Errorf("line %d is %d wide > %d: %q", i, lipgloss.Width(l), width, l)
		}
		if strings.TrimSpace(l) == "" {
			t.Errorf("line %d is blank in %q", i, got)
		}
		if again := wrapPreviewLine(l, width); len(again) != 1 || again[0] != l {
			t.Errorf("second pass changed line %d %q into %q", i, l, again)
		}
	}
}

// TestWrapPreviewLine_ClauseNearWidth is review finding 2: a clause that
// fits the first line but not a continuation line behind its "· " lead
// (any clause wider than width-4) used to land on a continuation line up to
// width+4 columns, and the second wrap pass then emitted a blank line. Every
// clause width from width-4 (still whole) to width (word-wrapped) must give
// lines that fit and survive a second pass unchanged.
func TestWrapPreviewLine_ClauseNearWidth(t *testing.T) {
	const width = 40
	first := strings.Repeat("a", width-2) // fills the first line on its own
	for clauseWidth := width - 4; clauseWidth <= width; clauseWidth++ {
		var words []string
		for w := 0; w < clauseWidth; {
			n := min(5, clauseWidth-w)
			words = append(words, strings.Repeat("x", n))
			w += n + 1
		}
		clause := strings.Join(words, " ")
		clause += strings.Repeat("x", clauseWidth-lipgloss.Width(clause))
		got := wrapPreviewLine(first+previewClauseSep+clause, width)
		assertWrappedLines(t, got, width)
		if got[0] != first {
			t.Errorf("clause width %d: first line = %q", clauseWidth, got[0])
		}
		if len(got) < 2 || !strings.HasPrefix(got[1], previewListIndent+previewContinuationLead) {
			t.Errorf("clause width %d: continuation must open with the lead: %q", clauseWidth, got)
		}
		if clauseWidth <= width-4 && (len(got) != 2 || got[1] != previewListIndent+previewContinuationLead+clause) {
			t.Errorf("clause width %d fits a continuation line whole, got %q", clauseWidth, got)
		}
		joined := strings.Join(strings.Fields(strings.Join(got, " ")), " ")
		if wantJoined := strings.Join(strings.Fields(first+" · "+clause), " "); joined != wantJoined {
			t.Errorf("clause width %d lost text: %q", clauseWidth, got)
		}
	}
	// The reviewer's repro, verbatim.
	for _, c := range []struct {
		line  string
		width int
	}{
		{"agent-deck v1.16.10+local.abcdef012… · older than here (update available)", 37},
		{"Sessions  0 running · 41 waiting · 2 idle · 0 stopped · 2 error · " + strings.Repeat("a-clause-", 7) + "abcdef", 70},
	} {
		assertWrappedLines(t, wrapPreviewLine(c.line, c.width), c.width)
	}
}

// TestCapPreviewLines pins the row cap on a wrapped single-line field: the
// lines past the budget fold into the last kept line behind "…", the result
// still fits the width, and a budget of 0 means no cap.
func TestCapPreviewLines(t *testing.T) {
	const width = 30
	line := "Harnesses  claude ×3 · codex ×2 · pi ×1 · gemini ×1 · opencode ×1 · aider ×1"
	wrapped := wrapPreviewLine(line, width)
	if len(wrapped) < 3 {
		t.Fatalf("test needs at least 3 wrapped lines, got %q", wrapped)
	}
	got := capPreviewLines(wrapped, width, 2)
	if len(got) != 2 || got[0] != wrapped[0] {
		t.Fatalf("cap = %q", got)
	}
	if !strings.HasSuffix(got[1], "…") || lipgloss.Width(got[1]) != width {
		t.Errorf("last kept line must end in … at the width: %q", got[1])
	}
	if !strings.HasPrefix(got[1], wrapped[1]) {
		t.Errorf("last kept line must keep its own text first: %q vs %q", got[1], wrapped[1])
	}
	if got := capPreviewLines(wrapped, width, 0); len(got) != len(wrapped) {
		t.Errorf("rows 0 must not cap: %q", got)
	}
	if got := capPreviewLines(wrapped, width, len(wrapped)); len(got) != len(wrapped) || got[len(got)-1] != wrapped[len(wrapped)-1] {
		t.Errorf("a fitting list must come back unchanged: %q", got)
	}
	if got := fitPreviewLine(line, previewLayout{width: width, rows: 1}); len(got) != 1 || !strings.HasSuffix(got[0], "…") || lipgloss.Width(got[0]) != width {
		t.Errorf("one row: %q", got)
	}
}

// everyFieldHome is the adversarial remote of the review (findings 2 and 3):
// every field on, 13 harness tools, a build-metadata version, 12 long slot
// names, 6 ssh users, an old and slow poll.
func everyFieldHome(t *testing.T, fixedPollTime time.Time) *Home {
	t.Helper()
	home := goldenRemotePreviewHome(t)
	withControllerVersion(t, "1.16.11")
	tools := []string{"claude", "codex", "pi", "gemini", "opencode", "aider", "cursor-agent", "amp", "goose", "shell", "kimi", "qwen-code", "copilot-cli"}
	var infos []session.RemoteSessionInfo
	for i, tool := range tools {
		infos = append(infos, session.RemoteSessionInfo{ID: fmt.Sprintf("r%d", i), Title: tool, Status: []string{"running", "waiting", "idle", "stopped", "error"}[i%5], Tool: tool})
	}
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": infos}
	writeXDGTestConfig(t, os.Getenv("HOME"), `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"

[ui.remote_preview]
fields = ["version", "sessions_by_status", "harnesses", "load", "memory", "disk", "last_poll", "accounts", "ssh"]
`)
	home.remoteVersions = map[string]session.RemoteVersionState{
		"lab": {Version: "1.16.10+local.abcdef0123456789abcdef0123456789", Found: true, CheckedAt: fixedPollTime},
	}
	var accounts []session.AccountUsage
	for i := 0; i < 12; i++ {
		u := session.AccountUsage{Name: fmt.Sprintf("a-very-long-account-slot-name-number-%02d", i)}
		if i%3 != 2 {
			u.Known, u.HasUpdatedAt, u.UpdatedAt = true, true, fixedPollTime.Add(-time.Duration(i)*time.Minute)
			u.FiveHour = session.AccountUsageWindow{Known: true, Percent: float64(100 - i*3)}
			u.SevenDay = session.AccountUsageWindow{Known: true, Percent: float64(i * 5)}
		} else {
			u.UnknownReason = session.AccountUsageUnreadable
		}
		accounts = append(accounts, u)
	}
	var ssh []session.RemoteSSHSession
	for i := 0; i < 6; i++ {
		ssh = append(ssh, session.RemoteSSHSession{User: fmt.Sprintf("very-long-user-name-%d", i), Count: 10 - i, HasSince: true, Since: fixedPollTime.Add(-time.Duration(30*i) * time.Hour), From: "203.0.113.7"})
	}
	home.remoteHostStats = map[string]remoteHostStatsResult{
		"lab": {
			Stats: session.RemoteHostStats{
				Ok: true, CPUAvailable: true, CPUUsagePercent: 99.9,
				LoadAvailable: true, Load1: 123.45, Load5: 99.1, Load15: 88.2,
				MemAvailable: true, MemUsedBytes: 1 << 41, MemTotalBytes: 1 << 42, MemUsagePercent: 50,
				DiskAvailable: true, DiskUsedBytes: 1 << 46, DiskTotalBytes: 1 << 47, DiskUsagePercent: 50,
				AccountsAvailable: true, Accounts: accounts,
				SSHAvailable: true, SSHSessions: ssh,
			},
			Latency:   125 * time.Second,
			FetchedAt: fixedPollTime.Add(-40 * 24 * time.Hour),
		},
	}
	return home
}

// TestRemotePreview_EveryFieldFits is the review's adversarial sweep as a
// branch test: with every field on and long values, at every pane size, no
// line is wider than the pane, the frame is exactly the pane height, the
// body has no blank line inside it (finding 2), and from 45 columns and 18
// rows up the hint is still on screen (finding 3). Golden frames at w45/h30
// and w60/h20: testdata/remote_preview/allfields-w<width>-h<height>.txt.
func TestRemotePreview_EveryFieldFits(t *testing.T) {
	fixedPollTime := time.Now().Add(-5 * time.Minute)
	golden := map[[2]int]bool{{45, 30}: true, {60, 20}: true}
	for _, w := range []int{36, 40, 45, 50, 60, 70, 100} {
		for _, h := range []int{12, 14, 18, 20, 24, 30, 50} {
			t.Run(fmt.Sprintf("w%d/h%d", w, h), func(t *testing.T) {
				home := everyFieldHome(t, fixedPollTime)
				raw := home.renderRemotePreview(home.flatItems[0], w, h)
				got := stripAnsi(raw)
				lines := strings.Split(got, "\n")
				if len(lines) != h {
					t.Errorf("frame has %d lines, pane height %d", len(lines), h)
				}
				assertFrameFits(t, got, w)
				if w >= 45 && h >= 18 {
					if !strings.Contains(got, "Press Enter") {
						t.Errorf("hint pushed off:\n%s", got)
					}
					// No blank line between the first body line and the hint.
					first, hint := -1, -1
					for i, l := range lines {
						if strings.Contains(l, "agent-deck v") && first < 0 {
							first = i
						}
						if strings.Contains(l, "Press Enter") {
							hint = i
						}
					}
					if first >= 0 {
						for i := first; i < hint-1; i++ {
							if strings.TrimSpace(lines[i]) == "" {
								t.Errorf("blank line %d inside the body:\n%s", i, got)
							}
						}
					}
				}
				if !golden[[2]int{w, h}] {
					return
				}
				got = strings.TrimRight(got, "\n") + "\n"
				path := filepath.Join("testdata", "remote_preview", fmt.Sprintf("allfields-w%d-h%d.txt", w, h))
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
}
