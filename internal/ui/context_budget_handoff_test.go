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
