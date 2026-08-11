package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionRemoveCommandPersistenceFailurePreservesLifecycleState(t *testing.T) {
	if os.Getenv("AGENT_DECK_REMOVE_PERSIST_HELPER") == "single" {
		original := sessionRemovePersist
		sessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree) error {
			_ = storage.Close()
			return storage.RemoveSessionAndVerify(id, remaining, tree)
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

func TestSessionRemoveAllErroredPersistenceFailureDiscardsOnlyCommittedQueues(t *testing.T) {
	if os.Getenv("AGENT_DECK_REMOVE_PERSIST_HELPER") == "bulk" {
		failedID := os.Getenv("AGENT_DECK_REMOVE_PERSIST_ID")
		original := bulkSessionRemovePersist
		bulkSessionRemovePersist = func(storage *session.Storage, id string, remaining []*session.Instance, tree *session.GroupTree) error {
			if id == failedID {
				_ = storage.Close()
			}
			return storage.RemoveSessionAndVerify(id, remaining, tree)
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
		t.Fatalf("committed queue survived: %#v, %v", batch, err)
	}
	if batch, err := session.StageRuntimeQueue(failedID); err != nil || batch.Token != failedToken {
		t.Fatalf("failed queue was discarded: %#v, %v", batch, err)
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
