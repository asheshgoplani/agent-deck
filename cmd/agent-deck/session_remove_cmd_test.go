package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func assertHelperPersistedLiveSessions(t *testing.T, profile string, ids ...string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	instances, _, err := storage.LoadWithGroups()
	_ = storage.Close()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}
	for _, id := range ids {
		inst := byID[id]
		if inst == nil || inst.GetTmuxSession() == nil || !inst.Exists() {
			t.Fatalf("helper cannot observe persisted live tmux for %s: %#v", id, inst)
		}
	}
}

func runtimeQueueLockRootForTest(id string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(session.RuntimeQueuePathFor(id))), "runtime", "runtime-queue-locks")
}

func persistLiveCLIInstance(t *testing.T, storage *session.Storage, id, path string, order int) *session.Instance {
	t.Helper()
	inst := session.NewInstance(id+"-live", path)
	inst.ID, inst.Order, inst.Status = id, order, session.StatusError
	if err := inst.GetTmuxSession().Start("sleep 60"); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
	return inst
}

func TestSessionRemoveQueueLockFailurePreservesLiveProcessRowQueueAndWorktree(t *testing.T) {
	const profile = "_test_single_remove_lock_failure"
	const id = "single-remove-lock-failure"
	if os.Getenv("AGENT_DECK_SINGLE_REMOVE_LOCK_HELPER") == "1" {
		assertHelperPersistedLiveSessions(t, profile, id)
		handleSessionRemove(profile, []string{id, "--force", "--prune-worktree", "--json"})
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed")
	run("commit", "-m", "seed")
	run("branch", "remove-lock-branch")
	worktree := filepath.Join(t.TempDir(), "worktree")
	run("worktree", "add", worktree, "remove-lock-branch")
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	inst := persistLiveCLIInstance(t, storage, id, worktree, 0)
	inst.WorktreeRepoRoot, inst.WorktreePath, inst.WorktreeBranch = repo, worktree, "remove-lock-branch"
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	if _, err := session.EnqueueRuntimeMessage(id, "preserve"); err != nil {
		t.Fatal(err)
	}
	lockRoot := runtimeQueueLockRootForTest(id)
	if err := os.RemoveAll(lockRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockRoot, []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionRemoveQueueLockFailurePreservesLiveProcessRowQueueAndWorktree$")
	cmd.Env = append(os.Environ(), "AGENT_DECK_TASK6_HELPER_PROCESS=1", "AGENT_DECK_QUEUE_HANDLER=1", "AGENT_DECK_SINGLE_REMOVE_LOCK_HELPER=1")
	output, childErr := cmd.CombinedOutput()
	if childErr == nil {
		t.Fatalf("remove succeeded: %s", output)
	}
	outputText := string(output)
	if !strings.Contains(outputText, "failed to lock runtime queue for "+id) || strings.Contains(outputText, "failed to discard runtime queue") || !strings.Contains(outputText, "runtime-queue-locks") {
		t.Fatalf("single remove lock error lost operation, identity, or wrapped cause: %v\n%s", childErr, output)
	}
	if !inst.Exists() {
		t.Fatal("lock failure killed live process")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("lock failure pruned worktree: %v", err)
	}
	verify, _ := session.NewStorageWithProfile(profile)
	rows, _, loadErr := verify.LoadWithGroups()
	_ = verify.Close()
	if loadErr != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("row changed: %#v, %v", rows, loadErr)
	}
	if !session.RuntimeQueueHasPending(id) {
		t.Fatal("queue changed")
	}
}

func TestSessionBulkRemoveQueueLockFailureFinalizesPrefixAndPreservesRemainder(t *testing.T) {
	const profile = "_test_bulk_remove_lock_failure"
	ids := []string{"bulk-prefix", "bulk-failing", "bulk-unattempted"}
	if os.Getenv("AGENT_DECK_BULK_REMOVE_LOCK_HELPER") == "1" {
		assertHelperPersistedLiveSessions(t, profile, ids...)
		handleSessionRemove(profile, []string{"--all-errored", "--force", "--json"})
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	instances := make([]*session.Instance, 0, len(ids))
	for i, id := range ids {
		inst := persistLiveCLIInstance(t, storage, id, t.TempDir(), i)
		instances = append(instances, inst)
		if _, err := session.EnqueueRuntimeMessage(id, "preserve"); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTree(instances)); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	lockRoot := runtimeQueueLockRootForTest(ids[1])
	if err := os.MkdirAll(lockRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	failingLock := filepath.Join(lockRoot, ids[1]+".lock")
	if err := os.Remove(failingLock); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(failingLock, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionBulkRemoveQueueLockFailureFinalizesPrefixAndPreservesRemainder$")
	cmd.Env = append(os.Environ(), "AGENT_DECK_TASK6_HELPER_PROCESS=1", "AGENT_DECK_QUEUE_HANDLER=1", "AGENT_DECK_BULK_REMOVE_LOCK_HELPER=1")
	output, childErr := cmd.CombinedOutput()
	if childErr == nil {
		t.Fatalf("bulk remove succeeded: %s", output)
	}
	outputText := string(output)
	if !strings.Contains(outputText, "failed to lock runtime queue for "+ids[1]) || strings.Contains(outputText, "failed to discard runtime queue") || !strings.Contains(outputText, ids[1]+".lock") {
		t.Fatalf("bulk remove lock error lost operation, identity, or wrapped cause: %v\n%s", childErr, output)
	}
	if instances[0].Exists() {
		t.Fatal("committed prefix process was not finalized")
	}
	for i := 1; i < len(instances); i++ {
		if !instances[i].Exists() {
			t.Fatalf("%s process was killed", ids[i])
		}
		if !session.RuntimeQueueHasPending(ids[i]) {
			t.Fatalf("%s queue changed", ids[i])
		}
	}
	if session.RuntimeQueueHasPending(ids[0]) {
		t.Fatal("committed prefix queue survived")
	}
	verify, _ := session.NewStorageWithProfile(profile)
	rows, _, loadErr := verify.LoadWithGroups()
	_ = verify.Close()
	if loadErr != nil || len(rows) != 2 {
		t.Fatalf("rows after prefix finalization: %#v, %v", rows, loadErr)
	}
	remainingIDs := map[string]bool{}
	for _, row := range rows {
		remainingIDs[row.ID] = true
	}
	if remainingIDs[ids[0]] || !remainingIDs[ids[1]] || !remainingIDs[ids[2]] {
		t.Fatalf("wrong durable rows after prefix finalization: %#v", remainingIDs)
	}
}

func TestSessionRemoveCommandPersistenceFailurePreservesLifecycleState(t *testing.T) {
	if os.Getenv("AGENT_DECK_REMOVE_PERSIST_HELPER") == "single" {
		original := sessionRemovePersist
		sessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree, token string) error {
			_ = storage.Close()
			return storage.RemoveSessionAndVerify(id, remaining, tree, token)
		}
		defer func() { sessionRemovePersist = original }()
		handleSessionRemove("ch_support_test", []string{os.Getenv("AGENT_DECK_REMOVE_PERSIST_ID"), "--json"})
		return
	}
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	dataRoot := filepath.Join(home, ".local", "share")
	t.Setenv("XDG_DATA_HOME", dataRoot)
	id := addTestSession(t, home, filepath.Join(home, "project"), "remove-persist-failure")
	forceSetStatus(t, home, id, session.StatusStopped)
	_, pendingToken := seedRuntimeQueueLifecycleState(t, id)
	completionPath := filepath.Join(dataRoot, "agent-deck", "runtime", "runtime-queue-completed", id+".json")

	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionRemoveCommandPersistenceFailurePreservesLifecycleState$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"AGENT_DECK_REMOVE_PERSIST_HELPER=single",
		"AGENT_DECK_REMOVE_PERSIST_ID="+id,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+dataRoot,
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("session remove unexpectedly succeeded with persistence failure")
	}
	if list := readSessionsJSON(t, home); !strings.Contains(list, id) {
		t.Fatalf("failed removal lost durable row: %s", list)
	}
	if batch, err := session.StageRuntimeQueue(id); err != nil || batch.Token != pendingToken {
		t.Fatalf("failed removal lost active/WAL state: %#v, %v", batch, err)
	}
	if _, err := os.Stat(completionPath); err != nil {
		t.Fatalf("failed removal lost completion state: %v", err)
	}
	probe, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatalf("failed command retained queue transaction: %v", err)
	}
	probe.Release()
}

func TestSessionRemoveCommitFailurePreservesQueueAndLifecycleState(t *testing.T) {
	if os.Getenv("AGENT_DECK_REMOVE_COMMIT_FAILURE_HELPER") == "1" {
		handleSessionRemove("ch_support_test", []string{os.Getenv("AGENT_DECK_REMOVE_PERSIST_ID"), "--json"})
		return
	}
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(path, info.Mode()|0700)
			}
			return nil
		})
	})
	dataRoot := filepath.Join(home, ".local", "share")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	const id = "commit-failure"
	inst := &session.Instance{ID: id, Title: id, ProjectPath: filepath.Join(home, "project"), GroupPath: session.DefaultGroupPath, Tool: "shell", Command: "shell", Status: session.StatusStopped}
	storage, err := session.NewStorageWithProfile("ch_support_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	_, pendingToken := seedRuntimeQueueLifecycleState(t, id)
	if err := session.SaveQueuedMessage(id, "lifecycle sentinel"); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB().DB()
	for _, stmt := range []string{
		`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE commit_child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER fail_tombstone_commit AFTER INSERT ON instance_tombstones BEGIN INSERT INTO commit_child(parent_id) VALUES (999); END`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = storage.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionRemoveCommitFailurePreservesQueueAndLifecycleState$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"AGENT_DECK_REMOVE_COMMIT_FAILURE_HELPER=1",
		"AGENT_DECK_REMOVE_PERSIST_ID="+id,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+dataRoot,
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("remove unexpectedly succeeded after tombstone transaction commit failure")
	}
	if list := readSessionsJSON(t, home); !strings.Contains(list, id) {
		t.Fatalf("commit failure lost durable row: %s", list)
	}
	if batch, err := session.StageRuntimeQueue(id); err != nil || batch.Token != pendingToken {
		t.Fatalf("commit failure changed runtime queue: %#v, %v", batch, err)
	}
	if got, ok := session.PeekQueuedMessage(id); !ok || got != "lifecycle sentinel" {
		t.Fatalf("commit failure ran lifecycle cleanup: %q, %v", got, ok)
	}
}

