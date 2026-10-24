package web

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var menuStatusPassSeq atomic.Int64

// seedMenuStatusPassFleet writes n sessions to a fresh profile's state.db and
// starts one shell pane per session on a private tmux socket, made the process
// default for the test. Only that private server is killed afterwards.
func seedMenuStatusPassFleet(t *testing.T, n int) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if os.Getenv("AGENT_DECK_TEST_HOME_ISOLATED") != "1" || os.Getenv("AGENT_DECK_TEST_ISOLATED") != "1" {
		t.Skip("needs the isolated HOME and tmux dir from TestMain")
	}
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")
	t.Setenv("AGENTDECK_SUPPRESS_TMUX_WARNING", "1")

	seq := menuStatusPassSeq.Add(1)
	profile := fmt.Sprintf("menupass-%d-%d", os.Getpid(), seq)
	socket := fmt.Sprintf("menupass%d-%d", os.Getpid(), seq)
	path, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}

	tools := []string{"shell", "claude", "codex"}
	statuses := []string{"idle", "waiting", "running", "error"}
	home, _ := os.UserHomeDir()
	rows := make([]*statedb.InstanceRow, n)
	args := []string{"-L", socket}
	for i := range rows {
		id := fmt.Sprintf("mp-%04d", i)
		rows[i] = &statedb.InstanceRow{
			ID: id, Title: "pass " + id, ProjectPath: home, GroupPath: fmt.Sprintf("work/team-%d", i%4),
			Order: i, Tool: tools[i%3], Status: statuses[i%4], TmuxSession: "agentdeck_" + id,
			TmuxSocketName: socket, CreatedAt: time.Now().Add(-time.Hour), ToolData: json.RawMessage(`{}`),
		}
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "new-session", "-d", "-s", rows[i].TmuxSession, "-x", "120", "-y", "40", "/bin/sh")
	}
	if err := db.SaveInstances(rows); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v %s", err, out)
	}
	prev := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(socket)
	t.Cleanup(func() { tmux.SetDefaultSocketName(prev) })
	return profile
}

// serialMenuSnapshot is the menu load as it was before the status pass ran on
// a worker pool: same storage load, cache refresh, hook handling and status
// update, one session after another. It is the reference for both the output
// and the cost of LoadMenuSnapshot. It is a copy rather than a call into
// refreshStatuses on purpose, so the reference stays the old serial code; keep
// it in step with any per-session step added to refreshStatuses.
func serialMenuSnapshot(t *testing.T, profile string, hooks map[string]*session.HookStatus) *MenuSnapshot {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	tmux.RefreshExistingSessions()
	tmux.RefreshPaneInfoCache()
	var pass session.StatusUpdatePass
	for _, inst := range instances {
		haveHook := false
		if hs := hooks[inst.ID]; hs != nil {
			inst.UpdateHookStatus(hs)
			haveHook = true
		}
		if inst.GetToolThreadSafe() == "claude" && !haveHook {
			inst.ForceNextStatusCheck()
		}
		if inst.GetTmuxSession() == nil {
			continue
		}
		_ = pass.UpdateStatus(inst)
	}
	return BuildMenuSnapshot(storage.Profile(), session.FilterInstancesByArchive(instances, false), groups, time.Now())
}

func menuSessionStatuses(snap *MenuSnapshot) map[string]string {
	out := map[string]string{}
	for _, item := range snap.Items {
		if item.Session != nil {
			out[item.Session.ID] = string(item.Session.Status) + "/" + item.Session.Substate
		}
	}
	return out
}

// TestLoadMenuSnapshot_StatusPassMatchesSerial pins that the pooled status
// pass reports exactly what the serial pass reports for every session. The
// fleet mixes live panes, sessions whose tmux session is gone, and rows with
// fresh hook data (running, waiting, idle), so rows resolve to different
// statuses.
func TestLoadMenuSnapshot_StatusPassMatchesSerial(t *testing.T) {
	const n = 30
	profile := seedMenuStatusPassFleet(t, n)
	socket := tmux.DefaultSocketName()
	for i := 4; i < n; i += 5 {
		target := fmt.Sprintf("agentdeck_mp-%04d", i)
		if out, err := exec.Command("tmux", "-L", socket, "kill-session", "-t", target).CombinedOutput(); err != nil {
			t.Fatalf("kill-session %s: %v %s", target, err, out)
		}
	}
	hookStates := []string{"running", "waiting", "idle"}
	hooks := map[string]*session.HookStatus{}
	for i := 1; i < n; i += 3 { // claude rows (tools cycle shell, claude, codex)
		if i%7 == 0 {
			continue // some claude rows without hook data
		}
		hooks[fmt.Sprintf("mp-%04d", i)] = &session.HookStatus{
			Status: hookStates[i%3], SessionID: fmt.Sprintf("claude-%d", i), Event: "Stop", UpdatedAt: time.Now(),
		}
	}
	svc := NewSessionDataService(profile)
	svc.loadHookStatuses = func() map[string]*session.HookStatus { return hooks }

	want := menuSessionStatuses(serialMenuSnapshot(t, profile, hooks))
	snap, err := svc.LoadMenuSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := menuSessionStatuses(snap)
	if len(want) != n || len(got) != n {
		t.Fatalf("sessions: serial %d, menu %d, want %d", len(want), len(got), n)
	}
	distinct := map[string]bool{}
	for id, w := range want {
		distinct[w] = true
		if got[id] != w {
			t.Errorf("session %s: menu status %q, serial status %q", id, got[id], w)
		}
	}
	t.Logf("distinct serial statuses: %v", distinct)
	if len(distinct) < 2 {
		t.Fatalf("fleet resolved to one status %v; the comparison would be weak", distinct)
	}
}

// TestLoadMenuSnapshot_StatusPassIsPooled pins the GET /api/menu gain: the
// per-session status checks (tmux and ps subprocesses) run on a worker pool,
// so one menu load costs well under the serial pass over the same fleet.
// Serial it was ~0.9 s for 100 sessions and ~18 s for 500. Both sides are
// measured here, interleaved, best of three, so machine speed cancels out.
func TestLoadMenuSnapshot_StatusPassIsPooled(t *testing.T) {
	const n = 40
	profile := seedMenuStatusPassFleet(t, n)
	svc := NewSessionDataService(profile)
	if _, err := svc.LoadMenuSnapshot(); err != nil { // warm-up
		t.Fatal(err)
	}

	best := func(cur, d time.Duration) time.Duration {
		if cur == 0 || d < cur {
			return d
		}
		return cur
	}
	var serial, pooled time.Duration
	for round := 0; round < 3; round++ {
		start := time.Now()
		serialMenuSnapshot(t, profile, defaultLoadHookStatuses())
		serial = best(serial, time.Since(start))

		start = time.Now()
		if _, err := svc.LoadMenuSnapshot(); err != nil {
			t.Fatal(err)
		}
		pooled = best(pooled, time.Since(start))
	}
	t.Logf("%d sessions: serial pass %v, LoadMenuSnapshot %v", n, serial, pooled)
	// Pooled runs 3-6x faster here; 1.5x leaves room for a starved runner
	// while still failing the serial pass (ratio ~1).
	if pooled*3 > serial*2 {
		t.Fatalf("LoadMenuSnapshot took %v, want under 2/3 of the serial pass (%v)", pooled, serial)
	}
}
