package ui

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Defect 317 on the TUI close, TUI archive and web stop paths: they kill the
// pane and leave stopped to a later full save (or, for the web, to nothing).
// The notify daemon's error sample of that kill, committed in between, made
// the save abort on its external-change guard, so the row stayed error and
// the idle-death watch (#2806) told the parent its child had died.
//
// session.StopAfterKillHook commits that sample between the kill and the
// stop's own write, against a real tmux session.

func defect317UIHome(t *testing.T, profile string) (*Home, *session.Storage, *session.Instance) {
	return defect317UIHomeWithSample(t, profile, session.StatusError)
}

// defect317UIHomeWithSample commits sample between the kill and the stop's
// write.
func defect317UIHomeWithSample(t *testing.T, profile string, sample session.Status) (*Home, *session.Storage, *session.Instance) {
	t.Helper()
	skipIfNoTmuxBinaryUI(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	h, storage := newRestartSaveHome(t, profile)
	target := h.instances[1]
	if err := target.Restart(); err != nil {
		t.Fatalf("start target: %v", err)
	}
	t.Cleanup(func() {
		if sess := target.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})
	target.Status = session.StatusIdle
	h.forceSaveInstances()
	if got := defect317RowStatus(t, storage, target.ID); got != string(session.StatusIdle) {
		t.Fatalf("baseline row status = %q, want idle", got)
	}
	session.StopAfterKillHook = func(id string) {
		if err := storage.GetDB().WriteStatus(id, string(sample), "shell"); err != nil {
			t.Errorf("concurrent %s write: %v", sample, err)
		}
	}
	t.Cleanup(func() { session.StopAfterKillHook = nil })
	return h, storage, target
}

func defect317RowStatus(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	row, err := storage.GetDB().LoadInstanceByID(id)
	if err != nil || row == nil {
		t.Fatalf("load row %s: %v", id, err)
	}
	return row.Status
}

func TestDefect317_TUICloseSavesStoppedOverAConcurrentErrorSample(t *testing.T) {
	h, storage, target := defect317UIHome(t, "_defect317_tuiclose")
	msg := h.closeSession(target)()
	h.Update(msg)
	if got := defect317RowStatus(t, storage, target.ID); got != string(session.StatusStopped) {
		t.Fatalf("row status after TUI close = %q, want stopped", got)
	}
}

func TestDefect317_TUIArchiveSavesStoppedOverAConcurrentErrorSample(t *testing.T) {
	h, storage, target := defect317UIHome(t, "_defect317_tuiarchive")
	msg := h.archiveSession(target)()
	h.Update(msg)
	if got := defect317RowStatus(t, storage, target.ID); got != string(session.StatusStopped) {
		t.Fatalf("row status after TUI archive = %q, want stopped", got)
	}
}

func TestDefect317_WebStopSavesStoppedOverAConcurrentErrorSample(t *testing.T) {
	h, storage, target := defect317UIHome(t, "_defect317_webstop")
	if err := (&WebMutator{h: h}).StopSession(target.ID); err != nil {
		t.Fatalf("web StopSession: %v", err)
	}
	if got := defect317RowStatus(t, storage, target.ID); got != string(session.StatusStopped) {
		t.Fatalf("row status after web stop = %q, want stopped", got)
	}
}

// The field failure (deckbox, rc.3): running committed inside the kill window
// with no pane left alive is not a restart; the web stop still saves stopped.
func TestDefect317_WebStopSavesStoppedOverAConcurrentRunningSampleOfADeadPane(t *testing.T) {
	h, storage, target := defect317UIHomeWithSample(t, "_defect317_webstop_running", session.StatusRunning)
	if err := (&WebMutator{h: h}).StopSession(target.ID); err != nil {
		t.Fatalf("web StopSession: %v", err)
	}
	if got := defect317RowStatus(t, storage, target.ID); got != string(session.StatusStopped) {
		t.Fatalf("row status after web stop = %q, want stopped", got)
	}
}
