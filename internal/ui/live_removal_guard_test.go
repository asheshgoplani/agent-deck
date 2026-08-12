package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

func runGitGuardTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestRemoveSessionBlocksLiveTmuxDespiteStoppedStatus(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	inst := session.NewInstance("live-removal-guard", t.TempDir())
	inst.Status = session.StatusStopped // stale persisted status: the reported failure shape
	tmuxSess := inst.GetTmuxSession()
	if err := tmuxSess.Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })

	msg := (&Home{}).removeSession(inst)()
	if _, deleted := msg.(sessionDeletedMsg); deleted {
		t.Fatal("registry-only removal deleted a session whose tmux process is still live")
	}
}

func TestRegistryRemovalAbortsWhenQueueTransactionCannotStart(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	runtimeDir := filepath.Join(dataRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(runtimeDir, "runtime-queue-locks")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstance("discard-failure", t.TempDir())
	inst.Status = session.StatusStopped
	msg := registryRemovalMsg(inst)
	if _, ok := msg.(sessionDeleteFailedMsg); !ok {
		t.Fatalf("registryRemovalMsg() = %T, want sessionDeleteFailedMsg", msg)
	}
}

func TestDeleteSessionAbortsWhenQueueTransactionCannotStart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	runtimeDir := filepath.Join(dataRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(runtimeDir, "runtime-queue-locks")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstance("delete-discard-failure", t.TempDir())
	if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
	msg := (&Home{}).deleteSession(inst)()
	if _, ok := msg.(sessionDeleteFailedMsg); !ok {
		t.Fatalf("deleteSession() = %T, want sessionDeleteFailedMsg", msg)
	}
	if !inst.Exists() {
		t.Fatal("queue lock failure killed the live session")
	}
}

func TestArchiveSessionAbortsBeforeKillWhenQueueTransactionCannotStart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	runtimeDir := filepath.Join(dataRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime-queue-locks"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstance("archive-lock-failure", t.TempDir())
	if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
	msg := (&Home{}).archiveSession(inst)()
	archived, ok := msg.(sessionArchivedMsg)
	if !ok || archived.killErr == nil {
		t.Fatalf("archiveSession() = %T %#v", msg, msg)
	}
	if !inst.Exists() {
		t.Fatal("queue lock failure killed the live session")
	}
	if !inst.ArchivedAt.IsZero() {
		t.Fatal("queue lock failure changed ArchivedAt")
	}
}

func TestTUIWorktreeFinishLockFailureLeavesWorkspaceAndBranchUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	runGitGuardTest(t, repo, "init", "-b", "main")
	runGitGuardTest(t, repo, "config", "user.email", "test@example.com")
	runGitGuardTest(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitGuardTest(t, repo, "add", "seed")
	runGitGuardTest(t, repo, "commit", "-m", "seed")
	runGitGuardTest(t, repo, "branch", "feature-lock-guard")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitGuardTest(t, repo, "worktree", "add", worktree, "feature-lock-guard")

	queueRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", queueRoot)
	const id = "tui-worktree-lock-failure"
	if _, err := session.EnqueueRuntimeMessage(id, "preserve"); err != nil {
		t.Fatal(err)
	}
	blockedRoot := t.TempDir()
	runtimeDir := filepath.Join(blockedRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime-queue-locks"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", blockedRoot)
	inst := session.NewInstance("tui-worktree-lock-live", worktree)
	inst.ID = id
	inst.WorktreeRepoRoot, inst.WorktreePath, inst.WorktreeBranch = repo, worktree, "feature-lock-guard"
	if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
	msg := (&Home{}).finishWorktree(inst, id, id, inst.WorktreeBranch, repo, worktree, false, "main", false, false)()
	result, ok := msg.(worktreeFinishResultMsg)
	if !ok || result.err == nil {
		t.Fatalf("finishWorktree() = %T %#v", msg, msg)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree changed on lock failure: %v", err)
	}
	if got := runGitGuardTest(t, repo, "branch", "--list", inst.WorktreeBranch); !strings.Contains(got, inst.WorktreeBranch) {
		t.Fatal("branch deleted on lock failure")
	}
	if !inst.Exists() {
		t.Fatal("queue lock failure killed live TUI worktree session")
	}
	t.Setenv("XDG_DATA_HOME", queueRoot)
	if !session.RuntimeQueueHasPending(id) {
		t.Fatal("queue changed on lock failure")
	}
}

func TestWebDeleteAndArchiveLockFailureKeepsLiveProcessAndDurableState(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, operation := range []string{"delete", "archive"} {
		t.Run(operation, func(t *testing.T) {
			dataRoot := filepath.Join(t.TempDir(), "data")
			t.Setenv("XDG_DATA_HOME", dataRoot)
			h, storage := newHeadlessHomeForTest(t, "_test_web_"+operation+"_lock_failure")
			inst := session.NewInstance("web-"+operation+"-lock-live", t.TempDir())
			inst.ID = "web-" + operation + "-lock-failure"
			if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
				t.Fatal(err)
			}
			if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
				t.Skipf("tmux unavailable: %v", err)
			}
			t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
			if _, err := session.EnqueueRuntimeMessage(inst.ID, "preserve"); err != nil {
				t.Fatal(err)
			}
			blockedRoot := t.TempDir()
			runtimeDir := filepath.Join(blockedRoot, "agent-deck", "runtime")
			if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runtimeDir, "runtime-queue-locks"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_DATA_HOME", blockedRoot)
			m := NewWebMutator(h)
			var err error
			if operation == "delete" {
				err = m.DeleteSession(inst.ID)
			} else {
				err = m.ArchiveSession(inst.ID)
			}
			if err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
			live := h.instanceByID[inst.ID]
			if live == nil || !live.Exists() {
				t.Fatal("queue lock failure killed live process")
			}
			if !inst.ArchivedAt.IsZero() {
				t.Fatal("queue lock failure changed archive state")
			}
			t.Setenv("XDG_DATA_HOME", dataRoot)
			if !session.RuntimeQueueHasPending(inst.ID) {
				t.Fatal("queue changed on lock failure")
			}
			rows, _, loadErr := storage.LoadWithGroups()
			if loadErr != nil || len(rows) != 1 || rows[0].ID != inst.ID {
				t.Fatalf("durable row changed: %#v, %v", rows, loadErr)
			}
		})
	}
}

