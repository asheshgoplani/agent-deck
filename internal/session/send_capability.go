package session

import "strings"

var busyInputCapabilities = map[string]bool{
	"claude": true,  // Claude Code queues a submitted line during a turn.
	"codex":  false, // The guarded composer accepts after turn end.
	"pi":     false,
	"shell":  false,
}

// AcceptsInputWhileBusy reports whether a running harness queues submitted
// input for its next turn. Unknown tools stay in the durable send queue until
// they become idle, since typing into an unknown busy pane may lose input.
func AcceptsInputWhileBusy(tool string) bool {
	if accepts, known := busyInputCapabilities[strings.ToLower(tool)]; known {
		return accepts
	}
	return IsClaudeCompatible(tool)
}
