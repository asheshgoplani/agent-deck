package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2740: without -p, every `watcher` subcommand read and wrote the
// default profile's state.db. openWatcherDB and watcherUndelivered built the
// path from the raw -p value, and GetDBPathForProfile("") is the default
// profile's. The TUI, the web server and the other commands resolve the
// profile first (AGENTDECK_PROFILE, then a CLAUDE_CONFIG_DIR-inferred profile
// behind the #1790 guard, then default_profile), so a watcher created with
// AGENTDECK_PROFILE=work went to a profile whose engine never runs it.

// issue2740Watcher returns profile's watcher row named name, or nil when the
// profile has none. It never creates the profile's state.db.
func issue2740Watcher(t *testing.T, profile, name string) *statedb.WatcherRow {
	t.Helper()
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	w, err := db.LoadWatcherByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// TestIssue2740_WatcherCommandsUseTheResolvedProfile drives `watcher create`,
// `list` and `status` through the dispatcher, with -p given or not, and checks
// the watcher lands in the profile every other command would use.
func TestIssue2740_WatcherCommandsUseTheResolvedProfile(t *testing.T) {
	tests := []struct {
		name          string
		flag          string   // -p
		env           string   // AGENTDECK_PROFILE
		claudeDir     string   // CLAUDE_CONFIG_DIR, relative to HOME
		configDefault string   // config.json default_profile
		existing      []string // profiles that already have a state.db
		want          string
	}{
		{name: "AGENTDECK_PROFILE", env: "work", want: "work"},
		{name: "default_profile", configDefault: "work", want: "work"},
		{name: "CLAUDE_CONFIG_DIR of an existing profile", claudeDir: ".claude-work", existing: []string{"work"}, want: "work"},
		{name: "CLAUDE_CONFIG_DIR of a missing profile falls back to default_profile", claudeDir: ".claude-ghost", configDefault: "work", want: "work"},
		{name: "-p wins", flag: "other", env: "work", configDefault: "work", want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENTDECK_EVENTS_BUS", "off")
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
			t.Setenv("AGENTDECK_PROFILE", tt.env)
			claudeDir := ""
			if tt.claudeDir != "" {
				claudeDir = filepath.Join(home, tt.claudeDir)
			}
			t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
			if tt.configDefault != "" {
				if err := session.SaveConfig(&session.Config{DefaultProfile: tt.configDefault}); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range tt.existing {
				db, err := openWatcherDB(p)
				if err != nil {
					t.Fatal(err)
				}
				_ = db.Close()
			}

			const name = "hook-2740"
			captureStdout(t, func() {
				handleWatcher(tt.flag, []string{"create", "webhook", "--name", name, "--port", "18781"})
			})
			for _, p := range []string{"default", "work", "other", "ghost"} {
				if got := issue2740Watcher(t, p, name) != nil; got != (p == tt.want) {
					t.Fatalf("watcher in profile %q: %v; want it only in %q", p, got, tt.want)
				}
			}
			if exists, err := session.ProfileExists("ghost"); err != nil || exists {
				t.Fatalf("profile ghost exists: %v, %v; the #1790 guard must not create it", exists, err)
			}

			var listed []watcherListEntry
			out := captureStdout(t, func() { handleWatcher(tt.flag, []string{"list", "--json"}) })
			if err := json.Unmarshal([]byte(out), &listed); err != nil {
				t.Fatalf("watcher list --json: %v\n%s", err, out)
			}
			if len(listed) != 1 || listed[0].Name != name {
				t.Fatalf("watcher list --json = %s; want only %s", out, name)
			}

			// A routed event waiting in the resolved profile's send queue is
			// what `watcher status` reports as undelivered.
			dbPath, err := session.GetDBPathForProfile(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			w := issue2740Watcher(t, tt.want, name)
			if _, created, err := sendqueue.EnqueueOnce(tt.want, sendqueue.Dir(filepath.Dir(dbPath)), sendqueue.Send{
				SessionID: "conductor-2740", SessionTitle: session.ConductorSessionTitlePrefix + "ops", Tool: "shell",
				Message: "[webhook] issue-2740", Sender: "watcher:" + name, Key: "watcher:" + w.ID + ":k1",
				WaitWhileStopped: true,
			}, time.Now()); err != nil || !created {
				t.Fatalf("queue a routed event: created %v, %v", created, err)
			}
			var st struct {
				Name        string `json:"name"`
				Undelivered []struct {
					Conductor string `json:"conductor"`
					Count     int    `json:"count"`
				} `json:"undelivered"`
			}
			out = captureStdout(t, func() { handleWatcher(tt.flag, []string{"status", name, "--json"}) })
			if err := json.Unmarshal([]byte(out), &st); err != nil {
				t.Fatalf("watcher status --json: %v\n%s", err, out)
			}
			if st.Name != name || len(st.Undelivered) != 1 || st.Undelivered[0].Conductor != "ops" || st.Undelivered[0].Count != 1 {
				t.Fatalf("watcher status --json = %s; want %s with one undelivered event for ops", out, name)
			}
		})
	}
}
