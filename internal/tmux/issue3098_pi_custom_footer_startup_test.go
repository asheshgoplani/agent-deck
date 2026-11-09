package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #3098 (part A): with pi-powerline-footer installed, pi draws its stock
// footer for one frame and then swaps in the extension's footer and editor.
// None of pi's PromptPatterns (#2521) match the swapped layout, so an idle pi
// pane that never ran the agent-deck extension's session_start hook stayed
// "starting" until the startup watchdog replaced it with the timeout hold.
//
// What both layouts share is pi's editor: a horizontal-rule border, the input
// line(s), another rule, near the bottom of the frame. The overdue startup
// probe now accepts that box as proof pi is alive.

// pi3098PowerlineIdle is the synthetic idle frame quoted in #3098 (widths
// shortened): the powerline header, then the composer box with " >".
const pi3098PowerlineIdle = "  Model (Provider)  think:med   project   0/1.0M (0.0%)\n" +
	" ────────────────────────────────────────\n" +
	" >\n" +
	" ────────────────────────────────────────\n"

// pi3098PowerlineTyping is the same layout with a two-line draft in the
// composer (typing without submitting, step 3 of the report).
const pi3098PowerlineTyping = "  Model (Provider)  think:med   project   0/1.0M (0.0%)\n" +
	" ────────────────────────────────────────\n" +
	" > please refactor the status poller so it\n" +
	"   stops double-capturing the pane\n" +
	" ────────────────────────────────────────\n"

// pi3098PowerlineScrolled is a powerline composer whose draft overflows: the
// upper border carries the scroll arrow glued to the rule.
const pi3098PowerlineScrolled = "  Model (Provider)  think:med   project   12k/1.0M (1.2%)\n" +
	" ↑───────────────────────────────────────\n" +
	"   line 4 of the draft\n" +
	"   line 5 of the draft\n" +
	" > line 6 of the draft\n" +
	" ────────────────────────────────────────\n"

// pi3098StockScrolled is pi's stock editor scrolled ("─── ↑ 3 more ───"),
// with a footer that is not the #2521 shape (a custom footer extension).
const pi3098StockScrolled = " Earlier answer text.\n\n" +
	"──────────────── ↑ 3 more ────────────────\n" +
	"draft line 4\n" +
	"draft line 5\n" +
	"──────────────────────────────────────────\n" +
	"~/project (main)\n" +
	"custom footer: model-x  ctx 4%\n"

// pi3098StockIdle is pi's stock idle frame (#2521 footer), which the existing
// prompt patterns already accept; it pins that the stock layout keeps working.
const pi3098StockIdle = " Answer text.\n\n" +
	"──────────────────────────────────────────\n" +
	"\n" +
	"──────────────────────────────────────────\n" +
	"~/project (main)\n" +
	"0.0%/1.0M (auto)                  (provider) vendor/model-id • max\n"

// pi3098CrashNoBox is what a pi session pane shows when pi died while
// loading an extension: a stack trace, no composer box (and no shell prompt,
// which pi's prompt detector would accept on its own).
const pi3098CrashNoBox = "Error: Cannot find module 'pi-powerline-footer'\n" +
	"    at Module._resolveFilename (node:internal/modules/cjs/loader:1225:15)\n" +
	"    at Module._load (node:internal/modules/cjs/loader:1051:27)\n" +
	"────────────\n"

// pi3098PowerlineWorking is a live turn under powerline: pi draws its
// "── ⠴ Working ──" banner as the composer's top border (#2948).
const pi3098PowerlineWorking = "  Model (Provider)  think:med   project   3k/1.0M (0.3%)\n" +
	"── ⠴ Working ─────────────────────────────\n" +
	" >\n" +
	" ────────────────────────────────────────\n"

// pi3098Overdue backdates the startup clock past the watchdog window.
func pi3098Overdue(s *Session) {
	s.mu.Lock()
	s.startupAt = time.Now().Add(-startupStateWindow - time.Second)
	s.lastStableStatus = "starting"
	s.mu.Unlock()
}

func TestIssue3098_PiCustomFooterOverdueSurvives(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct{ name, content, marker string }{
		{"powerline-idle", pi3098PowerlineIdle, "0/1.0M (0.0%)"},
		{"powerline-typing", pi3098PowerlineTyping, "double-capturing"},
		{"powerline-scrolled", pi3098PowerlineScrolled, "line 6 of the draft"},
		{"stock-scrolled-custom-footer", pi3098StockScrolled, "custom footer"},
		{"stock-idle", pi3098StockIdle, "vendor/model-id"},
		// #2948 residue: a banner above a complete idle box is not a live
		// turn, so this frame is idle and only the box vouches for it.
		{"working-residue-above-idle-box", " ── ⠴ Working ──\n" + pi3098PowerlineIdle, "0/1.0M (0.0%)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startPaneWithContent(t, "issue3098-"+tc.name, tc.content, "pi --no-skills", tc.marker)
			oldPID, _ := s.getPaneProcessTree()
			pi3098Overdue(s)

			status, err := s.GetStatus()
			if err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if status == "error" {
				t.Fatalf("overdue pi pane showing its composer box was expired into the timeout hold")
			}
			newPID, _ := s.getPaneProcessTree()
			if oldPID == 0 || newPID != oldPID {
				t.Fatalf("pi pane replaced: pid %d -> %d", oldPID, newPID)
			}
			s.mu.Lock()
			startupAt, timedOut := s.startupAt, s.startupTimedOut
			s.mu.Unlock()
			if timedOut {
				t.Fatal("startup timeout claimed for a live pi pane")
			}
			if !startupAt.IsZero() {
				t.Fatalf("startup watchdog still armed: %v", startupAt)
			}
			pane, _ := s.CapturePaneFresh()
			if strings.Contains(strings.ToLower(StripANSI(pane)), "timed out") {
				t.Fatalf("pane shows the timeout hold:\n%s", pane)
			}
		})
	}
}

