package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

func TestArchivedAtStorageRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	storage := &Storage{db: db, dbPath: dbPath, profile: "_test"}
	archivedAt := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	inst := &Instance{
		ID:          "arch-store",
		Title:       "Archived",
		ProjectPath: "/tmp",
		GroupPath:   "grp",
		Tool:        "shell",
		Status:      StatusStopped,
		CreatedAt:   time.Now(),
		ArchivedAt:  archivedAt,
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d instances, want 1", len(loaded))
	}
	if !loaded[0].IsArchived() {
		t.Fatal("expected archived instance after reload")
	}
	if !loaded[0].ArchivedAt.Equal(archivedAt) {
		t.Errorf("ArchivedAt: got %v want %v", loaded[0].ArchivedAt, archivedAt)
	}
	arch := FilterInstancesByArchive(loaded, true)
	if len(arch) != 1 || arch[0].ID != "arch-store" {
		t.Fatalf("archived filter after reload: got %+v", ids(arch))
	}
}
