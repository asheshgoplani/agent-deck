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