func TestWebWorktreeFinishLockFailureLeavesWorkspaceBranchRowAndQueue(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	runGitGuardTest(t, repo, "init", "-b", "main")
	runGitGuardTest(t, repo, "config", "user.email", "test@example.com")
	runGitGuardTest(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitGuardTest(t, repo, "add", "seed")
	runGitGuardTest(t, repo, "commit", "-m", "seed")
	runGitGuardTest(t, repo, "branch", "web-feature-lock-guard")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitGuardTest(t, repo, "worktree", "add", worktree, "web-feature-lock-guard")

	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataRoot)
	h, storage := newHeadlessHomeForTest(t, "_test_web_worktree_lock_failure")
	inst := session.NewInstance("web-worktree-lock-live", worktree)
	inst.ID = "web-worktree-lock-failure"
	inst.WorktreeRepoRoot, inst.WorktreePath, inst.WorktreeBranch = repo, worktree, "web-feature-lock-guard"
	if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "preserve"); err != nil {
		t.Fatal(err)
	}
	blockedRoot := t.TempDir()
	runtimeDir := filepath.Join(blockedRoot, "agent-deck", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "runtime-queue-locks"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", blockedRoot)
	if _, err := NewWebMutator(h).FinishWorktree(inst.ID, web.WorktreeFinishOptions{NoMerge: true, Force: true}); err == nil {
		t.Fatal("finish unexpectedly succeeded")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("worktree changed: %v", err)
	}
	if got := runGitGuardTest(t, repo, "branch", "--list", inst.WorktreeBranch); !strings.Contains(got, inst.WorktreeBranch) {
		t.Fatal("branch deleted")
	}
	live := h.instanceByID[inst.ID]
	if live == nil || !live.Exists() {
		t.Fatal("queue lock failure killed live web worktree session")
	}
	t.Setenv("XDG_DATA_HOME", dataRoot)
	if !session.RuntimeQueueHasPending(inst.ID) {
		t.Fatal("queue changed")
	}
	rows, _, err := storage.LoadWithGroups()
	if err != nil || len(rows) != 1 || rows[0].ID != inst.ID {
		t.Fatalf("row changed: %#v, %v", rows, err)
	}
}

func TestRemoveSessionDeletesStoppedTmuxSession(t *testing.T) {
	inst := session.NewInstance("stopped-removal-guard", t.TempDir())
	inst.Status = session.StatusStopped

	msg := (&Home{}).removeSession(inst)()
	deleted, ok := msg.(sessionDeletedMsg)
	if !ok || deleted.deletedID != inst.ID {
		t.Fatalf("removeSession() = %T %#v, want sessionDeletedMsg for %q", msg, msg, inst.ID)
	}
}
