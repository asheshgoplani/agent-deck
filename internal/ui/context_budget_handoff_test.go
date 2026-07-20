package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestIsAutonomousSession(t *testing.T) {
	cases := []struct {
		name string
		inst *session.Instance
		want bool
	}{
		{"conductor flag", &session.Instance{IsConductor: true}, true},
		{"conductor group", &session.Instance{GroupPath: "conductor"}, true},
		{"parented child", &session.Instance{ParentSessionID: "parent-1"}, true},
		{"plain interactive", &session.Instance{GroupPath: "my-sessions"}, false},
	}
	for _, tc := range cases {
		if got := isAutonomousSession(tc.inst); got != tc.want {
			t.Errorf("%s: isAutonomousSession = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The effective target decides which spawn path runs: same tool forks (which
// inherits everything), a different tool must go through the create path. An
// unset or invalid target must fall back to the source's own tool rather than
// silently spawning a shell.
func TestEffectiveHandoffTargetTool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instTool string
		target   string
		want     string
	}{
		{name: "unset keeps source tool", instTool: "claude", target: "", want: "claude"},
		{name: "same tool keeps source tool", instTool: "claude", target: "claude", want: "claude"},
		{name: "different tool switches", instTool: "claude", target: "codex", want: "codex"},
		{name: "case-insensitive same tool", instTool: "claude", target: "Claude", want: "claude"},
		{name: "invalid target falls back to source", instTool: "claude", target: "codex --yolo", want: "claude"},
		{name: "unknown target falls back to source", instTool: "claude", target: "nope-not-a-tool", want: "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := session.ContextBudgetSettings{HandoffTargetTool: tc.target}
			got := effectiveHandoffTargetTool(&session.Instance{Tool: tc.instTool}, cfg)
			if got != tc.want {
				t.Errorf("effectiveHandoffTargetTool = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandoffAgentIdle(t *testing.T) {
	if !handoffAgentIdle(&session.Instance{Status: session.StatusWaiting}) {
		t.Errorf("waiting session should be idle")
	}
	if !handoffAgentIdle(&session.Instance{Status: session.StatusIdle}) {
		t.Errorf("idle session should be idle")
	}
	if handoffAgentIdle(&session.Instance{Status: session.StatusRunning}) {
		t.Errorf("running session should not be idle")
	}
}
