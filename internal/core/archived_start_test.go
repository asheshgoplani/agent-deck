package core

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Starting or restarting an archived session makes it a live session, so it
// must leave the archived view durably. A live session still flagged archived
// is hidden from the active list, mis-counted, and woken by heartbeats as if
// it were parked. A start that fails restores the exact archive timestamp, so
// the session does not change lists without a running process (the TUI's
// restartWithArchiveTransition contract).

// archivedAtForTest is whole seconds: archived_at is stored as a Unix second.
var archivedAtForTest = time.Date(2026, time.October, 1, 9, 30, 0, 0, time.UTC)

func archivedStartHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	quiesceBackgroundWriters(t)
}

func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// newArchivedShell is an archived shell session (Command "" runs the shell).
func newArchivedShell(t *testing.T, prefix string) (profile string, inst *session.Instance) {
	t.Helper()
	profile = fmt.Sprintf("_core_%s_%d", prefix, time.Now().UnixNano())
	inst = session.NewInstance(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()), t.TempDir())
	inst.Command = ""
	inst.Status = session.StatusStopped
	inst.ArchivedAt = archivedAtForTest
	return profile, inst
}

func killStored(t *testing.T, profile, title string) {
	t.Helper()
	t.Cleanup(func() {
		if n := findByTitle(loadStore(t, profile), title); n != nil {
			_ = n.Kill()
		}
	})
}

func rowState(inst *session.Instance) string {
	if inst == nil {
		return "<missing>"
	}
	return fmt.Sprintf("{status=%s archived_at=%v}", inst.Status, inst.ArchivedAt)
}

func TestSessionStartOfArchivedSessionUnarchivesIt(t *testing.T) {
	requireTmux(t)
	archivedStartHome(t)
	profile, inst := newArchivedShell(t, "start_archived")
	seedStore(t, profile, nil, inst)
	killStored(t, profile, inst.Title)

	out, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.Title, NoWait: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Status != StartStatusStarted || !out.Unarchived {
		t.Errorf("out = %+v, want started with Unarchived=true", out)
	}
	got := findByTitle(loadStore(t, profile), inst.Title)
	if got == nil || got.IsArchived() {
		t.Fatalf("stored row after start of an archived session = %s, want archived_at cleared", rowState(got))
	}
}

func TestSessionStartOfActiveSessionDoesNotReportUnarchived(t *testing.T) {
	requireTmux(t)
	archivedStartHome(t)
	profile, inst := newArchivedShell(t, "start_active")
	inst.ArchivedAt = time.Time{}
	seedStore(t, profile, nil, inst)
	killStored(t, profile, inst.Title)

	out, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.Title, NoWait: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Unarchived {
		t.Errorf("out = %+v, want Unarchived=false for a session that was never archived", out)
	}
}

func TestSessionStartOfArchivedSessionRestoresArchiveOnFailure(t *testing.T) {
	archivedStartHome(t)
	profile, inst := newArchivedShell(t, "start_archived_fail")
	inst.Tool = "claude"
	inst.Account = "no-such-account" // Start fails in ValidateAccount
	inst.SetTmuxSessionForTest(nil)
	seedStore(t, profile, nil, inst)

	_, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.Title, NoWait: true})
	if res.Err == nil {
		t.Fatal("start with an unknown account succeeded")
	}
	got := findByTitle(loadStore(t, profile), inst.Title)
	if got == nil || !got.ArchivedAt.Equal(archivedAtForTest) {
		t.Fatalf("stored row after failed start = %s, want archived_at restored to %v", rowState(got), archivedAtForTest)
	}
}

func TestSessionStartOfArchivedSessionAtCapQueuesItUnarchived(t *testing.T) {
	archivedStartHome(t)
	profile := fmt.Sprintf("_core_start_archived_queue_%d", time.Now().UnixNano())
	dir := t.TempDir()
	busy := session.NewInstanceWithGroup("busy", dir, "serial")
	busy.Status = session.StatusRunning
	next := session.NewInstanceWithGroup("next", dir, "serial")
	next.Status = session.StatusStopped
	next.ArchivedAt = archivedAtForTest
	next.SetTmuxSessionForTest(nil)
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, busy, next)

	out, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: "next"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Status != StartStatusQueued || !out.Unarchived {
		t.Errorf("out = %+v, want queued with Unarchived=true", out)
	}
	got := findByTitle(loadStore(t, profile), "next")
	if got == nil || got.Status != session.StatusQueued || got.IsArchived() {
		t.Fatalf("stored row = %s, want queued and not archived (a drain would start it hidden)", rowState(got))
	}
}

func TestSessionRestartOfArchivedSessionUnarchivesIt(t *testing.T) {
	requireTmux(t)
	archivedStartHome(t)
	profile, inst := newArchivedShell(t, "restart_archived")
	seedStore(t, profile, nil, inst)
	killStored(t, profile, inst.Title)

	out, res := Invoke[SessionRestartOut](context.Background(), testRegistry(t, Deps{}), IDSessionRestart, SessionRestartIn{Profile: profile, Session: inst.Title, Force: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if out.Skipped || !out.Unarchived {
		t.Errorf("out = %+v, want restarted with Unarchived=true", out)
	}
	got := findByTitle(loadStore(t, profile), inst.Title)
	if got == nil || got.IsArchived() {
		t.Fatalf("stored row after restart of an archived session = %s, want archived_at cleared", rowState(got))
	}
}

func TestSessionRestartOfArchivedSessionRestoresArchiveOnFailure(t *testing.T) {
	archivedStartHome(t)
	profile, inst := newArchivedShell(t, "restart_archived_fail")
	inst.Tool = "claude"
	inst.Account = "no-such-account" // Restart fails in ValidateAccount
	inst.SetTmuxSessionForTest(nil)
	seedStore(t, profile, nil, inst)

	_, res := Invoke[SessionRestartOut](context.Background(), testRegistry(t, Deps{}), IDSessionRestart, SessionRestartIn{Profile: profile, Session: inst.Title, Force: true})
	if res.Err == nil {
		t.Fatal("restart with an unknown account succeeded")
	}
	got := findByTitle(loadStore(t, profile), inst.Title)
	if got == nil || !got.ArchivedAt.Equal(archivedAtForTest) {
		t.Fatalf("stored row after failed restart = %s, want archived_at restored to %v", rowState(got), archivedAtForTest)
	}
}
