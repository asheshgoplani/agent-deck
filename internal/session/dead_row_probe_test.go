package session

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var deadRowSocketSeq atomic.Int64

// deadRowSocket starts a private tmux server on its own socket holding the
// given live sessions, and kills only that server afterwards.
func deadRowSocket(t *testing.T, tag string, live ...string) string {
	t.Helper()
	socket := fmt.Sprintf("drp-%s-%d-%d", tag, os.Getpid(), deadRowSocketSeq.Add(1))
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	for _, name := range live {
		if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name, "/bin/sh").CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s on %s: %v %s", name, socket, err, out)
		}
	}
	return socket
}

// deadRowPair returns two identical reloaded instances (not added in this
// process, started long ago) for a tmux session name on socket: one for a
// pass, one for plain UpdateStatus, so verdicts can be compared.
func deadRowPair(name, socket, tool string) (*Instance, *Instance) {
	return deadRowPairStatus(name, socket, tool, StatusRunning)
}

// deadRowPairStatus is deadRowPair with the row's stored status.
func deadRowPairStatus(name, socket, tool string, status Status) (*Instance, *Instance) {
	mk := func() *Instance {
		inst := &Instance{ID: name, Title: name, Tool: tool, Status: status,
			CreatedAt: time.Now().Add(-time.Hour)}
		inst.tmuxSession = tmux.ReconnectSessionLazy(name, name, os.TempDir(), "", "running")
		inst.tmuxSession.SocketName = socket
		return inst
	}
	return mk(), mk()
}

func requireSameVerdict(t *testing.T, got, want *Instance) {
	t.Helper()
	if got.GetStatusThreadSafe() != want.GetStatusThreadSafe() || got.Substate() != want.Substate() ||
		(got.ExitCode() == nil) != (want.ExitCode() == nil) {
		t.Errorf("%s on %s: pass verdict %q/%q, plain UpdateStatus %q/%q", got.ID, got.tmuxSession.SocketName,
			got.GetStatusThreadSafe(), got.Substate(), want.GetStatusThreadSafe(), want.Substate())
	}
}

// A row whose tmux session is gone cost a has-session and a list-panes in
// every status pass that loads fresh instances (web menu, CLI list): two tmux
// processes per dead row per request. One listing per socket per pass proves
// those rows absent. The rows are already recorded dead (error, stopped), the
// common dead row; a row last seen live is confirmed by has-session instead
// (TestStatusUpdatePass_ListedAbsentLiveRowConfirmsBeforeDeath). Rows on two sockets (TmuxSocketName per row), with the
// same session name live on one socket and dead on the other.
func TestStatusUpdatePass_DeadRowsStartNoTmuxPerRow(t *testing.T) {
	skipIfNoTmuxBinary(t)
	const perSocket = 6
	sockA := deadRowSocket(t, "a", "drp-keep", "drp-both")
	sockB := deadRowSocket(t, "b", "drp-keep")

	var withPass, plain []*Instance
	add := func(name, socket, tool string, status Status) {
		p, q := deadRowPairStatus(name, socket, tool, status)
		withPass, plain = append(withPass, p), append(plain, q)
	}
	tools := []string{"shell", "claude", "codex", "opencode"}
	dead := []Status{StatusError, StatusStopped}
	for _, sock := range []string{sockA, sockB} {
		for i := 0; i < perSocket; i++ {
			add(fmt.Sprintf("drp-dead-%d", i), sock, tools[i%len(tools)], dead[i%len(dead)])
		}
	}
	add("drp-both", sockB, "shell", StatusError) // live on A only: dead here

	var pass StatusUpdatePass
	before := tmux.SubprocessStarts()
	for _, inst := range withPass {
		_ = pass.UpdateStatusOnly(inst)
	}
	starts := tmux.SubprocessStarts() - before
	t.Logf("tmux starts for %d dead rows on 2 sockets: %d", len(withPass), starts)
	for _, inst := range plain {
		_ = inst.updateStatus(nil, false)
	}

	for n := range withPass {
		requireSameVerdict(t, withPass[n], plain[n])
		if got := withPass[n].GetStatusThreadSafe(); got == StatusRunning || got == StatusIdle {
			t.Errorf("%s on %s: status %q for a dead row", withPass[n].ID, withPass[n].tmuxSession.SocketName, got)
		}
	}
	if starts > 2 {
		t.Fatalf("a status pass over %d dead rows on 2 sockets started %d tmux commands; want at most 2 (one listing per socket)",
			len(withPass), starts)
	}
}

