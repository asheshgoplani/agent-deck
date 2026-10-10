package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2473: a running workflow means a running session. The fixtures in
// testdata/background_work are live tmux captures of a Claude Code 2.1.288
// session (paths scrubbed) taken while it ran a two-agent Workflow, then a
// background Agent plus a background Bash:
//
//	workflow-running.txt                 workflow row 1/2 under the footer, "Waiting for 1 dynamic workflow"
//	workflow-finished.txt                the same row left at 2/2 after the notification turn
//	agent-and-shells.txt                 turn done, "· 2 shells, 1 monitor ·" footer, agent roster
//	monitor-footer-busy-turn.txt         a foreground turn running while a monitor is alive
//	all-finished-stale-waiting-line.txt  everything reported back; an old "Waiting for 1 background
//	                                     agent" line is still 16 lines up the pane

func loadBackgroundFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "background_work", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return string(b)
}

// frameSubstate runs a frame through the same steps GetStatus/GetSubstate
// take: prepareFrame (records background work from the UNTRIMMED frame, then
// trims the roster) and classifyFrameLocked.
func frameSubstate(content string) (Substate, string, BackgroundWork) {
	s := &Session{detectedTool: "claude"}
	s.mu.Lock()
	defer s.mu.Unlock()
	trimmed := s.prepareFrame(StripANSI(content))
	sub := s.classifyFrameLocked(trimmed)
	return sub, s.lastSubstateDetail, s.lastBackgroundWork
}

// issueScreenshotFrame rebuilds the frame from the #2473 screenshot: an empty
// prompt, the Workflow launch receipt above it, and the exact footer row.
const issueScreenshotFrame = `⏺ Workflow(Run the comms follow-on round 3)
  ⎿  Running in background · /workflows to monitor

⏺ Launched comms-followon-round3 in the background.

────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────
  [p] u@host:/x | [Opus 5.5] ctx:41%
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents

  ○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens`

func TestBackgroundWork_WorkflowRowVariants(t *testing.T) {
	cases := []struct {
		name      string
		row       string
		inFlight  bool
		task      string
		step, all int
		elapsed   string
	}{
		{"issue screenshot ○", "  ○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens", true, "comms-followon-round3", 3, 5, "18m32s"},
		{"live capture ◯", "  ◯ probe-two-agents  ▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱  1/2 · 22s · ↓ 73.3k tokens", true, "probe-two-agents", 1, 2, "22s"},
		{"spinner ◐", "  ◐ comms-followon-round3  ▰▰▰▱▱  3/5 · 18m32s · ↓ 784.8k tokens", true, "comms-followon-round3", 3, 5, "18m32s"},
		{"spinner ●", "  ● comms-followon-round3  ▰▰▰▱▱  3/5 · 18m32s · ↓ 784.8k tokens", true, "comms-followon-round3", 3, 5, "18m32s"},
		{"no tokens segment", "  ○ comms-followon-round3  ▰▰▰▱▱  3/5 · 18m32s", true, "comms-followon-round3", 3, 5, "18m32s"},
		{"no bar", "  ○ comms-followon-round3  0/5 · 4s", true, "comms-followon-round3", 0, 5, "4s"},
		{"hours", "  ◑ nightly-sweep  ▰▱  1/9 · 1h02m · ↓ 2.1M tokens", true, "nightly-sweep", 1, 9, "1h02m"},
		{"finished n/n lingers", "  ◯ probe-two-agents  ▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰  2/2 · 1m44s · ↓ 74.4k tokens", false, "", 0, 0, ""},
		{"agent roster row (no n/m)", "  ◯ general-purpose  probe sleeper agent      16s · ↓ 35.9k tokens", false, "", 0, 0, ""},
		{"assistant prose with n/m", "⏺ Step 3/5 · 18m32s done for comms-followon-round3", false, "", 0, 0, ""},
		{"effort header glyph", "                    ◐ medium · /effort", false, "", 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := "────\n❯ \n────\n  ⏵⏵ bypass permissions on · ← for agents\n\n" + c.row
			got := ParseClaudeBackgroundWork(frame)
			if got.InFlight() != c.inFlight {
				t.Fatalf("InFlight = %v (%+v), want %v", got.InFlight(), got, c.inFlight)
			}
			if !c.inFlight {
				return
			}
			if got.Kind != BackgroundKindWorkflow || got.Task != c.task || got.Step != c.step || got.Steps != c.all || got.Elapsed != c.elapsed {
				t.Fatalf("parsed %+v, want workflow %s %d/%d · %s", got, c.task, c.step, c.all, c.elapsed)
			}
		})
	}
}

