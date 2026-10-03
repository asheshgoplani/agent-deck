package tmux

import "testing"

// Issue #2473, frames grafted from the duplicate PR #2480. They are synthetic
// (not live captures) but use a layout the live fixtures do not: a custom
// statusline ("Model: … Ctx: … ⎇ branch") in place of the [profile] line, a
// token counter drawn above the input box, and, for the Bash case, a Claude
// footer with only the shell counter. The detector must read the workflow row
// and the counter the same way under that layout, and a finished turn whose
// row is gone must settle.
const (
	pr2480WorkflowRunning = `⏺ Starting the multi-agent workflow.

  Workflow(name: "comms-followon-round3")
  Running in background · /workflows to monitor
                                                                            90874 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 90.9k  ⎇ feat/comms  (+0,-0)  𖠰 main
  ○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens`

	pr2480BashRunning = `⏺ Running build in background.

  Bash(command: "cargo test")
  Running in background · /tasks to monitor
                                                                            50123 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 50.1k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

	pr2480WorkflowCompleted = `⏺ Workflow completed successfully.

  ===AGENTDECK_DONE=== status=ok summary=Workflow finished
  ✻ Crunched for 20m 10s · done 11:45 AM
                                                                            95000 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 95.0k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`
)

func TestBackgroundWork2473_CustomStatuslineFrames(t *testing.T) {
	cases := []struct {
		name    string
		content string
		frame   FrameVerdict
		sub     Substate
		summary string
	}{
		{"workflow row under a custom statusline", pr2480WorkflowRunning, FrameActive, SubstateBackgroundWork, "workflow comms-followon-round3 3/5 · 18m32s"},
		{"shell counter under a custom statusline", pr2480BashRunning, FrameActive, SubstateBackgroundWork, "bash 1 shell"},
		{"finished turn, row gone", pr2480WorkflowCompleted, FrameWaiting, SubstateIdleAtEmptyPrompt, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyPaneFrame("claude", c.content); got != c.frame {
				t.Errorf("frame verdict = %s, want %s", got, c.frame)
			}
			sub, _, work := frameSubstate(c.content)
			if sub != c.sub {
				t.Errorf("substate = %q, want %q", sub, c.sub)
			}
			if work.Summary() != c.summary {
				t.Errorf("work = %+v (%q), want %q", work, work.Summary(), c.summary)
			}
		})
	}
}

// #2480 also treated the launch receipt "Running in background · /workflows
// to monitor" on its own as work in flight. #2479 deliberately does not: the
// receipt sits in the transcript area above the turn summary, not in the
// footer Claude redraws, so it is the same scrollback-prose class the
// 2026-09-23 audit rejected (it can outlive the work it names, e.g. a task
// stopped from /workflows). A redrawn pane that lost the row and the Waiting
// line is covered by the session layer's transcript hold instead
// (TestBackgroundWork2473_TranscriptHoldsRedrawnPane). Pinned so the choice
// is visible.
func TestBackgroundWork2473_LaunchReceiptAloneIsNotEvidence(t *testing.T) {
	receiptOnly := `⏺ Workflow(Two parallel agents each run sleep 60 via Bash and return done)
  ⎿  Running in background · /workflows to monitor

⏺ Launched probe-two-agents.

✻ Brewed for 4s

────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`
	if w := ParseClaudeBackgroundWork(receiptOnly); w.InFlight() {
		t.Fatalf("launch receipt alone = %+v, want nothing in flight from the pane", w)
	}
}