func TestSessionRemoveAllErroredPersistenceFailureDiscardsOnlyCommittedQueues(t *testing.T) {
	if os.Getenv("AGENT_DECK_REMOVE_PERSIST_HELPER") == "bulk" {
		failedID := os.Getenv("AGENT_DECK_REMOVE_PERSIST_ID")
		original := bulkSessionRemovePersist
		bulkSessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree, token string) error {
			if id == failedID {
				return errors.New("forced later-item persistence failure")
			}
			return storage.RemoveSessionAndVerify(id, remaining, tree, token)
		}
		defer func() { bulkSessionRemovePersist = original }()
		handleSessionRemove("ch_support_test", []string{"--all-errored", "--json"})
		return
	}
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	dataRoot := filepath.Join(home, ".local", "share")
	t.Setenv("XDG_DATA_HOME", dataRoot)
	committedID := addTestSession(t, home, filepath.Join(home, "committed"), "bulk-committed-first")
	failedID := addTestSession(t, home, filepath.Join(home, "failed"), "bulk-failed-second")
	forceSetStatus(t, home, committedID, session.StatusError)
	forceSetStatus(t, home, failedID, session.StatusError)
	_, _ = seedRuntimeQueueLifecycleState(t, committedID)
	_, failedToken := seedRuntimeQueueLifecycleState(t, failedID)
	if err := session.SaveQueuedMessage(committedID, "cleanup committed prefix"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionRemoveAllErroredPersistenceFailureDiscardsOnlyCommittedQueues$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"AGENT_DECK_REMOVE_PERSIST_HELPER=bulk",
		"AGENT_DECK_REMOVE_PERSIST_ID="+failedID,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+dataRoot,
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("bulk removal unexpectedly succeeded with per-session persistence failure")
	}
	list := readSessionsJSON(t, home)
	if strings.Contains(list, committedID) || !strings.Contains(list, failedID) {
		t.Fatalf("bulk durable rows do not match committed prefix: %s", list)
	}
	if batch, err := session.StageRuntimeQueue(committedID); err != nil || batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("durably deleted prefix retained an orphan queue: %#v, %v", batch, err)
	}
	if _, ok := session.PeekQueuedMessage(committedID); ok {
		t.Fatal("durably deleted prefix skipped lifecycle cleanup")
	}
	if batch, err := session.StageRuntimeQueue(failedID); err != nil || batch.Token != failedToken {
		t.Fatalf("failed queue was discarded: %#v, %v", batch, err)
	}
}

