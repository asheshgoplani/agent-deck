package tmux

import "testing"

// Fixtures captured live from a Claude Code pane (tmux capture-pane) in the
// states that matter for the bg-work-after-Stop status:
//   - foreground turn ended with run_in_background shells still running
//   - foreground turn ended while awaiting a background agent
//   - foreground turn ended with nothing pending (the must-stay-waiting case)
//
// Newer captures (Claude Code 2.1.288, issue #2473) live in
// testdata/background_work and are exercised by background_work_2473_test.go.

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

// Issue #2473 reverses the 2026-09-23 ruling for shells and monitors: work in
// flight is activity. Every shape below that shows live background work is
// pending; a finished turn with nothing in flight is not.
func TestClaudeBackgroundWorkPending(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		// Was false before #2473 ("the shells are context"): the footer
		// counter is live, so shells still running are work in flight.
		{"shells still running (plural)", paneShellsStillRunning, true},
		{"single shell footer", paneSingleShell, true},
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

// Finite shells and awaited agents remain running; armed monitors do not.
func TestClaudeBackgroundShellsAreRunning(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"shells still running (plural)", paneShellsStillRunning, true},
		{"single shell footer", paneSingleShell, true},
		{"monitor still running", "⏺ done\n✻ Baked for 13s · done 2:02 PM · 1 monitor still running\n───\n❯ plepa\n───\n  ⏵⏵ auto mode on · 1 monitor", false},
		{"awaiting background agent only", paneAwaitingAgent, true},
		{"idle, nothing pending", paneIdleNoBackground, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseClaudeBackgroundWork(c.content).Running(); got != c.want {
				t.Fatalf("ParseClaudeBackgroundWork(%s).Running() = %v, want %v", c.name, got, c.want)
			}
		})
	}
	if got := ClassifyPaneFrame("claude", paneShellsStillRunning); got != FrameActive {
		t.Errorf("frame with background shells at the prompt = %s, want active (#2473)", got)
	}
	s := &Session{detectedTool: "claude"}
	if got := s.classifySubstate(paneShellsStillRunning); got != SubstateBackgroundWork {
		t.Errorf("substate = %q, want %q", got, SubstateBackgroundWork)
	}
	if got := ParseClaudeBackgroundWork(paneShellsStillRunning); got.Kind != BackgroundKindBash || got.Task != "2 shells" {
		t.Errorf("shells work = %+v, want bash \"2 shells\"", got)
	}
	// paneSingleShell carries a live spinner line above the footer counter:
	// the spinner (foreground work) is what makes it running.
	if got := ClassifyPaneFrame("claude", paneSingleShell); got != FrameActive {
		t.Errorf("live spinner with a shell counter = %s, want active", got)
	}
	if got := ClassifyPaneFrame("claude", paneAwaitingAgent); got != FrameActive {
		t.Errorf("awaiting a background agent must stay active, got %s", got)
	}
	if got := ClassifyPaneFrame("claude", paneIdleNoBackground); got != FrameWaiting {
		t.Errorf("finished turn with nothing in flight = %s, want waiting", got)
	}
}
