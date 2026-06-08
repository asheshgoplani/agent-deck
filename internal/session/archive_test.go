package session

import (
	"testing"
	"time"
)

func TestFilterInstancesByArchive(t *testing.T) {
	archived := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	instances := []*Instance{
		{ID: "a", ArchivedAt: time.Time{}},
		{ID: "b", ArchivedAt: archived},
		nil,
		{ID: "c", ArchivedAt: archived},
	}

	active := FilterInstancesByArchive(instances, false)
	if len(active) != 1 || active[0].ID != "a" {
		t.Fatalf("active filter: got %+v, want [a]", ids(active))
	}

	arch := FilterInstancesByArchive(instances, true)
	if len(arch) != 2 || arch[0].ID != "b" || arch[1].ID != "c" {
		t.Fatalf("archived filter: got %+v, want [b c]", ids(arch))
	}
}

func TestIsArchived(t *testing.T) {
	var nilInst *Instance
	if nilInst.IsArchived() {
		t.Fatal("nil instance should not be archived")
	}
	if (&Instance{}).IsArchived() {
		t.Fatal("zero ArchivedAt should not be archived")
	}
	if !(&Instance{ArchivedAt: time.Now()}).IsArchived() {
		t.Fatal("non-zero ArchivedAt should be archived")
	}
}

func ids(insts []*Instance) []string {
	out := make([]string, 0, len(insts))
	for _, i := range insts {
		if i != nil {
			out = append(out, i.ID)
		}
	}
	return out
}