// Busy still wins: a live pi turn under powerline (banner on the composer's
// top border) reads active after the overdue probe resolves startup.
func TestIssue3098_PiWorkingBannerOnComposerStaysActive(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := startPaneWithContent(t, "issue3098-working", pi3098PowerlineWorking, "pi --no-skills", "3k/1.0M (0.3%)")
	pi3098Overdue(s)
	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != "active" {
		t.Fatalf("status = %q, want active", status)
	}
}

// The #1892 guard: a pane without a busy signal, a prompt or a composer box
// must still expire, for pi as well as claude.
func TestIssue3098_PiWithoutComposerBoxStillExpires(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct{ name, content, marker string }{
		{"stuck-1892", issue2361StuckPaneContent, "zfadsffa"},
		{"crash-no-box", pi3098CrashNoBox, "_resolveFilename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startPaneWithContent(t, "issue3098-expire-"+tc.name, tc.content, "pi --no-skills", tc.marker)
			pi3098Overdue(s)
			status, err := s.GetStatus()
			if err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if status != "error" {
				t.Fatalf("pi pane without a composer box: status = %q, want error", status)
			}
		})
	}
}

// The box is pi's: a claude pane that happens to end in two rules is not
// vouched for by the pi recognizer (claude's own patterns decide).
func TestIssue3098_ComposerBoxOnlyCountsForPi(t *testing.T) {
	skipIfNoTmuxBinary(t)
	content := "some output\n" +
		" ────────────────────────────────────────\n" +
		" zzz\n" +
		" ────────────────────────────────────────\n"
	s := startPaneWithContent(t, "issue3098-not-pi", content, "some-other-agent", "zzz")
	pi3098Overdue(s)
	status, err := s.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != "error" {
		t.Fatalf("non-pi pane with a rule box: status = %q, want error", status)
	}
}

func TestIssue3098_PiComposerBoxVisible(t *testing.T) {
	rule := strings.Repeat("─", 40)
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"powerline idle", pi3098PowerlineIdle, true},
		{"powerline typing", pi3098PowerlineTyping, true},
		{"powerline scrolled", pi3098PowerlineScrolled, true},
		{"powerline scrolled down", " ↓" + rule + "\n > x\n " + rule, true},
		{"stock scrolled", pi3098StockScrolled, true},
		{"stock idle", pi3098StockIdle, true},
		{"stock scrolled both ways", "─── ↑ 3 more ───────\nx\n─── ↓ 12 more ──────\n~/p", true},
		{"corpus-like idle with wide footer", "answer\n\n" + rule + "\n\n" + rule + "\n~/p (main)\n↑5.2M ↓298k $1 20.9%/272k (auto)", true},
		{"box then footer and widgets", rule + "\n>\n" + rule + "\nw1\nw2\nw3\nw4\nw5\nw6\nw7", true},
		{"trailing blank pane rows", pi3098PowerlineIdle + strings.Repeat("\n", 30), true},
		{"heavy rules", strings.Repeat("━", 20) + "\n>\n" + strings.Repeat("━", 20), true},
		{"empty", "", false},
		{"stuck 1892", issue2361StuckPaneContent, false},
		{"crash", pi3098CrashNoBox, false},
		{"single rule", "text\n" + rule + "\n> \n", false},
		{"adjacent rules, no input line", "x\n" + rule + "\n" + rule + "\n", false},
		{"short rules", "─────\n>\n─────\n", false},
		{"rules too far apart", rule + "\n" + strings.Repeat("line\n", 13) + rule, false},
		{"box scrolled away", rule + "\n>\n" + rule + "\n" + strings.Repeat("output\n", 9), false},
		{"rule with prose", "── see below for 3 more ─────────────\n>\n" + rule, false},
		{"working banner is not a border", "── ⠴ Working ─────────────────\n>\n" + rule, false},
		{"arrow only", "↑\n>\n↑", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := piComposerBoxVisible(tc.content); got != tc.want {
				t.Fatalf("piComposerBoxVisible = %v, want %v for:\n%s", got, tc.want, tc.content)
			}
		})
	}
}

// Every real idle pi capture in the status corpus shows the box too, so the
// recognizer agrees with the stock layout it generalizes. The live-turn frame
// is left out: its top border is the Working banner, and busy covers it.
func TestIssue3098_PiComposerBoxVisibleOnCorpus(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "status_corpus", "pi-*.txt"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no pi corpus frames: %v", err)
	}
	for _, p := range paths {
		if strings.Contains(p, "subagent-working") {
			continue
		}
		t.Run(filepath.Base(p), func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !piComposerBoxVisible(StripANSI(string(b))) {
				t.Fatalf("composer box not recognized in %s", p)
			}
		})
	}
}
