package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const paneWorkflowRunningFixture = `⏺ Starting the multi-agent workflow.

  Workflow(name: "comms-followon-round3")
  Running in background · /workflows to monitor
                                                                            90874 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 90.9k  ⎇ feat/comms  (+0,-0)  𖠰 main
  ○ comms-followon-round3  ▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱  3/5 · 18m32s · ↓ 784.8k tokens`

const paneBackgroundBashFixture = `⏺ Running build in background.

  Bash(command: "cargo test")
  Running in background · /tasks to monitor
                                                                            50123 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 50.1k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

const paneBackgroundAgentFixture = `⏺ Probe launched. Ending my turn.

✻ Waiting for 1 background agent to finish

⏺ main
  ◯ Explore  Long idle agent for probe                               15s · ↑ 13.9k tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 136.9k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on · 1 shell · ← for agents`

const paneWorkflowCompletedFixture = `⏺ Workflow completed successfully.

  ===AGENTDECK_DONE=== status=ok summary=Workflow finished
  ✻ Crunched for 20m 10s · done 11:45 AM
                                                                            95000 tokens
─────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────
   Model: Opus 4.8  Ctx: 95.0k  ⎇ main  (+0,-0)  𖠰 main
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents`

func TestIssue2473_WorkflowRunningStatusAndSubstate(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "workflow-running", paneWorkflowRunningFixture)
	defer cleanup()

	inst.tmuxSession.Command = "claude"
	tmux.ExpireStartupWindowForTest(t, inst.tmuxSession)
	status, sub := cliPass(t, inst)
	if status != StatusRunning {
		t.Fatalf("status = %q, want running while workflow is in flight", status)
	}
	if sub != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", sub, SubstateBackgroundWork)
	}
	if detail := inst.SubstateDetail(); detail != "comms-followon-round3 3/5 · 18m32s" {
		t.Fatalf("substate detail = %q, want 'comms-followon-round3 3/5 · 18m32s'", detail)
	}
	if task := inst.BackgroundTaskName(); task != "comms-followon-round3" {
		t.Fatalf("task name = %q, want 'comms-followon-round3'", task)
	}
}

func TestIssue2473_WorkflowCompletedTransitionsWaitingThenIdle(t *testing.T) {
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte(paneWorkflowRunningFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("audit-wf-trans", tmpHome, "claude")
	inst.Command = "claude"
	cmd := fmt.Sprintf("sh -c 'cat %q; exec bash'", panePath)
	if err := inst.tmuxSession.Start(cmd); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	defer func() { _ = inst.tmuxSession.Kill() }()
	time.Sleep(2 * time.Second)

	// Step 1: while running, status is running + background-work
	inst.tmuxSession.Command = "claude"
	tmux.ExpireStartupWindowForTest(t, inst.tmuxSession)
	status, sub := cliPass(t, inst)
	if status != StatusRunning {
		t.Fatalf("step 1 status = %q, want running", status)
	}
	if sub != SubstateBackgroundWork {
		t.Fatalf("step 1 substate = %q, want background-work", sub)
	}

	// Step 2: workflow completes, update pane fixture
	if err := os.WriteFile(panePath, []byte(paneWorkflowCompletedFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = inst.tmuxSession.SendKeysAndEnter(fmt.Sprintf("clear; cat %q", panePath))
	time.Sleep(2500 * time.Millisecond)

	// Within a few polls (prompt-no-busy hysteresis + debounce), status returns to waiting
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _ = cliPass(t, inst)
		if status == StatusWaiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("step 2 status = %q, want waiting after workflow finished", status)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Step 3: user acknowledges
	inst.tmuxSession.Acknowledge()
	status, _ = cliPass(t, inst)
	if status != StatusIdle {
		t.Fatalf("step 3 status = %q, want idle after acknowledgement", status)
	}
}

func TestIssue2473_BackgroundBashFixtureIsRunning(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "bg-bash", paneBackgroundBashFixture)
	defer cleanup()

	inst.tmuxSession.Command = "claude"
	tmux.ExpireStartupWindowForTest(t, inst.tmuxSession)
	status, sub := cliPass(t, inst)
	if status != StatusRunning {
		t.Fatalf("status = %q, want running for background bash", status)
	}
	if sub != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", sub, SubstateBackgroundWork)
	}
}

func TestIssue2473_BackgroundAgentFixtureIsRunning(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "bg-agent", paneBackgroundAgentFixture)
	defer cleanup()

	inst.tmuxSession.Command = "claude"
	tmux.ExpireStartupWindowForTest(t, inst.tmuxSession)
	status, sub := cliPass(t, inst)
	if status != StatusRunning {
		t.Fatalf("status = %q, want running for awaited background agent", status)
	}
	if sub != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", sub, SubstateBackgroundWork)
	}
}

func TestIssue2473_TranscriptWorkflowInFlight(t *testing.T) {
	lines := []string{
		fxHuman("u0", "run two trivial agents"),
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"tool_use","name":"Workflow","id":"wf1","input":{"name":"two-trivial-agents"}}]}}`,
		`{"type":"user","uuid":"u1","message":{"content":[{"type":"tool_result","tool_use_id":"wf1","content":"Running in background · /workflows to monitor"}]}}`,
	}
	inFlight, name := classifyTranscriptBackgroundWork(lines)
	if !inFlight || name != "two-trivial-agents" {
		t.Fatalf("classifyTranscriptBackgroundWork = (%v, %q), want (true, 'two-trivial-agents')", inFlight, name)
	}

	// Now append completion notification
	lines = append(lines,
		fxTaskNotification("u2"),
		fxAssistantText("a2", "Workflow two-trivial-agents finished."),
	)
	inFlight, name = classifyTranscriptBackgroundWork(lines)
	if inFlight {
		t.Fatalf("after completed notification, inFlight must be false, got %v (%q)", inFlight, name)
	}
}