// tmux resolves `has-session -t name` by unique prefix when no session has
// that exact name, so a stored name that prefixes a live session still reads
// as existing. A listing cannot prove such a row absent: it must keep its
// probe and the same verdict as before.
func TestStatusUpdatePass_PrefixOfLiveSessionKeepsProbeVerdict(t *testing.T) {
	skipIfNoTmuxBinary(t)
	sock := deadRowSocket(t, "pfx", "drp-pfx-live")
	p, q := deadRowPair("drp-pfx", sock, "shell")
	var pass StatusUpdatePass
	_ = pass.UpdateStatusOnly(p)
	_ = q.updateStatus(nil, false)
	t.Logf("verdict with pass %q, plain %q", p.GetStatusThreadSafe(), q.GetStatusThreadSafe())
	requireSameVerdict(t, p, q)
}

// Live rows the warm session cache already answers must not pay for a
// listing: only a row Exists would probe needs one. Same tmux commands as
// plain UpdateStatus.
func TestStatusUpdatePass_CachedLiveRowsDoNotList(t *testing.T) {
	skipIfNoTmuxBinary(t)
	names := []string{"drp-live-0", "drp-live-1", "drp-live-2"}
	sock := deadRowSocket(t, "live", names...)
	prev := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(sock)
	t.Cleanup(func() { tmux.SetDefaultSocketName(prev) })

	var withPass, plain []*Instance
	for _, name := range names {
		p, q := deadRowPair(name, sock, "shell")
		withPass, plain = append(withPass, p), append(plain, q)
	}
	tmux.RefreshExistingSessions()
	before := tmux.SubprocessStarts()
	for _, inst := range plain {
		_ = inst.updateStatus(nil, false)
	}
	plainStarts := tmux.SubprocessStarts() - before
	tmux.RefreshExistingSessions()
	before = tmux.SubprocessStarts()
	var pass StatusUpdatePass
	for _, inst := range withPass {
		_ = pass.UpdateStatusOnly(inst)
	}
	passStarts := tmux.SubprocessStarts() - before
	t.Logf("tmux starts for %d cached live rows: plain %d, pass %d", len(names), plainStarts, passStarts)
	if passStarts > plainStarts {
		t.Fatalf("a pass over %d live rows in the warm session cache started %d tmux commands, plain UpdateStatus %d; want no extra listing",
			len(names), passStarts, plainStarts)
	}
}

// The web menu and the TUI share one pass between concurrent workers. They
// must still share one listing per socket: concurrent rows wait for the
// listing in flight instead of starting their own.
func TestStatusUpdatePass_ConcurrentWorkersShareOneListingPerSocket(t *testing.T) {
	skipIfNoTmuxBinary(t)
	const perSocket, workers = 20, 10
	sockA := deadRowSocket(t, "ca", "drp-keep")
	sockB := deadRowSocket(t, "cb", "drp-keep")

	var withPass, plain []*Instance
	for _, sock := range []string{sockA, sockB} {
		for i := 0; i < perSocket; i++ {
			p, q := deadRowPairStatus(fmt.Sprintf("drp-cdead-%d", i), sock, "shell", StatusError)
			withPass, plain = append(withPass, p), append(plain, q)
		}
	}

	var pass StatusUpdatePass
	rows := make(chan *Instance, len(withPass))
	for _, inst := range withPass {
		rows <- inst
	}
	close(rows)
	start := make(chan struct{})
	var wg sync.WaitGroup
	before := tmux.SubprocessStarts()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for inst := range rows {
				_ = pass.UpdateStatusOnly(inst)
			}
		}()
	}
	close(start)
	wg.Wait()
	starts := tmux.SubprocessStarts() - before
	t.Logf("tmux starts for %d dead rows on 2 sockets, %d workers: %d", len(withPass), workers, starts)
	for _, inst := range plain {
		_ = inst.updateStatus(nil, false)
	}
	for n := range withPass {
		requireSameVerdict(t, withPass[n], plain[n])
	}
	if starts > 2 {
		t.Fatalf("%d workers sharing a pass over %d dead rows on 2 sockets started %d tmux commands; want at most 2 (one listing per socket)",
			workers, len(withPass), starts)
	}
}

