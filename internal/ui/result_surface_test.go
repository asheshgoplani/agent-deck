package ui

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestTUIResultSurfaceSemanticParity(t *testing.T) {
	cases := []session.SessionResult{
		{State: session.ResultStateKnown, Result: json.RawMessage(`{"value":"known"}`), Verdict: "PASS"},
		session.UnknownSessionResult(session.ResultIdentity{SessionID: "s", TurnID: "new-turn"}),
	}
	for _, result := range cases {
		if got, want := tuiSessionResultText(result), session.FormatSessionResult(result); got != want {
			t.Fatalf("TUI=%q CLI semantic=%q for %+v", got, want, result)
		}
	}
}
