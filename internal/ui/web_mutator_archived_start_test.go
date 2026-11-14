package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A web start or restart of an archived session must unarchive it durably,
// like the CLI and the TUI: a live session left flagged archived is hidden
// from the active list. A failed start restores the exact archive timestamp.

var webArchivedAt = time.Date(2026, time.October, 1, 9, 30, 0, 0, time.UTC)

func seedArchivedWebSession(t *testing.T, storage *session.Storage, title, account string) *session.Instance {
	t.Helper()
	inst := session.NewInstance(title, t.TempDir())
	inst.Command = ""
	inst.Status = session.StatusStopped
	inst.ArchivedAt = webArchivedAt
	if account != "" {
		inst.Tool = "claude"
		inst.Account = account // the start fails in ValidateAccount
	}
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}
	return inst
}

func storedArchivedAt(t *testing.T, storage *session.Storage, id string) time.Time {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, inst := range instances {
		if inst.ID == id {
			return inst.ArchivedAt
		}
	}
	t.Fatalf("session %s missing from storage", id)
	return time.Time{}
}

func killWebSessionOnCleanup(t *testing.T, h *Home, id string) {
	t.Helper()
	t.Cleanup(func() {
		h.instancesMu.RLock()
		inst := h.instanceByID[id]
		h.instancesMu.RUnlock()
		if inst != nil {
			_ = inst.Kill()
		}
	})
}

func TestWebMutatorStartOfArchivedSessionUnarchivesIt(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_start_archived_%d", time.Now().UnixNano()))
	inst := seedArchivedWebSession(t, storage, "web-start-archived", "")
	killWebSessionOnCleanup(t, h, inst.ID)

	if err := NewWebMutator(h).StartSession(inst.ID); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if got := storedArchivedAt(t, storage, inst.ID); !got.IsZero() {
		t.Fatalf("stored archived_at after web start = %v, want cleared", got)
	}
}

func TestWebMutatorRestartOfArchivedSessionUnarchivesIt(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_restart_archived_%d", time.Now().UnixNano()))
	inst := seedArchivedWebSession(t, storage, "web-restart-archived", "")
	killWebSessionOnCleanup(t, h, inst.ID)

	if err := NewWebMutator(h).RestartSession(inst.ID); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	if got := storedArchivedAt(t, storage, inst.ID); !got.IsZero() {
		t.Fatalf("stored archived_at after web restart = %v, want cleared", got)
	}
}

func TestWebMutatorFailedStartOfArchivedSessionKeepsArchive(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, fmt.Sprintf("_test_web_start_archived_fail_%d", time.Now().UnixNano()))
	inst := seedArchivedWebSession(t, storage, "web-start-archived-fail", "no-such-account")

	if err := NewWebMutator(h).StartSession(inst.ID); err == nil {
		t.Fatal("StartSession with an unknown account succeeded")
	}
	if got := storedArchivedAt(t, storage, inst.ID); !got.Equal(webArchivedAt) {
		t.Fatalf("stored archived_at after failed web start = %v, want restored to %v", got, webArchivedAt)
	}
}