// The screenshot frame: running + background-work with the task, n/m and
// elapsed in the detail — the exact thing #2473 reported as "○ idle".
func TestBackgroundWork_IssueScreenshotFrameIsRunning(t *testing.T) {
	if got := ClassifyPaneFrame("claude", issueScreenshotFrame); got != FrameActive {
		t.Fatalf("frame verdict = %s, want active", got)
	}
	sub, detail, work := frameSubstate(issueScreenshotFrame)
	if sub != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", sub, SubstateBackgroundWork)
	}
	if detail != "workflow comms-followon-round3 3/5 · 18m32s" {
		t.Fatalf("detail = %q", detail)
	}
	if work.Task != "comms-followon-round3" || work.Step != 3 || work.Steps != 5 || work.Elapsed != "18m32s" {
		t.Fatalf("work = %+v", work)
	}
	// The row is drawn with a roster glyph under the footer, so the roster
	// trim removes it: the frame the other detectors read no longer has it.
	// The verdict survives because it is taken before the trim.
	if strings.Contains(trimClaudeTrailingRoster(strings.ReplaceAll(issueScreenshotFrame, "○", "◯")), "3/5 · 18m32s") {
		t.Fatal("expected the roster trim to drop the workflow row (precondition of this test)")
	}
	_, _, work = frameSubstate(strings.ReplaceAll(issueScreenshotFrame, "○", "◯"))
	if !work.InFlight() {
		t.Fatal("◯ row trimmed away before detection: background work lost")
	}
}

func TestBackgroundWork_LiveFixtures(t *testing.T) {
	cases := []struct {
		file    string
		frame   FrameVerdict
		sub     Substate
		kind    string
		summary string
	}{
		{"workflow-running.txt", FrameActive, SubstateBackgroundWork, BackgroundKindWorkflow, "workflow probe-two-agents 1/2 · 22s"},
		{"workflow-finished.txt", FrameWaiting, SubstateIdleAtEmptyPrompt, "", ""},
		{"agent-and-shells.txt", FrameActive, SubstateBackgroundWork, BackgroundKindBash, "bash 2 shells, 1 monitor"},
		// A live spinner is foreground work: running, not background-work.
		{"monitor-footer-busy-turn.txt", FrameActive, SubstateRunning, BackgroundKindMonitor, "1 watching · 1 monitor"},
		// The old 20-line scan matched the stale "Waiting for 1 background
		// agent" line here and kept a finished session green.
		{"all-finished-stale-waiting-line.txt", FrameWaiting, SubstateIdleAtEmptyPrompt, "", ""},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			content := loadBackgroundFixture(t, c.file)
			if got := ClassifyPaneFrame("claude", content); got != c.frame {
				t.Errorf("frame verdict = %s, want %s", got, c.frame)
			}
			sub, _, work := frameSubstate(content)
			if sub != c.sub {
				t.Errorf("substate = %q, want %q", sub, c.sub)
			}
			if work.Kind != c.kind || work.Summary() != c.summary {
				t.Errorf("work = %+v (%q), want kind %q summary %q", work, work.Summary(), c.kind, c.summary)
			}
		})
	}
}

// The awaited line counts only as the CURRENT last turn line above the box.
func TestBackgroundWork_AwaitedLineMustBeCurrent(t *testing.T) {
	running := loadBackgroundFixture(t, "workflow-running.txt")
	// Cut the footer and the workflow row (a capture that missed the bottom of
	// the pane): the Waiting line still proves the hand-off.
	idx := strings.Index(running, "  [p] u@host")
	partial := running[:idx]
	got := ParseClaudeBackgroundWork(partial)
	if got.Kind != BackgroundKindWorkflow || got.Task != "1 dynamic workflow" {
		t.Fatalf("partial frame work = %+v, want the awaited workflow", got)
	}
	// Redrawn without the Waiting line and without the footer: the pane alone
	// shows nothing (the session layer's transcript scan covers this frame).
	redrawn := strings.Replace(partial, "✻ Waiting for 1 dynamic workflow to finish", "", 1)
	if w := ParseClaudeBackgroundWork(redrawn); w.InFlight() {
		t.Fatalf("redrawn frame = %+v, want nothing in flight from the pane", w)
	}
}

// The footer counter is only read on the footer line itself.
func TestBackgroundWork_CounterOnlyOnFooter(t *testing.T) {
	prose := "⏺ I left · 2 shells · running for the dev server.\n\n✻ Brewed for 3s\n────\n❯ \n────\n  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"
	if w := ParseClaudeBackgroundWork(prose); w.InFlight() {
		t.Fatalf("counter in prose = %+v, want nothing in flight", w)
	}
	footer := "✻ Brewed for 3s\n────\n❯ \n────\n  ⏵⏵ bypass permissions on · 1 shell, 2 monitors · ← for agents"
	if w := ParseClaudeBackgroundWork(footer); w.Kind != BackgroundKindBash || w.Task != "1 shell, 2 monitors" {
		t.Fatalf("footer counter = %+v", w)
	}
	monitors := "✻ Brewed for 3s\n────\n❯ \n────\n  ⏵⏵ auto mode on · 2 monitors"
	if w := ParseClaudeBackgroundWork(monitors); w.Kind != BackgroundKindMonitor {
		t.Fatalf("monitor counter = %+v", w)
	}
}

// A hook that still says running over a frame with work in flight is not
// lagging: the completed-turn sample must not count it.
func TestBackgroundWork_CompletedTurnSampleExcludesWorkInFlight(t *testing.T) {
	s := &Session{detectedTool: "claude"}
	s.mu.Lock()
	defer s.mu.Unlock()
	trimmed := s.prepareFrame(loadBackgroundFixture(t, "workflow-running.txt"))
	s.classifyFrameLocked(trimmed)
	if s.completedTurnIdle {
		t.Fatal("a frame with a workflow in flight was sampled as a completed turn at an idle prompt")
	}
}
