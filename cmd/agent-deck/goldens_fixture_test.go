package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// goldensProfile is the fixed profile name the behaviour-goldens suite seeds
// and drives the built binary against. It is never the "default" profile so
// a bug that accidentally resolves the real default profile fails loudly
// instead of silently reading an empty (and therefore falsely-passing) store.
const goldensProfile = "goldens"

// goldensFixedNow anchors every timestamp written into the seeded store.
// A fixed instant (rather than time.Now()) keeps the raw pre-scrub bytes
// reproducible across machines and days; the scrub pass in goldens_test.go
// still normalizes it away, but starting from a fixed value means a scrub
// bug shows up as a diff instead of being masked by two runs sharing "today".
var goldensFixedNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// seedGoldensStore writes the fixture store described in PROMPT.md deliverable 1:
// 3 groups (one of them a subgroup), 6 sessions with mixed statuses and tools,
// and 1 remote entry (written to config.toml, since remotes are not state.db rows).
//
// It uses the same internal/statedb row types and SaveGroups/SaveInstances
// calls the production Storage layer uses, so the fixture is exactly what a
// real store looks like on disk, not a hand-rolled approximation of one.
func seedGoldensStore(t *testing.T, home string) {
	t.Helper()

	profileDir, err := session.GetProfileDir(goldensProfile)
	if err != nil {
		t.Fatalf("resolving goldens profile dir: %v", err)
	}
	dbPath := filepath.Join(profileDir, "state.db")

	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("opening seeded state.db: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("migrating seeded state.db: %v", err)
	}

	groups := []*statedb.GroupRow{
		{Path: "my-sessions", Name: session.DefaultGroupName, Expanded: true, Order: 0, MaxConcurrent: 0},
		{Path: "backend", Name: "Backend", Expanded: true, Order: 1, DefaultPath: "/repo/backend", MaxConcurrent: 1},
		{Path: "backend/api", Name: "API", Expanded: true, Order: 0, DefaultPath: "/repo/backend/api", MaxConcurrent: 2},
	}
	if err := db.SaveGroups(groups); err != nil {
		t.Fatalf("seeding groups: %v", err)
	}

	base := goldensFixedNow
	mk := func(id, title, group, tool, status, project string, offsetMinutes int) *statedb.InstanceRow {
		created := base.Add(time.Duration(offsetMinutes) * time.Minute)
		return &statedb.InstanceRow{
			ID:           id,
			Title:        title,
			ProjectPath:  project,
			GroupPath:    group,
			Order:        offsetMinutes,
			Command:      "",
			Tool:         tool,
			Status:       status,
			TmuxSession:  "ad-" + id,
			CreatedAt:    created,
			LastAccessed: created,
			ToolData:     []byte(`{}`),
		}
	}

	instances := []*statedb.InstanceRow{
		mk("golden-sess-1", "claude idle", "my-sessions", "claude", string(session.StatusIdle), "/repo/app", 0),
		mk("golden-sess-2", "codex running", "my-sessions", "codex", string(session.StatusRunning), "/repo/app", 1),
		mk("golden-sess-3", "gemini waiting", "backend", "gemini", string(session.StatusWaiting), "/repo/backend", 2),
		mk("golden-sess-4", "shell error", "backend", "shell", string(session.StatusError), "/repo/backend", 3),
		mk("golden-sess-5", "claude stopped", "backend/api", "claude", string(session.StatusStopped), "/repo/backend/api", 4),
		mk("golden-sess-6", "codex queued", "backend/api", "codex", string(session.StatusQueued), "/repo/backend/api", 5),
	}
	if err := db.SaveInstances(instances); err != nil {
		t.Fatalf("seeding instances: %v", err)
	}

	writeGoldensConfig(t, home)
}

// writeGoldensConfig writes the one remote entry the fixture calls for,
// directly as TOML: remotes live in config.toml (internal/session.RemoteConfig),
// not in state.db.
func writeGoldensConfig(t *testing.T, home string) {
	t.Helper()
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	contents := "[remotes.golden-remote]\n" +
		"host = \"golden@remote.invalid\"\n" +
		"profile = \"default\"\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}
