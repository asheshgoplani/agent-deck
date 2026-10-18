package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Commands that pick their own profile instead of the shared resolution chain
// keep escaping one at a time: watcher commands used the default profile's
// state.db whatever AGENTDECK_PROFILE or default_profile said (#2740),
// remote-agent watched the default profile's DB (#2755), inbox export read
// every profile (#2539) and restart bookkeeping went to the global DB
// (#2206). Each fix added a test for its own command only.
//
// This matrix drives the real binary with each way of selecting a profile
// (-p before the subcommand, AGENTDECK_PROFILE, config default_profile) and
// runs several state-writing commands. Every write must land in the selected
// profile's state.db and none in the default profile's.

// profileIsolationDB opens profile's state.db read-only for inspection, or
// returns nil when the profile has no state.db. HOME and XDG must already
// point at the test home.
func profileIsolationDB(t *testing.T, profile string) *statedb.StateDB {
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
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

// profileIsolationRows reports which of the matrix's three records (session,
// group, watcher) exist in profile's state.db.
func profileIsolationRows(t *testing.T, profile, title, group, watcher string) (hasSession, hasGroup, hasWatcher bool) {
	t.Helper()
	db := profileIsolationDB(t, profile)
	if db == nil {
		return false, false, false
	}
	insts, err := db.LoadInstances()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range insts {
		if in.Title == title {
			hasSession = true
		}
	}
	groups, err := db.LoadGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.Name == group {
			hasGroup = true
		}
	}
	w, err := db.LoadWatcherByName(watcher)
	if err != nil {
		t.Fatal(err)
	}
	return hasSession, hasGroup, w != nil
}

func TestProfileIsolation_StateWritingCommandsUseTheSelectedProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	const (
		target  = "work"
		title   = "iso-session"
		group   = "iso-group"
		watcher = "iso-watcher"
	)
	tests := []struct {
		name          string
		flag          []string // global args before the subcommand
		env           string   // AGENTDECK_PROFILE
		configDefault string   // config.json default_profile
	}{
		{name: "-p before the subcommand", flag: []string{"-p", target}},
		{name: "AGENTDECK_PROFILE", env: target},
		{name: "config default_profile", configDefault: target},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			xdg := []string{
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
				"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
				"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
			}
			// Same view of the filesystem in this process, so the DB paths
			// and config.json below are the ones the binary uses.
			t.Setenv("HOME", home)
			for _, kv := range xdg {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			t.Setenv("AGENTDECK_PROFILE", "")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			if tt.configDefault != "" {
				if err := session.SaveConfig(&session.Config{DefaultProfile: tt.configDefault}); err != nil {
					t.Fatal(err)
				}
			}
			project := filepath.Join(home, "proj")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}

			env := append(append([]string{}, xdg...),
				"AGENTDECK_PROFILE="+tt.env, // overrides the helper's ch_support_test
				"AGENTDECK_EVENTS_BUS=off",
			)
			run := func(args ...string) string {
				t.Helper()
				full := append(append([]string{}, tt.flag...), args...)
				stdout, stderr, code := runAgentDeckEnv(t, home, "", env, full...)
				if code != 0 {
					t.Fatalf("agent-deck %s: exit %d\nstdout: %s\nstderr: %s",
						strings.Join(full, " "), code, stdout, stderr)
				}
				return stdout
			}

			run("add", "-t", title, "-c", "shell", "--no-parent", "--json", project)
			run("group", "create", group)
			run("watcher", "create", "webhook", "--name", watcher, "--port", "18791")

			if s, g, w := profileIsolationRows(t, target, title, group, watcher); !s || !g || !w {
				t.Errorf("profile %q: session=%v group=%v watcher=%v; want all three written to the selected profile",
					target, s, g, w)
			}
			if s, g, w := profileIsolationRows(t, session.DefaultProfile, title, group, watcher); s || g || w {
				t.Errorf("profile %q: session=%v group=%v watcher=%v; the default profile must stay untouched",
					session.DefaultProfile, s, g, w)
			}

			// Reads follow the same selection: list shows the session it
			// just wrote.
			if out := run("list", "--json"); !strings.Contains(out, title) {
				t.Errorf("list --json under the selected profile does not show %q:\n%s", title, out)
			}
		})
	}
}