// A listing is a snapshot. A session created after it must not read as
// absent once the listing is older than passSessionListMaxAge: the pass
// lists the socket again. The wait is fixed, not derived from the constant,
// so it also pins the bound well under the 1.5 s start grace.
func TestStatusUpdatePass_ListingAgesOut(t *testing.T) {
	skipIfNoTmuxBinary(t)
	sock := deadRowSocket(t, "age", "drp-keep")
	gone, _ := deadRowPairStatus("drp-age-gone", sock, "shell", StatusError)
	var pass StatusUpdatePass
	_ = pass.UpdateStatusOnly(gone) // lists sock without drp-age-late

	if out, err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "drp-age-late", "/bin/sh").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v %s", err, out)
	}
	time.Sleep(400 * time.Millisecond)
	// Recorded dead (error): a row the listing alone may answer.
	late, _ := deadRowPairStatus("drp-age-late", sock, "shell", StatusError)
	_ = pass.UpdateStatusOnly(late)
	if got := late.GetStatusThreadSafe(); got == StatusError || got == StatusStopped {
		t.Fatalf("session created after the pass listing read %q after the listing aged out; want it seen live", got)
	}
}

// A listing never answers a live row positively, so a live row with no
// in-process positive (a socket other than the default, no PipeManager
// connection) keeps its own has-session and pays only its share of one
// listing per socket: at most one tmux command more than plain UpdateStatus.
func TestStatusUpdatePass_UncachedLiveRowsCostOneListingPerSocket(t *testing.T) {
	skipIfNoTmuxBinary(t)
	names := []string{"drp-ulive-0", "drp-ulive-1", "drp-ulive-2", "drp-ulive-3"}
	sock := deadRowSocket(t, "ulive", names...)
	var withPass, plain []*Instance
	for _, name := range names {
		p, q := deadRowPair(name, sock, "shell")
		withPass, plain = append(withPass, p), append(plain, q)
	}
	before := tmux.SubprocessStarts()
	for _, inst := range plain {
		_ = inst.updateStatus(nil, false)
	}
	plainStarts := tmux.SubprocessStarts() - before
	before = tmux.SubprocessStarts()
	var pass StatusUpdatePass
	for _, inst := range withPass {
		_ = pass.UpdateStatusOnly(inst)
	}
	passStarts := tmux.SubprocessStarts() - before
	t.Logf("tmux starts for %d uncached live rows on one socket: plain %d, pass %d", len(names), plainStarts, passStarts)
	for n := range withPass {
		requireSameVerdict(t, withPass[n], plain[n])
	}
	if passStarts > plainStarts+1 {
		t.Fatalf("a pass over %d uncached live rows on one socket started %d tmux commands, plain UpdateStatus %d; want at most one listing more",
			len(names), passStarts, plainStarts)
	}
}

// A listing is up to passSessionListMaxAge old. A session another process
// created or restarted after it reads absent there, and a row last seen live
// written error from that would page its parent (running -> error) and stick
// for errorRecheckInterval. So a row whose stored status is not dead
// (anything but error/stopped) confirms the listing's absence with its own
// has-session before a terminated status is written.
func TestStatusUpdatePass_ListedAbsentLiveRowConfirmsBeforeDeath(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, status := range []Status{StatusRunning, StatusWaiting, StatusIdle, StatusStarting} {
		t.Run(string(status), func(t *testing.T) {
			sock := deadRowSocket(t, "conf", "drp-keep")
			// The listing is taken first: it has only drp-keep.
			stale, err := tmux.ListSessionNamesOnSocket(sock)
			if err != nil || stale == nil {
				t.Fatalf("listing of %s failed: %v", sock, err)
			}
			// Then another process creates the row's session.
			if out, err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "drp-conf-new", "/bin/sh").CombinedOutput(); err != nil {
				t.Fatalf("tmux new-session: %v %s", err, out)
			}
			// Every listing the pass takes answers with that pre-create
			// listing, so the row sees it however long the steps above took
			// (no dependence on landing inside passSessionListMaxAge).
			prev := passListSessionNames
			t.Cleanup(func() { passListSessionNames = prev })
			var calls atomic.Int64
			passListSessionNames = func(string) (map[string]struct{}, error) {
				calls.Add(1)
				names := make(map[string]struct{}, len(stale))
				for n := range stale {
					names[n] = struct{}{}
				}
				return names, nil
			}
			var pass StatusUpdatePass
			row, _ := deadRowPairStatus("drp-conf-new", sock, "shell", status)
			_ = pass.UpdateStatusOnly(row)
			if calls.Load() == 0 {
				t.Fatalf("row stored %q never consulted the pass listing; the test no longer exercises a listed-absent row", status)
			}
			if got := row.GetStatusThreadSafe(); got == StatusError || got == StatusStopped {
				t.Fatalf("row stored %q whose session was created after the pass listing read %q; want it seen live", status, got)
			}
		})
	}
}

