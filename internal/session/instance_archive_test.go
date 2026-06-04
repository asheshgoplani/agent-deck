package session

import (
	"testing"
	"time"
)

func TestIsArchived(t *testing.T) {
	var inst Instance
	if inst.IsArchived() {
		t.Fatal("zero ArchivedAt must report not archived")
	}
	inst.ArchivedAt = time.Now()
	if !inst.IsArchived() {
		t.Fatal("non-zero ArchivedAt must report archived")
	}
	inst.ArchivedAt = time.Time{}
	if inst.IsArchived() {
		t.Fatal("cleared ArchivedAt must report not archived again")
	}
}

func TestSortInstancesByActionable_ArchivedSinkToBottom(t *testing.T) {
	now := time.Now()
	archived := &Instance{ID: "arch", Status: StatusWaiting, ArchivedAt: now} // high priority status...
	idle := &Instance{ID: "idle", Status: StatusIdle}
	running := &Instance{ID: "run", Status: StatusRunning}

	insts := []*Instance{archived, idle, running}
	SortInstancesByActionable(insts)

	// Archived must be last even though StatusWaiting normally sorts near the top.
	if insts[len(insts)-1].ID != "arch" {
		got := make([]string, len(insts))
		for i, in := range insts {
			got[i] = in.ID
		}
		t.Fatalf("archived session must sort to the bottom; got order %v", got)
	}
}
