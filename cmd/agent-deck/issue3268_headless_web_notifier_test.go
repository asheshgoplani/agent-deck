package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #3268: while `web --no-tui` ran for the profile, notify-daemon never
// saw a child's turn end when only the pane shows the child's status (Codex
// without its notify hook, here a Claude-like fake that fires no hooks). The
// headless web server registered and heartbeated as a live TUI, so the daemon
// stopped probing tmux and read status rows that nothing updated.
func TestIssue3268_HeadlessWebDoesNotBlindTheNotifier(t *testing.T) {
	parallelSubprocessTest(t)
	e := newDeliveryEnv(t)
	writeFile(t, filepath.Join(e.root, "config", "agent-deck", "config.toml"),
		fmt.Sprintf("[claude]\ncommand = %q\nhooks_enabled = false\n", filepath.Join(e.root, "fakebin", "claude")), 0o600)
	parent := e.startFakeNoHooks("parent", "idle", "")
	child := e.startFakeNoHooks("child", "busy", parent.id)

	e.startWebForProfile(deliveryE2EProfile, "web")
	// The headless web server polls no session status, so it must not look
	// like a live TUI to the daemon.
	if n := e.aliveInstances(); n != 0 {
		t.Errorf("with only web --no-tui running, %d instance(s) heartbeat as a live TUI, want 0", n)
	}

	e.startNotifyDaemon()
	// Let the daemon run a few passes (1 s apart while a child runs) with the
	// child busy. Not a wait on the status row: that row is what froze.
	time.Sleep(5 * time.Second)

	e.setPhaseNoHooks(child, "idle")
	deadline := time.Now().Add(20 * time.Second)
	for len(e.pendingFrom(parent.id, child.id)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("20s after the child's turn ended its parent's inbox holds nothing (child status row %q, live TUIs %d): the notifier missed running -> waiting while web --no-tui ran",
				e.childStatus(child.id), e.aliveInstances())
		}
		time.Sleep(250 * time.Millisecond)
	}
	got := e.pendingFrom(parent.id, child.id)
	if len(got) != 1 || got[0]["from_status"] != "running" || got[0]["to_status"] != "waiting" {
		t.Fatalf("parent inbox holds %v, want one running -> waiting record", got)
	}
}

// startNotifyDaemon runs `notify-daemon` for the env's profile in its own
// process group until the test ends.
func (e *deliveryEnv) startNotifyDaemon() {
	e.t.Helper()
	cmd := exec.Command(e.bin, "-p", deliveryE2EProfile, "notify-daemon")
	cmd.Env = e.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logFile, err := os.Create(filepath.Join(e.root, "notify-daemon.log"))
	if err != nil {
		e.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(exited)
	}()
	e.t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	})
}

// stateDB opens the profile's state.db read-only.
func (e *deliveryEnv) stateDB() *statedb.StateDB {
	e.t.Helper()
	db, err := statedb.OpenReadOnlyLive(filepath.Join(e.root, "data", "agent-deck", "profiles", deliveryE2EProfile, "state.db"))
	if err != nil {
		e.t.Fatalf("open state.db: %v", err)
	}
	return db
}

// aliveInstances is what the daemon's live-TUI check counts.
func (e *deliveryEnv) aliveInstances() int {
	e.t.Helper()
	db := e.stateDB()
	defer db.Close()
	n, err := db.AliveInstanceCount()
	if err != nil {
		e.t.Fatalf("AliveInstanceCount: %v", err)
	}
	return n
}

// childStatus is the status row the daemon reads while a TUI is alive.
func (e *deliveryEnv) childStatus(id string) string {
	e.t.Helper()
	db := e.stateDB()
	defer db.Close()
	rows, err := db.ReadAllStatuses()
	if err != nil {
		e.t.Fatalf("ReadAllStatuses: %v", err)
	}
	return rows[id].Status
}
