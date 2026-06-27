package session

import (
	"testing"
	"time"
)

func handoffCfg() ContextBudgetSettings {
	return (&UserConfig{}).GetContextBudget() // high=200000, ceiling=250000, timeout=300s
}

func TestNextHandoffState_Table(t *testing.T) {
	cfg := handoffCfg()
	base := time.Unix(1_000_000, 0)
	trig := base.Add(-10 * time.Second) // 10s into wrap

	cases := []struct {
		name       string
		cur        HandoffState
		in         HandoffInputs
		wantNext   HandoffState
		wantAction HandoffAction
	}{
		{"normal stays normal below high", HandoffNormal,
			HandoffInputs{Tokens: 199999, Now: base}, HandoffNormal, ActionNone},
		{"normal triggers wrap at high", HandoffNormal,
			HandoffInputs{Tokens: 200000, Now: base}, HandoffWrapRequested, ActionRequestWrap},
		{"wrap waits when not ready", HandoffWrapRequested,
			HandoffInputs{Tokens: 210000, Now: base, TriggeredAt: trig}, HandoffWaiting, ActionNone},
		{"waiting forks when ready+idle", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: true, AgentIdle: true, Now: base, TriggeredAt: trig},
			HandoffDone, ActionFork},
		{"waiting holds when ready but busy", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: true, AgentIdle: false, Now: base, TriggeredAt: trig},
			HandoffWaiting, ActionNone},
		{"ceiling crossed -> failsafe", HandoffWaiting,
			HandoffInputs{Tokens: 250000, PromptReady: false, Now: base, TriggeredAt: trig},
			HandoffFailsafe, ActionFailsafe},
		{"timeout -> failsafe", HandoffWaiting,
			HandoffInputs{Tokens: 210000, PromptReady: false, Now: base, TriggeredAt: base.Add(-301 * time.Second)},
			HandoffFailsafe, ActionFailsafe},
		{"done is terminal", HandoffDone,
			HandoffInputs{Tokens: 300000, Now: base}, HandoffDone, ActionNone},
		{"failsafe is terminal", HandoffFailsafe,
			HandoffInputs{Tokens: 300000, Now: base}, HandoffFailsafe, ActionNone},
	}
	for _, tc := range cases {
		got := NextHandoffState(tc.cur, tc.in, cfg)
		if got.Next != tc.wantNext || got.Action != tc.wantAction {
			t.Errorf("%s: NextHandoffState = {%v,%v}, want {%v,%v}",
				tc.name, got.Next, got.Action, tc.wantNext, tc.wantAction)
		}
	}
}