func TestSessionRemoveAllErroredTerminalVerification(t *testing.T) {
	mode := os.Getenv("AGENT_DECK_REMOVE_FINAL_HELPER")
	if mode != "" {
		original := bulkSessionReverifyPersist
		defer func() { bulkSessionReverifyPersist = original }()
		switch mode {
		case "resurrect":
			storage, err := session.NewStorageWithProfile("ch_support_test")
			if err != nil {
				t.Fatal(err)
			}
			instances, groups, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			_ = storage.Close()
			resurrected := false
			bulkSessionReverifyPersist = func(s *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree, token string) error {
				if err := original(s, id, remaining, tree, token); err != nil {
					return err
				}
				if !resurrected {
					resurrected = true
					return s.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups))
				}
				return nil
			}
		case "observe-fail":
			bulkObserveAbsent = func(*session.Storage, []string, []string, func() error) (bool, error) {
				return false, errors.New("forced atomic observation failure")
			}
		case "persistent-resurrection":
			storage, err := session.NewStorageWithProfile("ch_support_test")
			if err != nil {
				t.Fatal(err)
			}
			instances, groups, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			_ = storage.Close()
			bulkObserveAbsent = func(s *session.Storage, _ []string, _ []string, _ func() error) (bool, error) {
				if err := s.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
					return false, err
				}
				return false, nil
			}
		case "discard-fail":
			calls := 0
			bulkQueueDiscard = func(tx *session.RuntimeQueueTransaction) error {
				calls++
				if calls == 2 {
					return errors.New("forced second queue discard failure")
				}
				return tx.Discard()
			}
		}
		handleSessionRemove("ch_support_test", []string{"--all-errored", "--json"})
		return
	}
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}

	for _, tc := range []struct {
		mode        string
		wantSuccess bool
		wantQueues  []bool
		wantCleanup bool
		wantRows    int
	}{
		{mode: "resurrect", wantSuccess: true, wantQueues: []bool{false, false}, wantCleanup: true},
		{mode: "observe-fail", wantQueues: []bool{true, true}},
		{mode: "persistent-resurrection", wantQueues: []bool{true, true}},
		{mode: "discard-fail", wantQueues: []bool{false, true}, wantCleanup: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			home := t.TempDir()
			dataRoot := filepath.Join(home, ".local", "share")
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", dataRoot)
			t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
			first := &session.Instance{ID: "terminal-first", Title: "terminal-first", ProjectPath: filepath.Join(home, "first"), GroupPath: session.DefaultGroupPath, Tool: "shell", Command: "shell", Status: session.StatusError}
			second := &session.Instance{ID: "terminal-second", Title: "terminal-second", ProjectPath: filepath.Join(home, "second"), GroupPath: session.DefaultGroupPath, Tool: "shell", Command: "shell", Status: session.StatusError}
			instances := []*session.Instance{first, second}
			storage, err := session.NewStorageWithProfile("ch_support_test")
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.SaveWithGroups(instances, session.NewGroupTree(instances)); err != nil {
				t.Fatal(err)
			}
			_ = storage.Close()
			for _, id := range []string{first.ID, second.ID} {
				_, _ = seedRuntimeQueueLifecycleState(t, id)
				if err := session.SaveQueuedMessage(id, "cleanup sentinel"); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestSessionRemoveAllErroredTerminalVerification$")
			cmd.Env = append(os.Environ(),
				"AGENT_DECK_TASK6_HELPER_PROCESS=1",
				"AGENT_DECK_REMOVE_FINAL_HELPER="+tc.mode,
				"HOME="+home,
				"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
				"XDG_DATA_HOME="+dataRoot,
				"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
			)
			err = cmd.Run()
			if tc.wantSuccess && err != nil {
				t.Fatalf("terminal resurrection recovery failed: %v", err)
			}
			if !tc.wantSuccess && err == nil {
				t.Fatal("terminal reverification failure returned success")
			}

			storage, err = session.NewStorageWithProfile("ch_support_test")
			if err != nil {
				t.Fatal(err)
			}
			rows, _, loadErr := storage.LoadWithGroups()
			_ = storage.Close()
			if loadErr != nil || len(rows) != tc.wantRows {
				t.Fatalf("terminal rows = %#v, %v; want %d", rows, loadErr, tc.wantRows)
			}
			for i, id := range []string{first.ID, second.ID} {
				if got := session.RuntimeQueueHasPending(id); got != tc.wantQueues[i] {
					t.Fatalf("queue pending for %s = %v, want %v", id, got, tc.wantQueues[i])
				}
				probe, probeErr := session.BeginRuntimeQueueTransaction(id)
				if probeErr != nil {
					t.Fatalf("terminal path retained queue transaction for %s: %v", id, probeErr)
				}
				probe.Release()
				_, cleanupPending := session.PeekQueuedMessage(id)
				if cleanupPending == tc.wantCleanup {
					t.Fatalf("cleanup pending for %s = %v, want %v", id, cleanupPending, !tc.wantCleanup)
				}
			}
		})
	}
}

