package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

type neverStartedFixtureRow struct {
	Name                   string          `json:"name"`
	Tool                   string          `json:"tool"`
	Status                 string          `json:"status"`
	LastAccessedAgoSeconds int64           `json:"last_accessed_ago_seconds"`
	ToolData               json.RawMessage `json:"tool_data"`
	Want                   string          `json:"want"`
}

func loadNeverStartedFixture(t *testing.T) []neverStartedFixtureRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "never-started-reload-20261118", "stored-rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Rows []neverStartedFixtureRow `json:"rows"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Rows) == 0 {
		t.Fatal("fixture has no rows")
	}
	return fx.Rows
}

// A session added by one process and read by another (`add`, then `list`,
// `status`, the web menu, the phone, the TUI after a restart) is reloaded
// from storage. It used to read idle for the 1.5 s grace period and then
// error/unknown-exit for good, because the never-started rule only knew
// sessions added in the same process. Every surface must read it as idle,
// the status the TUI gives a session it just added, and a session that did
// run must still read error once its tmux is gone.
func TestReloadedNeverStartedSession_ReadsIdleNotError(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	storage, err := NewStorageWithProfile("_never_started_reload")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	rows := loadNeverStartedFixture(t)
	created := time.Now().Add(-time.Hour).Truncate(time.Second)
	suffix := time.Now().UnixNano()
	byID := map[string]neverStartedFixtureRow{}
	for n, r := range rows {
		id := fmt.Sprintf("nsr%02d-%d", n, suffix)
		row := &statedb.InstanceRow{
			ID:          id,
			Title:       r.Name,
			ProjectPath: t.TempDir(),
			GroupPath:   "my-sessions",
			Order:       n,
			Command:     r.Tool,
			Tool:        r.Tool,
			Status:      r.Status,
			TmuxSession: fmt.Sprintf("agentdeck_nsr%02d_%d", n, suffix),
			CreatedAt:   created,
			ToolData:    r.ToolData,
		}
		if r.LastAccessedAgoSeconds > 0 {
			row.LastAccessed = time.Now().Add(-time.Duration(r.LastAccessedAgoSeconds) * time.Second)
		}
		if err := storage.GetDB().SaveInstance(row); err != nil {
			t.Fatalf("SaveInstance %s: %v", r.Name, err)
		}
		byID[id] = r
	}

	for _, surface := range []string{"cli-refresh", "update-status"} {
		loaded, _, err := storage.LoadWithGroups()
		if err != nil {
			t.Fatalf("LoadWithGroups: %v", err)
		}
		var mine []*Instance
		for _, inst := range loaded {
			if _, ok := byID[inst.ID]; ok {
				mine = append(mine, inst)
			}
		}
		if len(mine) != len(rows) {
			t.Fatalf("%s: reloaded %d of %d rows", surface, len(mine), len(rows))
		}
		if surface == "cli-refresh" {
			// list / status / session show / web: one refresh pass, then
			// UpdateStatus per row.
			RefreshInstancesForCLIStatus(mine)
		}
		for _, inst := range mine {
			// Twice: the second pass is the row the first pass would have
			// written back, and it must not flip.
			for pass := 1; pass <= 2; pass++ {
				inst.ForceNextStatusCheck()
				if err := inst.UpdateStatus(); err != nil {
					t.Fatalf("%s %s: UpdateStatus: %v", surface, byID[inst.ID].Name, err)
				}
				r := byID[inst.ID]
				if got := string(inst.GetStatusThreadSafe()); got != r.Want {
					t.Errorf("%s pass %d: %s (stored %q) reads %q, want %q", surface, pass, r.Name, r.Status, got, r.Want)
				}
			}
		}
	}
}
