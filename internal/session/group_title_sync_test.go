package session

import (
	"strconv"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestGroupTitleReconcileBoundsConcurrentMutations(t *testing.T) {
	current := statedb.InstanceRow{TmuxSession: "owned-pane", GroupPath: "initial"}
	writes := 0
	_, err := reconcileGroupTitle(func() (*statedb.InstanceRow, error) { copy := current; return &copy, nil }, func(*statedb.InstanceRow) error {
		writes++
		current.GroupPath = strconv.Itoa(writes)
		return nil
	})
	if err == nil || writes != 3 {
		t.Fatalf("writes=%d err=%v; want bounded contention failure", writes, err)
	}
}

func TestGroupTitleReconcileTracksCommittedIdentity(t *testing.T) {
	current := statedb.InstanceRow{TmuxSession: "old-pane", TmuxSocketName: "old-socket", GroupPath: "group"}
	var targets []string
	applied, err := reconcileGroupTitle(func() (*statedb.InstanceRow, error) { copy := current; return &copy, nil }, func(row *statedb.InstanceRow) error {
		targets = append(targets, row.TmuxSocketName+"/"+row.TmuxSession)
		current.TmuxSession = "new-pane"
		current.TmuxSocketName = "new-socket"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != "old-socket/old-pane" || targets[1] != "new-socket/new-pane" || applied.TmuxSession != "new-pane" {
		t.Fatalf("targets=%v applied=%+v", targets, applied)
	}
}