func seedRuntimeQueueLifecycleState(t *testing.T, id string) (completedToken, pendingToken string) {
	t.Helper()
	if _, err := session.EnqueueRuntimeMessage(id, "completed"); err != nil {
		t.Fatal(err)
	}
	completed, err := session.StageRuntimeQueue(id)
	if err != nil {
		t.Fatal(err)
	}
	lease, valid, err := session.BeginRuntimeQueueSubmission(id, completed.Token)
	if err != nil || !valid {
		t.Fatalf("begin completed submission = %v, %v", valid, err)
	}
	if err := lease.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(id, "pending"); err != nil {
		t.Fatal(err)
	}
	pending, err := session.StageRuntimeQueue(id)
	if err != nil {
		t.Fatal(err)
	}
	return completed.Token, pending.Token
}

func TestCommitRuntimeQueueRemovalFailurePreservesStateAndReleasesTransaction(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataRoot)
	const id = "single-remove-persist-failure"
	_, pendingToken := seedRuntimeQueueLifecycleState(t, id)
	completionPath := filepath.Join(dataRoot, "agent-deck", "runtime", "runtime-queue-completed", id+".json")
	if _, err := os.Stat(completionPath); err != nil {
		t.Fatalf("completion marker setup: %v", err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("forced registry persistence failure")
	if err := commitRuntimeQueueRemoval(tx, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("commit error = %v, want %v", err, wantErr)
	}
	tx.Release()
	batch, err := session.StageRuntimeQueue(id)
	if err != nil || batch.Token != pendingToken {
		t.Fatalf("pending WAL after failed removal = %#v, %v", batch, err)
	}
	if _, err := os.Stat(completionPath); err != nil {
		t.Fatalf("completion marker lost after failed removal: %v", err)
	}
	probe, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatalf("transaction retained after failed removal: %v", err)
	}
	probe.Release()
}

