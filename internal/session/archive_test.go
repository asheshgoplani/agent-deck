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

// TestArchiveState_ReadsAndWritesUnderTheInstanceLock: background commands
// archive and unarchive a session while the TUI renders and filters it and
// the watcher looks its conductor up. Run with -race: every read of the
// archive state takes the instance lock its writers take.
func TestArchiveState_ReadsAndWritesUnderTheInstanceLock(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "s1"}
	list := []*Instance{inst}
	stop := make(chan struct{})
	written := make(chan struct{})
	go func() {
		defer close(written)
		for {
			select {
			case <-stop:
				return
			default:
			}
			inst.SetArchivedAtThreadSafe(time.Now().UTC())
			inst.SetArchivedAtThreadSafe(time.Time{})
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = inst.IsArchived()
		_ = FilterInstancesByArchive(list, true)
		_ = VisibleInstances(list)
		_ = inst.ArchivedAtThreadSafe()
	}
	close(stop)
	<-written
	if inst.IsArchived() {
		t.Fatal("the session is archived after the last write unarchived it")
	}
}

// TestIsArchived_DoesNotWaitOnTheStatusLock: UpdateStatus holds the
// instance's mu across tool detection and metadata I/O, and the TUI checks
// the archive state of every row it renders. Reading it must not wait for
// that lock (#1753).
func TestIsArchived_DoesNotWaitOnTheStatusLock(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "s1", ArchivedAt: time.Now().UTC()}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	read := make(chan bool, 1)
	go func() { read <- inst.IsArchived() && !inst.ArchivedAtThreadSafe().IsZero() }()
	select {
	case archived := <-read:
		if !archived {
			t.Fatal("IsArchived = false for an archived session")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("IsArchived waited on the lock UpdateStatus holds")
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
