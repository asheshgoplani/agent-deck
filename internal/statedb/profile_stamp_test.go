package statedb

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestBindProfileStampsOnceAndRefusesAnotherProfile pins the store-side
// identity check: a store without a stamp (every store created before the
// check) is stamped by its first writable bind and keeps its rows; the same
// profile binds again (any letter case); another profile is refused with
// ErrProfileMismatch and the stamp is left as it was.
func TestBindProfileStampsOnceAndRefusesAnotherProfile(t *testing.T) {
	db, path := newTestDBAtPath(t)
	if err := db.SaveInstance(&InstanceRow{
		ID: "legacy-1", Title: "legacy", ProjectPath: "/tmp/p", GroupPath: "g",
		Tool: "shell", Status: "idle", CreatedAt: time.Now(), ToolData: []byte("{}"),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	if stamp, _ := db.GetMeta(metaProfileKey); stamp != "" {
		t.Fatalf("fresh store already stamped %q", stamp)
	}

	if err := db.BindProfile("work"); err != nil {
		t.Fatalf("first bind of an unstamped store: %v", err)
	}
	if got := db.Profile(); got != "work" {
		t.Fatalf("Profile() = %q, want work", got)
	}
	if stamp, _ := db.GetMeta(metaProfileKey); stamp != "work" {
		t.Fatalf("stamp = %q, want work", stamp)
	}
	if n, _ := db.InstanceCount(); n != 1 {
		t.Fatalf("stamping changed the rows: count = %d, want 1", n)
	}
	if err := db.BindProfile("Work"); err != nil {
		t.Fatalf("same profile, other case: %v", err)
	}

	err := db.BindProfile("personal")
	var mm *ProfileMismatchError
	if !errors.Is(err, ErrProfileMismatch) || !errors.As(err, &mm) {
		t.Fatalf("bind for another profile: err = %v, want ErrProfileMismatch", err)
	}
	if mm.Stamped != "work" || mm.Wanted != "personal" || mm.Path != path {
		t.Fatalf("mismatch error = %+v", mm)
	}
	if stamp, _ := db.GetMeta(metaProfileKey); stamp != "work" {
		t.Fatalf("refused bind rewrote the stamp to %q", stamp)
	}

	// A read-only handle checks the stamp too.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ro, err := OpenReadOnlyLive(path)
	if err != nil {
		t.Fatalf("OpenReadOnlyLive: %v", err)
	}
	defer ro.Close()
	if err := ro.BindProfile("personal"); !errors.Is(err, ErrProfileMismatch) {
		t.Fatalf("read-only bind for another profile: err = %v, want ErrProfileMismatch", err)
	}
	if err := ro.BindProfile("work"); err != nil {
		t.Fatalf("read-only bind for the owner: %v", err)
	}
}

// TestBindProfileReadOnlyNeverStamps: a read-only bind of an unstamped store
// succeeds and writes nothing.
func TestBindProfileReadOnlyNeverStamps(t *testing.T) {
	db, path := newTestDBAtPath(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if err := ro.BindProfile("work"); err != nil {
		t.Fatalf("read-only bind of an unstamped store: %v", err)
	}
	_ = ro.Close()

	rw, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rw.Close()
	if stamp, _ := rw.GetMeta(metaProfileKey); stamp != "" {
		t.Fatalf("read-only bind wrote stamp %q", stamp)
	}
}

// TestBindProfileReadOnlyReportsUnreadableStamp: only a store too old to have
// a metadata table counts as "no stamp" on a read-only handle. Any other
// failure to read the stamp (here a closed handle; in the field a busy or
// corrupt store) is reported, not mistaken for an unstamped store.
func TestBindProfileReadOnlyReportsUnreadableStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	bare, err := Open(path) // no Migrate: no metadata table
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := bare.DB().Exec(`CREATE TABLE placeholder (x INTEGER)`); err != nil {
		t.Fatalf("create placeholder: %v", err)
	}
	_ = bare.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if err := ro.BindProfile("work"); err != nil {
		t.Fatalf("read-only bind of a store without a metadata table: %v", err)
	}
	_ = ro.Close()

	closed, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	_ = closed.Close()
	if err := closed.BindProfile("work"); err == nil {
		t.Fatal("read-only bind that could not read the stamp reported success")
	}
}
