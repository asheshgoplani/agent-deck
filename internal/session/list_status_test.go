package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestCLIStatusCandidatesStoppedBudget(t *testing.T) {
	for _, tc := range []struct {
		count int
		limit time.Duration
	}{{300, 300 * time.Millisecond}, {1000, time.Second}} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			instances := make([]*Instance, tc.count)
			for i := range instances {
				inst := &Instance{Title: fmt.Sprintf("stopped-%d", i), Status: StatusStopped}
				inst.tmuxSession = tmux.ReconnectSessionLazy(fmt.Sprintf("absent-%d", i), inst.Title, inst.ProjectPath, "", "inactive")
				inst.tmuxSession.SocketName = "list-fast-budget-absent"
				instances[i] = inst
			}
			start := time.Now()
			refresh, cached := CLIStatusCandidates(instances)
			elapsed := time.Since(start)
			if len(refresh) != 0 || len(cached) != tc.count {
				t.Fatalf("refresh=%d cached=%d, want 0 and %d", len(refresh), len(cached), tc.count)
			}
			if elapsed >= tc.limit {
				t.Fatalf("%d stopped rows took %s, limit %s", tc.count, elapsed, tc.limit)
			}
		})
	}
}
