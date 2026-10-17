package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestProcessExitLabel(t *testing.T) {
	code0, code3 := 0, 3
	for _, tc := range []struct {
		status   string
		substate session.Substate
		code     *int
		want     string
	}{
		{"running", session.SubstateNone, nil, "running"},
		{"idle", session.SubstateProcessExited, &code0, "idle · process exited (0)"},
		{"error", session.SubstateProcessExited, &code3, "error · process exited (3)"},
		{"error", session.SubstateProcessExited, nil, "error · process exited"},
	} {
		if got := processExitLabel(tc.status, tc.substate, tc.code); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}
