package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// End-to-end tests for #2954: the real binary, an isolated tmux server, a
// long-running notify daemon and the Claude-like fake behind the `claude`
// command of restart_resume_e2e_test.go. About a second after `agent-deck
// launch` a Claude child sits at its empty prompt, before its kickoff reaches
// the transcript. The daemon committed that running -> idle edge with no tier,
// so it counted as urgent: the parent's Stop hook blocked on "<child>: idle"
// while the parent was still in the turn that ran `launch`.
//
// The fake comes up as a fresh Claude does: no transcript until the first
// prompt (.fake-late-transcript), SessionStart a few seconds in. With
// .fake-hold-submit it takes the kickoff into its composer and submits it, but
// writes it to the transcript only once the test removes the file, which pins
// the window the issue reports (the idle edge, then the kickoff 4 s later).

const freshStartLine = "Started; at its prompt, no turn yet."

// stopHook fires the parent's Stop hook (a turn boundary) and returns what the
// hook handler printed: a block decision, or nothing.
func (e *deliveryEnv) stopHook(f *fakeTarget) string {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]string{"hook_event_name": "Stop", "session_id": f.claudeSID, "cwd": f.project})
	out, errOut, code := e.run(string(payload), []string{"AGENTDECK_INSTANCE_ID=" + f.id, "AGENTDECK_PROFILE=" + deliveryE2EProfile}, "hook-handler")
	if code != 0 {
		e.t.Fatalf("hook-handler Stop: exit %d %s", code, errOut)
	}
	return out
}

// launched is a child started by `agent-deck launch`, which may still be
// delivering its kickoff.
type launched struct {
	*fakeTarget
	cmd  *exec.Cmd
	out  strings.Builder
	errb strings.Builder
}

// launchChild runs `agent-deck launch` for a Claude child of parent, the
// markers created in its project first, and returns once the child's pane
// shows its prompt; with message the launch keeps delivering it in the
// background (wait joins it).
func (e *deliveryEnv) launchChild(title string, parent *fakeTarget, message string, markers ...string) *launched {
	e.t.Helper()
	l := &launched{fakeTarget: &fakeTarget{project: filepath.Join(e.root, "p-"+title)}}
	if err := os.MkdirAll(l.project, 0o700); err != nil {
		e.t.Fatal(err)
	}
	writeFile(e.t, filepath.Join(l.project, ".fake-phase"), "idle", 0o600)
	for _, m := range append([]string{".fake-late-transcript"}, markers...) {
		writeFile(e.t, filepath.Join(l.project, m), "", 0o600)
	}
	args := []string{"-p", deliveryE2EProfile, "launch", l.project, "-t", title, "-c", "claude", "--parent", parent.id, "--json"}
	if message != "" {
		args = append(args, "-m", message, "--no-assert-done")
	}
	l.cmd = exec.Command(e.bin, args...)
	l.cmd.Env = append([]string{}, e.env...)
	l.cmd.Stdout, l.cmd.Stderr = &l.out, &l.errb
	if err := l.cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = l.cmd.Process.Kill(); _ = l.cmd.Wait() })
	issue2530WaitFor(e.t, 20*time.Second, title+" in the session list", func() bool {
		out, _, _ := e.run("", nil, "list", "--json")
		var rows []struct{ ID, Title string }
		_ = json.Unmarshal([]byte(out), &rows)
		for _, r := range rows {
			if r.Title == title {
				l.id = r.ID
			}
		}
		return l.id != ""
	})
	issue2530WaitFor(e.t, 20*time.Second, title+" fake to start", func() bool {
		b, err := os.ReadFile(filepath.Join(l.project, ".fake", "transcript"))
		l.transcript = string(b)
		return err == nil && l.transcript != ""
	})
	var shown struct {
		TmuxSession string `json:"tmux_session"`
		ClaudeSID   string `json:"claude_session_id"`
	}
	issue2530WaitFor(e.t, 20*time.Second, title+" tmux session", func() bool {
		out, _, _ := e.run("", nil, "session", "show", l.id, "--json")
		return json.Unmarshal([]byte(out), &shown) == nil && shown.TmuxSession != "" && shown.ClaudeSID != ""
	})
	l.tmux, l.claudeSID = shown.TmuxSession, shown.ClaudeSID
	e.waitPane(l.fakeTarget, title+" first frame", func(p string) bool { return strings.Contains(p, "All four steps ran.") && strings.Contains(p, "❯") })
	return l
}

// wait joins the launch and fails on a non-zero exit.
func (l *launched) wait(t *testing.T) {
	t.Helper()
	if err := l.cmd.Wait(); err != nil {
		t.Fatalf("launch: %v\n%s\n%s", err, l.out.String(), l.errb.String())
	}
}

// held reports whether the fake took text into its composer and submitted it
// (held back from the transcript by .fake-hold-submit).
func (l *launched) held(text string) bool {
	b, _ := os.ReadFile(filepath.Join(l.project, ".fake", "held"))
	return strings.Contains(string(b), text)
}

// wantQuietBoot checks that the parent holds the boot's info line and
// nothing urgent from the child, and that its Stop hook does not block.
func (e *deliveryEnv) wantQuietBoot(parent *fakeTarget, child *fakeTarget, when string) {
	e.t.Helper()
	recs := e.pendingFrom(parent.id, child.id)
	if len(recs) != 1 || recs[0]["tier"] != "info" || recordText(recs[0]) != freshStartLine {
		e.t.Fatalf("%s: want exactly the boot's info line, got %v", when, recs)
	}
	if out := e.stopHook(parent); strings.Contains(out, "block") {
		e.t.Fatalf("%s: the parent's Stop must not block on the child's boot, got %s", when, out)
	}
}

