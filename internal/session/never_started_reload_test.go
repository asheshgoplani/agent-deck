package session

import (
	"encoding/json"
	"errors"
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

// A start that failed before tmux existed (a failed queued spawn, a missing
// project dir) is stored as error with no start stamp. It is not a
// never-started session: it must keep reading error everywhere, not idle.
// (Found in independent review.)
func TestFailedStartBeforeTmuxStaysError(t *testing.T) {
	t.Run("failed-queued-spawn", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		storage := newTestStorage(t)
		t.Cleanup(SetQueuedSpawnForTest(func(*Instance, string) error { return errors.New("spawn failed") }))
		inst := NewInstanceWithGroup("worker", t.TempDir(), "workers")
		inst.Tool = "claude"
		inst.CreatedAt = time.Now().Add(-time.Hour)
		inst.MarkQueued("brief")
		if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.StartQueued(inst); err == nil {
			t.Fatal("StartQueued returned nil for a failed spawn")
		}
		requireReloadedStatus(t, storage, StatusError)
	})
	t.Run("missing-project-dir", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		storage := newTestStorage(t)
		inst := NewInstanceWithGroup("worker2", "/nonexistent/failed-start-dir", "workers")
		inst.Tool = "shell"
		inst.CreatedAt = time.Now().Add(-time.Hour)
		inst.MarkQueued("")
		if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.StartQueued(inst); err == nil {
			if ts := inst.GetTmuxSession(); ts != nil {
				_ = ts.Kill()
			}
			t.Fatal("start in a missing directory did not fail")
		}
		requireReloadedStatus(t, storage, StatusError)
	})
}

func requireReloadedStatus(t *testing.T, storage *Storage, want Status) {
	t.Helper()
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v %+v", err, loaded)
	}
	stored := loaded[0].Status
	RefreshLiveStatus(loaded)
	if got := loaded[0].GetStatusThreadSafe(); got != want {
		t.Errorf("reloaded status after a failed start = %q (stored %q), want %q", got, stored, want)
	}
}

// A FRESH failed start (created moments ago, not backdated) must read error
// during the 1.5 s start grace and after it. The grace used to rewrite the
// stored error to starting, and the next refresh after the grace then read
// that starting row as never started: error, starting, idle. Covers the
// instance that ran the failed start, the same instance with no tmux handle,
// and the row reloaded by another process (UpdateStatus and the list path).
// (Found in independent review.)
func TestFreshFailedStartStaysErrorThroughGrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)
	t.Cleanup(SetQueuedSpawnForTest(func(*Instance, string) error { return errors.New("spawn failed") }))
	inst := NewInstanceWithGroup("fresh-worker", t.TempDir(), "workers")
	inst.Tool = "claude"
	inst.MarkQueued("brief")
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.StartQueued(inst); err == nil {
		t.Fatal("StartQueued returned nil for a failed spawn")
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("after the failed start: %q, want error", got)
	}
	created := inst.CreatedAt

	noHandle := NewInstanceWithGroup("fresh-worker-nohandle", t.TempDir(), "workers")
	noHandle.CreatedAt = created
	noHandle.Status = StatusError
	noHandle.tmuxSession = nil

	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v %+v", err, loaded)
	}
	reloaded := loaded[0]
	if !reloaded.CreatedAt.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("reloaded row is not fresh: created %v", reloaded.CreatedAt)
	}
	listed, _, err := storage.LoadWithGroups()
	if err != nil || len(listed) != 1 {
		t.Fatalf("load: %v %+v", err, listed)
	}

	check := func(phase string) {
		t.Helper()
		for name, in := range map[string]*Instance{"same-process": inst, "no-tmux-handle": noHandle, "reloaded": reloaded} {
			// Twice: the second read is the row the first would have written back.
			for pass := 1; pass <= 2; pass++ {
				in.ForceNextStatusCheck()
				if err := in.UpdateStatus(); err != nil {
					t.Fatalf("%s %s: UpdateStatus: %v", phase, name, err)
				}
				if got := in.GetStatusThreadSafe(); got != StatusError {
					t.Errorf("%s pass %d: %s reads %q, want error", phase, pass, name, got)
				}
			}
		}
		RefreshLiveStatus(listed)
		if got := listed[0].GetStatusThreadSafe(); got != StatusError {
			t.Errorf("%s: list path reads %q, want error", phase, got)
		}
	}

	if time.Since(created) >= 1500*time.Millisecond {
		t.Skip("setup outlasted the 1.5 s start grace; the grace read was not exercised")
	}
	check("in grace")
	time.Sleep(time.Until(created.Add(1600 * time.Millisecond)))
	check("after grace")
}
