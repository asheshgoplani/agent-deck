package ui

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// backgroundWorkLine renders the preview's one-line background-work detail
// (issue #2473), e.g. "background: comms-followon-round3 3/5 · 18m32s".
// "" when nothing is in flight. The kind names the work when no task name is
// known ("background: agent").
func backgroundWorkLine(work tmux.BackgroundWork) string {
	if !work.InFlight() {
		return ""
	}
	label := work.Task
	if label == "" {
		label = work.Kind
	}
	line := "background: " + label
	if work.Watching() {
		return fmt.Sprintf("watching: %d · %s", max(1, work.Count), label)
	}
	if work.Steps > 0 {
		line += fmt.Sprintf(" %d/%d", work.Step, work.Steps)
	}
	if work.Elapsed != "" {
		line += " · " + work.Elapsed
	}
	return line
}
