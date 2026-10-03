package tmux

import "testing"

// Fixtures captured live from a Claude Code pane (tmux capture-pane) in the
// three states that matter for the bg-work-after-Stop false-yellow bug:
//   - foreground turn ended with run_in_background shells still running
//   - foreground turn ended while awaiting a background agent
//   - foreground turn ended with nothing pending (the must-stay-waiting case)

const paneShellsStillRunning = `⏺ Decision noted: default-on for everyone.

✻ Churned for 6m 24s · 2 shells still running
                                                                           123608 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 123.1k  ⎇ feat/context-budget-handoff  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 2 shells · ← for agents`

const paneSingleShell = `  ⏺ I have a background task running.

✳ Meandering… (28s · ↓ 1.5k tokens)
  ⎿  Tip: Use /feedback to help us improve!
                                                                            90874 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 90.9k  ⎇ feat/context-budget-handoff  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

const paneAwaitingAgent = `⏺ Probe launched. Ending my turn.

✻ Waiting for 1 background agent to finish

⏺ main
  ◯ Explore  Long idle agent for probe                               15s · ↑ 13.9k tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 136.9k  ⎇ feat/context-budget-handoff  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

const paneIdleNoBackground = `⏺ All done — your tests pass.

✻ Churned for 1m 2s
                                                                            42100 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 42.1k  ⎇ feat/context-budget-handoff  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`

// A completed (frozen) agent row plus a no-background footer must NOT count as
// pending: the row "◯ Explore ... 5s" persists after the agent finishes, so it
// is unreliable and we rely on the footer/completion line instead.
const paneCompletedAgentRowNoBackground = `⏺ Here are the results.

✻ Churned for 12s
                                                                            42100 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 42.1k  ⎇ feat/context-budget-handoff  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents

  ⏺ main
  ◯ Explore  Idle probe agent                                         5s · ↓ 14.2k tokens`

func TestClaudeBackgroundWorkPending(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		// Background shells at the prompt are NOT pending work: the turn is
		// done and the operator can act (status-detection audit 2026-09-23).
		{"shells still running (plural)", paneShellsStillRunning, false},
		{"single shell footer", paneSingleShell, false},
		{"awaiting background agent", paneAwaitingAgent, true},
		{"idle, nothing pending", paneIdleNoBackground, false},
		{"completed agent row, no background", paneCompletedAgentRowNoBackground, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeBackgroundWorkPending(c.content); got != c.want {
				t.Fatalf("claudeBackgroundWorkPending(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// Prose far up the scrollback that merely mentions shells must not trip the
// detector — only the tail (completion line + footer) is authoritative.
func TestClaudeBackgroundWorkPending_IgnoresScrollbackProse(t *testing.T) {
	prose := "I launched 3 shells still running earlier in the session.\n"
	var content string
	for i := 0; i < 40; i++ {
		content += "line of unrelated transcript output here\n"
	}
	content = prose + content + `❯
   Model: Opus 4.8  Ctx: 42.1k
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`
	if claudeBackgroundWorkPending(content) {
		t.Fatal("scrollback prose mentioning shells must not be detected as pending background work")
	}
}

// Shells and monitors left alive at the prompt are reported as
// SubstateBackgroundWork, never as running: on the 2026-09-23 audit every
// remote row shown green on the controller was one of these, hours after the
// worker had printed its completion sentinel.
func TestClaudeBackgroundShellsPending(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"shells still running (plural)", paneShellsStillRunning, true},
		{"single shell footer", paneSingleShell, true},
		{"monitor still running", "⏺ done\n✻ Baked for 13s · done 2:02 PM · 1 monitor still running\n───\n❯ plepa\n───\n  ⏵⏵ auto mode on · 1 monitor", true},
		{"awaiting background agent only", paneAwaitingAgent, true}, // footer still shows · 1 shell ·
		{"idle, nothing pending", paneIdleNoBackground, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeBackgroundShellsPending(c.content); got != c.want {
				t.Fatalf("claudeBackgroundShellsPending(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
	if got := ClassifyPaneFrame("claude", paneShellsStillRunning); got != FrameWaiting {
		t.Errorf("frame with background shells at the prompt = %s, want waiting", got)
	}
	s := &Session{detectedTool: "claude"}
	if got := s.classifySubstate(paneShellsStillRunning); got != SubstateBackgroundWork {
		t.Errorf("substate = %q, want %q", got, SubstateBackgroundWork)
	}
	// paneSingleShell carries a live spinner line above the footer counter:
	// the shell counter is context, the spinner is what makes it running.
	if got := ClassifyPaneFrame("claude", paneSingleShell); got != FrameActive {
		t.Errorf("live spinner with a shell counter = %s, want active", got)
	}
	if got := ClassifyPaneFrame("claude", paneAwaitingAgent); got != FrameActive {
		t.Errorf("awaiting a background agent must stay active, got %s", got)
	}
}

const paneWorkflowRunning = `⏺ Starting the multi-agent workflow.

  Workflow(name: "comms-followon-round3")
  Running in background · /workflows to monitor
                                                                            90874 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 90.9k  ⎇ feat/comms  (+0,-0)  𖠰 main
  ○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens`

const paneBackgroundBashRunning = `⏺ Running build in background.

  Bash(command: "cargo test")
  Running in background · /tasks to monitor
                                                                            50123 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 50.1k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

const paneWorkflowCompleted = `⏺ Workflow completed successfully.

  ===AGENTDECK_DONE=== status=ok summary=Workflow finished
  ✻ Crunched for 20m 10s · done 11:45 AM
                                                                            95000 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 95.0k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`

func TestClaudeBackgroundWork_WorkflowAndBash(t *testing.T) {
	// 1. Workflow in flight: running + background-work
	if !claudeBackgroundWorkPending(paneWorkflowRunning) {
		t.Fatal("workflow footer must be detected as pending background work")
	}
	if got := ClassifyPaneFrame("claude", paneWorkflowRunning); got != FrameActive {
		t.Fatalf("workflow running frame = %s, want active", got)
	}
	s := &Session{detectedTool: "claude"}
	if got := s.classifySubstate(paneWorkflowRunning); got != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", got, SubstateBackgroundWork)
	}
	detector := NewPromptDetector("claude")
	detail := detector.SubstateDetail(paneWorkflowRunning)
	if want := "comms-followon-round3 3/5 · 18m32s"; detail != want {
		t.Fatalf("detail = %q, want %q", detail, want)
	}
	if task := claudeBackgroundTaskName(paneWorkflowRunning); task != "comms-followon-round3" {
		t.Fatalf("task name = %q, want comms-followon-round3", task)
	}

	// 2. Background Bash: active + background-work
	if !claudeBackgroundWorkPending(paneBackgroundBashRunning) {
		t.Fatal("background bash tool call must be detected as pending background work")
	}
	if got := ClassifyPaneFrame("claude", paneBackgroundBashRunning); got != FrameActive {
		t.Fatalf("background bash frame = %s, want active", got)
	}

	// 3. Completed workflow: not pending, frame is waiting
	if claudeBackgroundWorkPending(paneWorkflowCompleted) {
		t.Fatal("completed workflow must not be pending background work")
	}
	if got := ClassifyPaneFrame("claude", paneWorkflowCompleted); got != FrameWaiting {
		t.Fatalf("completed workflow frame = %s, want waiting", got)
	}
}

