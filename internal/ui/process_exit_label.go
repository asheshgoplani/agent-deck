package ui

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func processExitLabel(status string, substate session.Substate, code *int) string {
	if substate != session.SubstateProcessExited {
		return status
	}
	if code == nil {
		return status + " · process exited"
	}
	return fmt.Sprintf("%s · process exited (%d)", status, *code)
}