func TestCommitRuntimeQueueBulkRemovalDiscardsOnlyCommittedSessions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	const failedID, committedID = "bulk-failed", "bulk-committed"
	_, failedToken := seedRuntimeQueueLifecycleState(t, failedID)
	_, _ = seedRuntimeQueueLifecycleState(t, committedID)
	failedTx, _ := session.BeginRuntimeQueueTransaction(failedID)
	committedTx, _ := session.BeginRuntimeQueueTransaction(committedID)
	wantErr := errors.New("forced bulk persistence failure")
	if err := commitRuntimeQueueRemoval(failedTx, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("failed removal error = %v", err)
	}
	if err := commitRuntimeQueueRemoval(committedTx, func() error { return nil }); err != nil {
		t.Fatalf("committed removal error = %v", err)
	}
	failedTx.Release()
	committedTx.Release()
	if batch, err := session.StageRuntimeQueue(failedID); err != nil || batch.Token != failedToken {
		t.Fatalf("failed session queue not preserved: %#v, %v", batch, err)
	}
	if batch, err := session.StageRuntimeQueue(committedID); err != nil || batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("committed session queue survived: %#v, %v", batch, err)
	}
}

func TestBulkFinalizationDoesNotOverwriteConcurrentGroupUpdate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	storage, err := session.NewStorageWithProfile("_test_bulk_group_finalization")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	const id = "bulk-group-finalization"
	inst := session.NewInstance(id, t.TempDir())
	inst.ID = id
	if err := storage.InsertSessionAndVerify(inst, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	payload := session.LifecycleIntentPayload(inst, "", "")
	intent, err := session.PrepareLifecycleIntent(storage, id, session.LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.DeleteInstance(id, intent.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(id, "discard after finalization"); err != nil {
		t.Fatal(err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatal(err)
	}
	initial := []*session.GroupData{{Path: "team", Name: "Old", Expanded: true, Order: 1, DefaultPath: "/old", MaxConcurrent: 1}}
	if err := storage.SaveGroupsOnly(session.NewGroupTreeWithGroups(nil, initial)); err != nil {
		t.Fatal(err)
	}
	original := bulkSessionReverifyPersist
	updated := []*session.GroupData{{Path: "team", Name: "New", Expanded: false, Order: 8, DefaultPath: "/new", MaxConcurrent: 7}}
	bulkSessionReverifyPersist = func(s *session.Storage, target string, _ []*session.Instance, _ *session.GroupTree, token string) error {
		if err := original(s, target, nil, nil, token); err != nil {
			return err
		}
		return s.SaveGroupsOnly(session.NewGroupTreeWithGroups(nil, updated))
	}
	t.Cleanup(func() { bulkSessionReverifyPersist = original })
	if err := finalizeCommittedBulkRemovals(storage, []string{id}, []*session.RuntimeQueueTransaction{tx}, []session.LifecycleIntentHandle{intent}); err != nil {
		t.Fatal(err)
	}
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	var got *session.GroupData
	for _, group := range groups {
		if group.Path == "team" {
			got = group
		}
	}
	if got == nil || got.Name != "New" || got.Expanded || got.Order != 8 || got.DefaultPath != "/new" || got.MaxConcurrent != 7 {
		t.Fatalf("concurrent group update was overwritten: %#v", got)
	}
}

func TestFinalizeCommittedBulkRemovalsRejectsMismatchedIntentSet(t *testing.T) {
	storage, err := session.NewStorageWithProfile("_test_bulk_mismatched_intents")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	err = finalizeCommittedBulkRemovals(storage, []string{"missing-intent"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "one-to-one") {
		t.Fatalf("mismatched lifecycle sets error=%v", err)
	}
}

func TestCleanupCommittedBulkRemovalsContinuesAndCollectsErrors(t *testing.T) {
	ids := []string{"cleanup-one", "cleanup-two"}
	for _, id := range ids {
		if err := session.SaveQueuedMessage(id, "sentinel"); err != nil {
			t.Fatal(err)
		}
	}
	originalSweep, originalNotify := bulkSweepInboxes, bulkRemoveNotifyState
	sweepCalls, notifyCalls := 0, 0
	bulkSweepInboxes = func(id string) (int, error) {
		sweepCalls++
		return 0, fmt.Errorf("sweep %s", id)
	}
	bulkRemoveNotifyState = func(id string) (bool, error) {
		notifyCalls++
		return false, fmt.Errorf("notify %s", id)
	}
	t.Cleanup(func() {
		bulkSweepInboxes, bulkRemoveNotifyState = originalSweep, originalNotify
	})
	err := cleanupCommittedBulkRemovals(ids)
	if err == nil || !strings.Contains(err.Error(), "cleanup-one") || !strings.Contains(err.Error(), "cleanup-two") {
		t.Fatalf("aggregated cleanup error = %v", err)
	}
	if sweepCalls != 2 || notifyCalls != 2 {
		t.Fatalf("cleanup stopped early: sweep=%d notify=%d", sweepCalls, notifyCalls)
	}
	for _, id := range ids {
		if _, ok := session.PeekQueuedMessage(id); ok {
			t.Fatalf("queued-message cleanup skipped for %s", id)
		}
	}
}

func TestFinalizeCommittedBulkRemovalsPersistentResurrectionExhaustsAllPasses(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	storage, err := session.NewStorageWithProfile("_test_bulk_exhaustion")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	const id = "persistent-resurrection-exhaustion"
	inst := session.NewInstance(id, t.TempDir())
	inst.ID = id
	if err := storage.InsertSessionAndVerify(inst, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	payload := session.LifecycleIntentPayload(inst, "", "")
	intent, err := session.PrepareLifecycleIntent(storage, id, session.LifecycleIntentRemove, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.DeleteInstance(id, intent.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(id, "must survive exhaustion"); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveQueuedMessage(id, "cleanup must not run"); err != nil {
		t.Fatal(err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatal(err)
	}
	originalObserve := bulkObserveAbsent
	observeCalls := 0
	bulkObserveAbsent = func(*session.Storage, []string, []string, func() error) (bool, error) {
		observeCalls++
		return false, nil
	}
	t.Cleanup(func() { bulkObserveAbsent = originalObserve })
	err = finalizeCommittedBulkRemovals(storage, []string{id}, []*session.RuntimeQueueTransaction{tx}, []session.LifecycleIntentHandle{intent})
	if err == nil || !strings.Contains(err.Error(), "kept reappearing") {
		t.Fatalf("exhaustion error = %v", err)
	}
	if observeCalls != bulkFinalVerifyAttempts {
		t.Fatalf("observation passes = %d, want %d", observeCalls, bulkFinalVerifyAttempts)
	}
	if !session.RuntimeQueueHasPending(id) {
		t.Fatal("retry exhaustion discarded runtime queue")
	}
	if got, ok := session.PeekQueuedMessage(id); !ok || got != "cleanup must not run" {
		t.Fatalf("retry exhaustion ran cleanup: %q, %v", got, ok)
	}
	probe, err := session.BeginRuntimeQueueTransaction(id)
	if err != nil {
		t.Fatalf("retry exhaustion retained queue transaction: %v", err)
	}
	probe.Release()
}

func TestBulkRemoveFinalSweepDeletesRowsResurrectedAfterPerItemVerification(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	storage, err := session.NewStorageWithProfile("_test_bulk_final_sweep")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	first := session.NewInstanceWithTool("first", t.TempDir(), "shell")
	second := session.NewInstanceWithTool("second", t.TempDir(), "shell")
	first.Status = session.StatusError
	second.Status = session.StatusError
	instances := []*session.Instance{first, second}
	tree := session.NewGroupTree(instances)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if _, err := session.EnqueueRuntimeMessage(inst.ID, "discard committed queue"); err != nil {
			t.Fatal(err)
		}
	}

	originalPersist := bulkSessionRemovePersist
	bulkSessionRemovePersist = func(s *session.Storage, id string, remaining []*session.Instance, groupTree *session.GroupTree, token string) error {
		if err := s.RemoveSessionAndVerify(id, remaining, groupTree, token); err != nil {
			return err
		}
		if id == second.ID {
			// Simulate a stale full-table writer landing after both per-item
			// verification windows but before bulk removal returns.
			return s.SaveWithGroups(instances, tree)
		}
		return nil
	}
	t.Cleanup(func() { bulkSessionRemovePersist = originalPersist })

	removed := bulkRemoveSessions(NewCLIOutput(false, true), storage, instances, nil, instances, false)
	if len(removed) != 2 {
		t.Fatalf("removed rows = %#v", removed)
	}
	rows, _, err := storage.LoadWithGroups()
	if err != nil || len(rows) != 0 {
		t.Fatalf("final sweep left resurrected rows: %#v, %v", rows, err)
	}
	for _, inst := range instances {
		if session.RuntimeQueueHasPending(inst.ID) {
			t.Fatalf("committed queue survived for %s", inst.ID)
		}
	}
}

// addTestSession adds a session under the isolated HOME and returns its id.
// Mirrors sessionMoveAddSession but without the claude-project seeding side
// effect, so call sites that want a seeded transcript dir can do it
// separately via seedClaudeProjectDir.
func addTestSession(t *testing.T, home, workPath, title string) string {
	t.Helper()
	if err := os.MkdirAll(workPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", title,
		"-c", "claude",
		"--no-parent",
		"--json",
		workPath,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned empty id")
	}
	return resp.ID
}

// forceSetStatus opens storage directly under the isolated HOME and writes
// the target status onto the named instance. We can't use `agent-deck
// session set` because it doesn't accept status as a settable field (see
// handleSessionSet validFields map). Direct storage mutation is the
// standard test pattern for driving the registry into a specific state
// without racing the status worker.
func forceSetStatus(t *testing.T, home, id string, status session.Status) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")

	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var target *session.Instance
	for _, inst := range instances {
		if inst.ID == id {
			target = inst
			break
		}
	}
	if target == nil {
		t.Fatalf("instance %s not found (had %d instances)", id, len(instances))
		return
	}
	target.Status = status
	tree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestSessionRemove_StoppedSessionSucceeds is the happy path.
func TestSessionRemove_StoppedSessionSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-basic")
	forceSetStatus(t, home, id, session.StatusStopped)
	if _, err := session.EnqueueRuntimeMessage(id, "discard after committed removal"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StageRuntimeQueue(id); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "remove", id, "--json",
	)
	if code != 0 {
		t.Fatalf("session remove failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("session %s still present after remove; list:\n%s", id, listJSON)
	}
	if session.RuntimeQueueHasPending(id) {
		t.Fatal("runtime queue survived committed session removal")
	}
	if batch, err := session.StageRuntimeQueue(id); err != nil || batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("runtime WAL survived committed session removal: %#v, %v", batch, err)
	}
}

func TestSessionRemove_RemovesOwnedRepositorySessionTemp(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-repo-temp")
	forceSetStatus(t, home, id, session.StatusStopped)

	root := filepath.Join(workPath, ".agent-deck", "tmp", id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := "schema=1\nsession_id=" + id + "\n"
	if err := os.WriteFile(filepath.Join(root, ".agent-deck-session-temp"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large-task.log"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code != 0 {
		t.Fatalf("session remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("owned repository session temp survived removal: %s", root)
	}
}

// TestSessionRemove_RunningWithoutForce_Rejected enforces the safety gate.
func TestSessionRemove_RunningWithoutForce_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-running")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code == 0 {
		t.Fatalf("expected non-zero exit for running-without-force; stdout=%s stderr=%s", stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, id) {
		t.Errorf("running session was removed without --force; list:\n%s", listJSON)
	}
}

// TestSessionRemove_RunningWithForce_Succeeds confirms --force bypasses the gate.
func TestSessionRemove_RunningWithForce_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-forced")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--force", "--json")
	if code != 0 {
		t.Fatalf("--force remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("forced remove did not take effect; list:\n%s", listJSON)
	}
}

// TestSessionRemove_AllErrored_RemovesOnlyErrored — bulk path respects
// status filtering. Non-errored sessions must NOT be touched.
func TestSessionRemove_AllErrored_RemovesOnlyErrored(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	errID := addTestSession(t, home, filepath.Join(home, "err-proj"), "err-one")
	forceSetStatus(t, home, errID, session.StatusError)
	if _, err := session.EnqueueRuntimeMessage(errID, "discard bulk removed queue"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StageRuntimeQueue(errID); err != nil {
		t.Fatal(err)
	}
	stoppedID := addTestSession(t, home, filepath.Join(home, "stop-proj"), "stopped-one")
	forceSetStatus(t, home, stoppedID, session.StatusStopped)
	idleID := addTestSession(t, home, filepath.Join(home, "idle-proj"), "idle-one")
	forceSetStatus(t, home, idleID, session.StatusIdle)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, errID) {
		t.Errorf("errored session was NOT removed; list:\n%s", listJSON)
	}
	if session.RuntimeQueueHasPending(errID) {
		t.Fatal("runtime queue survived committed bulk removal")
	}
	if batch, err := session.StageRuntimeQueue(errID); err != nil || batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("runtime WAL survived committed bulk removal: %#v, %v", batch, err)
	}
	if !strings.Contains(listJSON, stoppedID) {
		t.Errorf("stopped session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, idleID) {
		t.Errorf("idle session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
}

func TestSessionRemove_AllErroredRemovesOwnedRepositorySessionTemp(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "bulk-temp-proj")
	id := addTestSession(t, home, workPath, "bulk-temp")
	forceSetStatus(t, home, id, session.StatusError)
	root := filepath.Join(workPath, ".agent-deck", "tmp", id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := "schema=1\nsession_id=" + id + "\n"
	if err := os.WriteFile(filepath.Join(root, ".agent-deck-session-temp"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("owned repository session temp survived bulk removal: %s", root)
	}
}

// Pinned errored sessions survive --all-errored unless --force is given.
//
// RemoteSession N/A (per repo guideline): `session remove --all-errored`
// operates only on the local registry loaded by loadSessionData, and the
// pin guard in removeAllErrored keys off inst.Pin/inst.Status alone with no
// local/remote branch. Remote (SSH) sessions live on a remote agent-deck
// instance and are never written to this registry, so there is no
// RemoteSession path for this command to cover.
func TestSessionRemove_AllErrored_SkipsPinned(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	pinnedID := addTestSession(t, home, filepath.Join(home, "pin-proj"), "pinned-err")
	plainID := addTestSession(t, home, filepath.Join(home, "plain-proj"), "plain-err")
	if _, stderr, code := runAgentDeck(t, home, "session", "set", pinnedID, "pin", "top"); code != 0 {
		t.Fatalf("set pin failed: %s", stderr)
	}
	forceSetStatus(t, home, pinnedID, session.StatusError)
	forceSetStatus(t, home, plainID, session.StatusError)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, pinnedID) {
		t.Errorf("pinned errored session must survive --all-errored; list:\n%s", listJSON)
	}
	if strings.Contains(listJSON, plainID) {
		t.Errorf("unpinned errored session should have been removed; list:\n%s", listJSON)
	}
}

// --force includes pinned errored sessions in the bulk sweep.
func TestSessionRemove_AllErrored_ForceIncludesPinned(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	pinnedID := addTestSession(t, home, filepath.Join(home, "pin-proj"), "pinned-err")
	if _, stderr, code := runAgentDeck(t, home, "session", "set", pinnedID, "pin", "top"); code != 0 {
		t.Fatalf("set pin failed: %s", stderr)
	}
	forceSetStatus(t, home, pinnedID, session.StatusError)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--force", "--json")
	if code != 0 {
		t.Fatalf("--all-errored --force failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, pinnedID) {
		t.Errorf("--force should remove pinned errored session; list:\n%s", listJSON)
	}
}

// TestSessionRemove_PreservesTranscripts is the hard invariant: registry
// removal must NOT touch ~/.claude/projects/<slug>/.
func TestSessionRemove_PreservesTranscripts(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-transcript")
	forceSetStatus(t, home, id, session.StatusStopped)

	// Seed the Claude transcript dir with a sentinel file.
	transcriptDir := seedClaudeProjectDir(t, home, workPath, "sentinel-transcript")
	sentinelPath := filepath.Join(transcriptDir, "abc-123.jsonl")

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code != 0 {
		t.Fatalf("remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Errorf("transcript sentinel missing after remove: %v", err)
	}
}

// TestSessionRemove_NotFound_Exit2 mirrors `session stop`'s convention.
func TestSessionRemove_NotFound_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	_, _, code := runAgentDeck(t, home, "session", "remove", "does-not-exist", "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 for not-found, got %d", code)
	}
}
