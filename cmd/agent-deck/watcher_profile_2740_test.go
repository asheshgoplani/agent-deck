package main

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// #2740: without -p, the watcher commands read and wrote the default
// profile's state.db, while web, the TUI and launch honoured AGENTDECK_PROFILE
// and default_profile. A watcher created that way never ran.

func isolateWatcherProfileHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
}

// watcherNamesIn lists the watcher rows in profile's state.db.
func watcherNamesIn(t *testing.T, profile string) []string {
	t.Helper()
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.LoadWatchers()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

func assertWatcherIn(t *testing.T, name, want, notIn string) {
	t.Helper()
	if got := watcherNamesIn(t, want); len(got) != 1 || got[0] != name {
		t.Errorf("profile %q watchers = %v, want [%s]", want, got, name)
	}
	if got := watcherNamesIn(t, notIn); len(got) != 0 {
		t.Errorf("profile %q watchers = %v, want none: the watcher landed in the wrong profile", notIn, got)
	}
}

func TestWatcherCreate_HonoursAGENTDECK_PROFILE_2740(t *testing.T) {
	isolateWatcherProfileHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	handleWatcher("", []string{"create", "webhook", "--name", "hook-env", "--port", "18781"})

	assertWatcherIn(t, "hook-env", "work", "default")
}

func TestWatcherCreate_HonoursDefaultProfile_2740(t *testing.T) {
	isolateWatcherProfileHome(t)
	if err := session.SaveConfig(&session.Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}

	handleWatcher("", []string{"create", "webhook", "--name", "hook-cfg", "--port", "18782"})

	assertWatcherIn(t, "hook-cfg", "work", "default")
}

// An explicit -p still wins over AGENTDECK_PROFILE and default_profile.
func TestWatcherCreate_ExplicitProfileWins_2740(t *testing.T) {
	isolateWatcherProfileHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")
	if err := session.SaveConfig(&session.Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}

	handleWatcher("personal", []string{"create", "webhook", "--name", "hook-flag", "--port", "18783"})

	assertWatcherIn(t, "hook-flag", "personal", "work")
}

// start/stop and the status undelivered count act on the resolved profile's
// rows too: start finds the watcher created under AGENTDECK_PROFILE.
func TestWatcherStartStop_HonourAGENTDECK_PROFILE_2740(t *testing.T) {
	isolateWatcherProfileHome(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	handleWatcher("", []string{"create", "webhook", "--name", "hook-ss", "--port", "18784"})
	handleWatcher("", []string{"start", "hook-ss"})

	dbPath, err := session.GetDBPathForProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	w, err := db.LoadWatcherByName("hook-ss")
	if err != nil || w == nil {
		t.Fatalf("LoadWatcherByName = %v, %v", w, err)
	}
	if w.Status != "running" {
		t.Errorf("status = %q after start, want running", w.Status)
	}
	if _, err := watcherUndelivered("work", w.ID); err != nil {
		t.Errorf("watcherUndelivered: %v", err)
	}
}
