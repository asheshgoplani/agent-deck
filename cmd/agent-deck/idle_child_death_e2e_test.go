package main

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Issue #2806: a child idle at its prompt whose process dies on its own (a
// crash, its pane killed, OOM) goes from waiting to error with no running in
// between. The snapshot edge reports only flips out of running and the hook
// path has no SessionEnd candidate, so the parent never heard of it. It now
// gets one urgent record. The fake fires SessionEnd when killed, as Claude
// does. The restart's own kill stays quiet (TestRestartE2E_IdleRestartIsQuiet).

// killPaneGroup sends SIGTERM to the process group of f's pane in the env's
// own tmux server, as `kill -TERM -- -<pane_pid>` does.
func (e *deliveryEnv) killPaneGroup(f *fakeTarget) {
	e.t.Helper()
	cmd := exec.Command("tmux", "display-message", "-p", "-t", f.tmux, "#{pane_pid}")
	cmd.Env = e.env
	out, err := cmd.Output()
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || convErr != nil || pid <= 1 {
		e.t.Fatalf("pane pid of %s: %v %v %q", f.tmux, err, convErr, out)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		e.t.Fatalf("kill -TERM -- -%d: %v", pid, err)
	}
}

func (e *deliveryEnv) sessionStatus(f *fakeTarget) string {
	e.t.Helper()
	out, _, _ := e.run("", nil, "session", "show", f.id, "--json")
	var shown struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal([]byte(out), &shown)
	return shown.Status
}

func TestIdleChildDeathE2E_ParentIsTold(t *testing.T) {
	t.Parallel()
	e := newRestartEnv(t)
	parent, child := setupIdleChild(t, e)
	time.Sleep(2 * time.Second) // the daemon sees the child idle at its prompt

	e.killPaneGroup(child) // the fake fires SessionEnd: the hook status becomes dead
	issue2530WaitFor(t, 15*time.Second, "child shown as error", func() bool { return e.sessionStatus(child) == "error" })
	death := func(r map[string]any) bool {
		return r["from_status"] == "waiting" && r["to_status"] == "error" && r["tier"] == "urgent"
	}
	e.waitRecords(parent, child, 20*time.Second, func(r []map[string]any) bool {
		for _, rec := range r {
			if death(rec) {
				return true
			}
		}
		return false
	})
	time.Sleep(4 * time.Second) // more daemon passes: still one record
	recs := e.pendingFrom(parent.id, child.id)
	if len(recs) != 1 || !death(recs[0]) {
		t.Fatalf("an idle child that dies gives its parent exactly one urgent waiting -> error record, got %d: %v", len(recs), recs)
	}
	if !strings.Contains(recordText(recs[0]), "exited while idle") {
		t.Fatalf("the record must say the child's process exited, got %q", recordText(recs[0]))
	}
}