// Start() reuses the same tmux.Session and does not bump stopRevision. A probe
// answering from a listing taken before Start's new-session must not
// overwrite the 'starting' Start wrote with error: Start's lastStartTime makes
// that probe stale. The row is recorded dead (error), the restart-a-dead-row
// case, so the listing alone would answer it.
func TestStatusUpdatePass_StartDuringListingKeepsStarting(t *testing.T) {
	skipIfNoTmuxBinary(t)
	sock := deadRowSocket(t, "start", "drp-keep")
	row, _ := deadRowPairStatus("drp-start", sock, "shell", StatusError)
	prev := passListSessionNames
	t.Cleanup(func() { passListSessionNames = prev })
	passListSessionNames = func(socket string) (map[string]struct{}, error) {
		names, err := prev(socket) // listed before the start: no drp-start
		if out, err := exec.Command("tmux", "-L", sock, "new-session", "-d", "-s", "drp-start", "/bin/sh").CombinedOutput(); err != nil {
			t.Errorf("tmux new-session: %v %s", err, out)
		}
		// What Start() writes once new-session returned (instance.go).
		row.mu.Lock()
		row.lastStartTime = time.Now()
		row.lastErrorCheck = time.Time{}
		row.Status = StatusStarting
		row.mu.Unlock()
		return names, err
	}
	var pass StatusUpdatePass
	_ = pass.UpdateStatusOnly(row)
	if got := row.GetStatusThreadSafe(); got != StatusStarting {
		t.Fatalf("probe from a listing taken before Start() wrote %q over starting; want starting", got)
	}
}

// The listing's age counts from before list-sessions runs: a session created
// while a slow listing was in flight is missing from it, so a listing that
// took longer than passSessionListMaxAge is already too old to reuse.
func TestStatusUpdatePass_SlowListingIsNotReused(t *testing.T) {
	skipIfNoTmuxBinary(t)
	sock := deadRowSocket(t, "slow", "drp-keep")
	prev := passListSessionNames
	t.Cleanup(func() { passListSessionNames = prev })
	var calls atomic.Int64
	passListSessionNames = func(socket string) (map[string]struct{}, error) {
		calls.Add(1)
		names, err := prev(socket)
		time.Sleep(passSessionListMaxAge + 50*time.Millisecond)
		return names, err
	}
	var pass StatusUpdatePass
	for _, name := range []string{"drp-slow-0", "drp-slow-1"} {
		row, _ := deadRowPairStatus(name, sock, "shell", StatusError)
		_ = pass.UpdateStatusOnly(row)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("two rows after a listing slower than the max age listed %d times; want 2 (the slow listing is not reused)", got)
	}
}

// A dead row last seen live pays its confirming has-session, but no
// list-panes: the has-session already proved the pane gone. Same verdict as
// plain UpdateStatus, at most one listing, the socket's once-per-socket
// protocol check (after a failed has-session), and one has-session per row.
func TestStatusUpdatePass_DeadLiveRowsCostOneHasSessionEach(t *testing.T) {
	skipIfNoTmuxBinary(t)
	const rows = 6
	sock := deadRowSocket(t, "dlive", "drp-keep")
	var withPass, plain []*Instance
	for i := 0; i < rows; i++ {
		p, q := deadRowPairStatus(fmt.Sprintf("drp-dlive-%d", i), sock, "shell", StatusRunning)
		withPass, plain = append(withPass, p), append(plain, q)
	}
	var pass StatusUpdatePass
	before := tmux.SubprocessStarts()
	for _, inst := range withPass {
		_ = pass.UpdateStatusOnly(inst)
	}
	starts := tmux.SubprocessStarts() - before
	t.Logf("tmux starts for %d dead rows stored running: %d", rows, starts)
	for _, inst := range plain {
		_ = inst.updateStatus(nil, false)
	}
	for n := range withPass {
		requireSameVerdict(t, withPass[n], plain[n])
	}
	if starts > 2+rows {
		t.Fatalf("a pass over %d dead rows stored running started %d tmux commands; want at most %d (one listing, one protocol check, one has-session per row)",
			rows, starts, 2+rows)
	}
}