func urgentWith(text string) func([]map[string]any) bool {
	return func(r []map[string]any) bool {
		for _, rec := range r {
			raw, _ := json.Marshal(rec)
			if rec["tier"] != "info" && strings.Contains(string(raw), text) {
				return true
			}
		}
		return false
	}
}

// The issue's sequence through the real `launch -m`: the child reaches its
// prompt, the daemon sees it there before the kickoff is in the transcript,
// then the kickoff's turn runs and ends. The boot is one info line and blocks
// nothing; the kickoff's own turn still blocks the parent's Stop with its
// reply.
func TestIssue2954_LaunchBootIsQuietKickoffTurnIsUrgent(t *testing.T) {
	t.Parallel()
	e := newRestartEnv(t)
	parent := e.startFake("parent", "idle", "")
	kickoff := deliveryToken("KICKOFF")
	child := e.launchChild("child", parent, kickoff+" reply pong", ".fake-hold-submit")
	issue2530WaitFor(t, 30*time.Second, "the launch to type the kickoff", func() bool { return child.held(kickoff) })
	e.waitRecords(parent, child.fakeTarget, 20*time.Second, func(r []map[string]any) bool { return len(r) >= 1 })
	time.Sleep(2 * time.Second) // a negative check: nothing more arrives over the next passes
	e.wantQuietBoot(parent, child.fakeTarget, "before the kickoff reached the transcript")

	// The kickoff reaches the transcript and its turn runs.
	if err := os.Remove(filepath.Join(child.project, ".fake-hold-submit")); err != nil {
		t.Fatal(err)
	}
	e.setPhase(child.fakeTarget, "busy")
	child.wait(t)
	e.waitStatus(child.fakeTarget, "running")
	done := deliveryToken("KICKOFF_DONE")
	child.reply(t, "pong\n\n===AGENTDECK_DONE=== status=ok summary="+done)
	e.setPhase(child.fakeTarget, "idle")
	e.waitRecords(parent, child.fakeTarget, 20*time.Second, urgentWith(done))
	if out := e.stopHook(parent); !strings.Contains(out, "block") || !strings.Contains(out, done) {
		t.Fatalf("the kickoff's turn must block the parent's Stop with its reply, got %q", out)
	}
}

// heartbeatTUI registers the test process as a live TUI in the profile's
// state DB until cleanup, so the daemon takes statuses from the DB rows and
// reads no pane, as with the TUI running. It returns the DB to write the rows
// the TUI would write.
func (e *deliveryEnv) heartbeatTUI() *statedb.StateDB {
	e.t.Helper()
	matches, _ := filepath.Glob(filepath.Join(e.root, "*", "*", "profiles", deliveryE2EProfile, "state.db"))
	more, _ := filepath.Glob(filepath.Join(e.root, "*", "*", "*", "profiles", deliveryE2EProfile, "state.db"))
	matches = append(matches, more...)
	if len(matches) != 1 {
		e.t.Fatalf("want one state.db for the profile, got %v", matches)
	}
	db, err := statedb.Open(matches[0])
	if err != nil {
		e.t.Fatal(err)
	}
	if err := db.RegisterInstance(false); err != nil {
		e.t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = db.Heartbeat()
			}
		}
	}()
	e.t.Cleanup(func() {
		close(stop)
		wg.Wait()
		_ = db.UnregisterInstance()
		_ = db.Close()
	})
	return db
}

// With the TUI running the daemon has no cached substate for the child: it
// reads the pane of the token holder, which shows the empty prompt. The test
// writes the row the TUI would for that pane; the launch leaves it starting.
func TestIssue2954_LaunchBootWithTUIStatusesIsQuiet(t *testing.T) {
	t.Parallel()
	e := newRestartEnv(t)
	parent := e.startFake("parent", "idle", "")
	db := e.heartbeatTUI()
	child := e.launchChild("child", parent, "")
	child.wait(t)
	if err := db.WriteStatus(child.id, "waiting", "claude"); err != nil {
		t.Fatal(err)
	}
	e.waitRecords(parent, child.fakeTarget, 20*time.Second, func(r []map[string]any) bool { return len(r) >= 1 })
	time.Sleep(2 * time.Second) // a negative check: nothing more arrives over the next passes
	e.wantQuietBoot(parent, child.fakeTarget, "with TUI statuses")
	// Precondition: the daemon was in TUI mode. Probing panes itself, it
	// would write the pane's idle over a row the TUI set to running.
	if err := db.WriteStatus(child.id, "running", "claude"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // a negative check: a probing daemon rewrites the row within a pass
	if rows, err := db.ReadAllStatuses(); err != nil || rows[child.id].Status != "running" {
		t.Fatalf("precondition: the daemon must take statuses from the DB, row %+v (%v)", rows[child.id], err)
	}
}

// waitStatus waits until the session's status is want.
func (e *deliveryEnv) waitStatus(f *fakeTarget, want string) {
	e.t.Helper()
	issue2530WaitFor(e.t, 20*time.Second, f.id+" "+want, func() bool { return e.sessionStatus(f) == want })
}
