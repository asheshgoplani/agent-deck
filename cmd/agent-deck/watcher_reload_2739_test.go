package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// #2739: `watcher reload` asks the profile's engine owner to apply the
// watchers and routes now and reports what it did.

func isolateWatcherReloadHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))
	t.Setenv("AGENTDECK_PROFILE", "work")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
}

func TestWatcherReload_NoEngine_2739(t *testing.T) {
	isolateWatcherReloadHome(t)

	res, failed, err := watcherReload("", time.Second)
	if err != nil || failed {
		t.Fatalf("watcherReload = failed %v, %v; want a plain answer", failed, err)
	}
	if res.Profile != "work" || res.EngineRunning || res.Applied || res.Report != nil {
		t.Fatalf("result = %+v, want profile work with no engine", res)
	}
}

// An engine owner that never answers (an agent-deck older than reload) is
// reported, not waited on forever, and the command fails.
func TestWatcherReload_OldEngineDoesNotAnswer_2739(t *testing.T) {
	isolateWatcherReloadHome(t)
	path, err := watcher.EngineLockPath("work")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := watcher.AcquireEngineOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	res, failed, err := watcherReload("", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !failed || !res.EngineRunning || res.Applied || res.OwnerPID != os.Getpid() || res.Report != nil {
		t.Fatalf("result = %+v (failed %v), want an unanswered engine owned by this pid", res, failed)
	}
}

func TestWatcherReload_EngineAnswers_2739(t *testing.T) {
	isolateWatcherReloadHome(t)
	dbPath, err := session.GetDBPathForProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
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
	now := time.Now()
	if err := db.SaveWatcher(&statedb.WatcherRow{ID: "w-1", Name: "hook", Type: "webhook", Status: "running", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	dir, err := session.WatcherNameDir("hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "watcher.toml"), []byte("[source]\nport = \""+port+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	host := watcher.NewEngineHost(watcher.HostConfig{
		Profile:         "work",
		DB:              db,
		SpawnSendWorker: func(string, string) error { return nil },
		RetryInterval:   20 * time.Millisecond,
	})
	host.Start()
	defer host.Stop(nil)
	if !host.IsOwner() {
		t.Fatal("the test host did not take the engine")
	}

	res, failed, err := watcherReload("", 5*time.Second)
	if err != nil || failed {
		t.Fatalf("watcherReload = %+v failed %v, %v", res, failed, err)
	}
	if !res.EngineRunning || !res.Applied || res.Report == nil || res.Report.Unchanged != 1 || res.Report.PID != os.Getpid() {
		t.Fatalf("result = %+v, report %+v; want the engine's answer with hook unchanged", res, res.Report)
	}

	if err := db.UpdateWatcherStatus("w-1", "stopped"); err != nil {
		t.Fatal(err)
	}
	res, failed, err = watcherReload("", 5*time.Second)
	if err != nil || failed {
		t.Fatalf("watcherReload after stop = %+v failed %v, %v", res, failed, err)
	}
	// The engine may have applied the stop on its own tick before the reload;
	// either way hook is no longer running.
	if a := host.Engine().Adapters(); len(a) != 0 {
		t.Fatalf("adapters after stop and reload = %+v, want none", a)
	}
}

// Two quick reloads: an answer whose pass started before this request was
// made does not cover it, even when it was written after; one whose pass
// started after does.
func TestWaitWatcherReload_OnlyPassesStartedAfterTheRequest_2739(t *testing.T) {
	isolateWatcherReloadHome(t)
	lock, err := watcher.EngineLockPath("work")
	if err != nil {
		t.Fatal(err)
	}
	ack := filepath.Join(filepath.Dir(lock), watcher.ReloadAckFile)
	if err := os.MkdirAll(filepath.Dir(ack), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAck := func(r watcher.ReloadReport) {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(ack, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requested := time.Now()

	writeAck(watcher.ReloadReport{Request: "other", StartedAt: requested.Add(-time.Second), AppliedAt: requested.Add(time.Second)})
	if rep := waitWatcherReload("work", "mine", requested, 200*time.Millisecond); rep != nil {
		t.Fatalf("accepted %+v from a pass that started before the request", rep)
	}
	writeAck(watcher.ReloadReport{Request: "later", StartedAt: requested.Add(time.Millisecond), AppliedAt: requested.Add(time.Second)})
	if rep := waitWatcherReload("work", "mine", requested, 200*time.Millisecond); rep == nil || rep.Request != "later" {
		t.Fatalf("waitWatcherReload = %+v, want the later pass's answer", rep)
	}
	writeAck(watcher.ReloadReport{Request: "mine", StartedAt: requested.Add(-time.Second)})
	if rep := waitWatcherReload("work", "mine", requested, 200*time.Millisecond); rep == nil || rep.Request != "mine" {
		t.Fatalf("waitWatcherReload = %+v, want the answer to its own token", rep)
	}
}
