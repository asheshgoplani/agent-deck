package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestCLIStatusCandidatesRefreshesLiveHistoryAndSharesSocketRead(t *testing.T) {
	old := listStatusSessionNames
	t.Cleanup(func() { listStatusSessionNames = old })
	calls := 0
	listStatusSessionNames = func(socket string) (map[string]struct{}, error) {
		calls++
		return map[string]struct{}{"live": {}}, nil
	}
	var rows []*Instance
	for _, state := range []Status{StatusStopped, StatusError} {
		for _, name := range []string{"live", "absent"} {
			inst := &Instance{Status: state, ArchivedAt: time.Now()}
			inst.tmuxSession = tmux.ReconnectSessionLazy(name, name, "", "", "inactive")
			rows = append(rows, inst)
		}
	}
	refresh, cached := CLIStatusCandidates(rows)
	if calls != 1 || len(refresh) != 2 || len(cached) != 2 {
		t.Fatalf("socket reads=%d refresh=%d cached=%d", calls, len(refresh), len(cached))
	}
	listStatusSessionNames = func(string) (map[string]struct{}, error) {
		return nil, errors.New("inventory unavailable")
	}
	refresh, cached = CLIStatusCandidates(rows)
	if len(refresh) != 2 || len(cached) != 2 {
		t.Fatal("indeterminate error rows must refresh; stopped history remains cached")
	}
}

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

func TestCLIStatusCandidatesAbsentHistory(t *testing.T) {
	old := listStatusSessionNames
	t.Cleanup(func() { listStatusSessionNames = old })
	listStatusSessionNames = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}
	for _, status := range []Status{StatusStopped, StatusError} {
		for _, archived := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/archived=%t", status, archived), func(t *testing.T) {
				inst := &Instance{Status: status}
				if archived {
					inst.ArchivedAt = time.Now()
				}
				inst.tmuxSession = tmux.ReconnectSessionLazy("absent", "history", "", "", "inactive")
				inst.tmuxSession.SocketName = "list-history-regression-absent"
				refresh, cached := CLIStatusCandidates([]*Instance{inst})
				if len(refresh) != 0 || !cached[inst] {
					t.Fatal("absent historical row must retain cached status")
				}
			})
		}
	}
}

func TestCLIStatusCandidatesRefreshesArchivedLiveStatus(t *testing.T) {
	for _, status := range []Status{StatusWaiting, StatusRunning} {
		inst := &Instance{Status: status, ArchivedAt: time.Now()}
		inst.tmuxSession = tmux.ReconnectSessionLazy("absent", "history", "", "", "inactive")
		refresh, cached := CLIStatusCandidates([]*Instance{inst})
		if len(refresh) != 1 || cached[inst] {
			t.Fatal("archived live status must be refreshed, not preserved as terminal history")
		}
	}
}
