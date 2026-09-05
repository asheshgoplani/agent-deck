package session

import (
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"testing"
)

// Includes local durable evidence/coordination overhead; excludes the unchanged
// tmux subprocess latency so the additional classification cost is visible.
func BenchmarkIssue2091UnknownExit(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	b.Setenv("XDG_CONFIG_HOME", "")
	b.Setenv("XDG_DATA_HOME", "")
	i := &Instance{ID: "issue2091-benchmark", Tool: "codex", Status: StatusWaiting, tmuxSession: &tmux.Session{Name: "missing"}, paneDeadExitStatusForTest: func() (int, bool) { return 0, false }}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		i.mu.Lock()
		i.Status = StatusWaiting
		i.applyTerminatedPaneStatus()
		i.mu.Unlock()
	}
}
