package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestOpenWatcherDBChecksTheProfileStamp: the watcher's own store open binds
// the profile like Storage does. One profile's state.db copied into another
// profile's directory is refused with ErrProfileMismatch, and a profile
// directory reached through a symlink binds as the directory it points at.
func TestOpenWatcherDBChecksTheProfileStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")

	db, err := openWatcherDB("work")
	if err != nil {
		t.Fatalf("openWatcherDB(work): %v", err)
	}
	if got := db.Profile(); got != "work" {
		t.Fatalf("Profile() = %q, want work", got)
	}
	_ = db.Close()

	workPath, err := session.GetDBPathForProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	personalPath, err := session.GetDBPathForProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(personalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	copyWatcherTestFile(t, workPath, personalPath)
	if db, err := openWatcherDB("personal"); !errors.Is(err, statedb.ErrProfileMismatch) {
		if db != nil {
			_ = db.Close()
		}
		t.Fatalf("openWatcherDB on a copy of another profile's store: err = %v, want ErrProfileMismatch", err)
	}

	aliasDir := filepath.Join(filepath.Dir(filepath.Dir(workPath)), "work-alias")
	if err := os.Symlink(filepath.Dir(workPath), aliasDir); err != nil {
		t.Fatal(err)
	}
	db, err = openWatcherDB("work-alias")
	if err != nil {
		t.Fatalf("openWatcherDB through a symlinked profile dir: %v", err)
	}
	_ = db.Close()
}

func copyWatcherTestFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
