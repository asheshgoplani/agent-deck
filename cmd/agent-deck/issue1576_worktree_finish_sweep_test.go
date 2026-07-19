package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression test for issue #1576: `agent-deck worktree finish` removed the
// session row from SQLite but never swept the transition-notifier artifacts
// (per-conductor inbox JSONL lines + the runtime/transition-notify-state.json
// dedup ledger). Orphan records for finished sessions kept re-firing stale
// [EVENT] deliveries to parent conductors. `agent-deck rm` / `session remove`
// have done this sweep since #910; worktree finish must do the same.
func TestIssue1576_WorktreeFinishSweepsNotifierState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENT_DECK_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	session.ResetInboxFingerprintCacheForTest()

	const profile = "wtfinish_1576"

	// --- Real git repo + worktree (worktree finish drives git for real). ---
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	wtPath := filepath.Join(home, "wt-1576")
	git("worktree", "add", wtPath, "-b", "wt-branch-1576")

	// --- Session registered against that worktree. ---
	inst := session.NewInstance("wt-finish-1576", wtPath)
	inst.WorktreePath = wtPath
	inst.WorktreeRepoRoot = repo
	inst.WorktreeBranch = "wt-branch-1576"

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	instances := []*session.Instance{inst}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	// --- Seed the notifier artifacts the finish must sweep. ---
	// 1. Conductor inbox: one line for the doomed session, one survivor.
	const conductor = "conductor-1576"
	mkEvent := func(child string) session.TransitionNotificationEvent {
		return session.TransitionNotificationEvent{
			ChildSessionID:  child,
			ChildTitle:      "worker",
			Profile:         profile,
			FromStatus:      "running",
			ToStatus:        "waiting",
			Timestamp:       time.Now(),
			TargetSessionID: conductor,
			TargetKind:      "conductor",
			DeliveryResult:  "deferred_target_busy",
		}
	}
	if err := session.WriteInboxEvent(conductor, mkEvent(inst.ID)); err != nil {
		t.Fatalf("WriteInboxEvent(doomed): %v", err)
	}
	if err := session.WriteInboxEvent(conductor, mkEvent("child-survivor")); err != nil {
		t.Fatalf("WriteInboxEvent(survivor): %v", err)
	}

	// 2. Dedup ledger: a record for the doomed session and a survivor.
	statePath, err := agentpaths.EffectiveDataPath(
		filepath.Join("runtime", "transition-notify-state.json"), "runtime")
	if err != nil {
		t.Fatalf("EffectiveDataPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	ledger := map[string]any{
		"records": map[string]any{
			inst.ID:          map[string]any{"from": "running", "to": "waiting", "at": time.Now().Unix()},
			"child-survivor": map[string]any{"from": "running", "to": "idle", "at": time.Now().Unix()},
		},
	}
	raw, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	if err := os.WriteFile(statePath, raw, 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	// --- Finish the worktree session (no merge, keep branch, no prompt). ---
	handleWorktreeFinish(profile, []string{"wt-finish-1576", "--no-merge", "--force", "--keep-branch"})

	// --- Ledger: doomed record gone, survivor intact. ---
	gotState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read ledger after finish: %v", err)
	}
	var after struct {
		Records map[string]json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(gotState, &after); err != nil {
		t.Fatalf("unmarshal ledger after finish: %v", err)
	}
	if _, present := after.Records[inst.ID]; present {
		t.Errorf("issue #1576: notify-state record for finished session %s still in ledger:\n%s", inst.ID, gotState)
	}
	if _, present := after.Records["child-survivor"]; !present {
		t.Errorf("survivor notify-state record was dropped:\n%s", gotState)
	}

	// --- Inbox: doomed line gone, survivor intact. ---
	inboxData, err := os.ReadFile(session.InboxPathFor(conductor))
	if err != nil {
		t.Fatalf("read conductor inbox after finish: %v", err)
	}
	if strings.Contains(string(inboxData), inst.ID) {
		t.Errorf("issue #1576: inbox still holds event(s) for finished session %s:\n%s", inst.ID, inboxData)
	}
	if !strings.Contains(string(inboxData), "child-survivor") {
		t.Errorf("survivor inbox event was swept:\n%s", inboxData)
	}
}
