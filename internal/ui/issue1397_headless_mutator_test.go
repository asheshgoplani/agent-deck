package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// newHeadlessHomeForTest builds a Home backed by a real (sandboxed, _test
// profile) storage but WITHOUT booting bubbletea — exactly the `web --no-tui`
// shape that issue #1397 is about. The in-memory instances/groupTree start
// empty, mirroring a freshly-constructed headless server.
func newHeadlessHomeForTest(t *testing.T, profile string) (*Home, *session.Storage) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	h := &Home{
		profile:      storage.Profile(),
		storage:      storage,
		instanceByID: make(map[string]*session.Instance),
		groupTree:    session.NewGroupTree(nil),
		headless:     true,
	}
	return h, storage
}

func seedSession(t *testing.T, storage *session.Storage, existing []*session.Instance, id, title string) *session.Instance {
	t.Helper()
	inst := &session.Instance{
		ID:          id,
		Title:       title,
		ProjectPath: "/tmp/issue1397-proj",
		GroupPath:   session.DefaultGroupPath,
		Command:     "bash",
		Tool:        "bash",
		Status:      session.StatusStopped,
		CreatedAt:   time.Now(),
	}
	all := append(append([]*session.Instance{}, existing...), inst)
	if err := storage.SaveWithGroups(all, session.NewGroupTree(all)); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}
	return inst
}

// TestIssue1397_HeadlessDeleteFindsExistingSession verifies that a WebMutator
// backed by a headless Home (empty in-memory list) can delete a session that
// exists in storage. Pre-fix this returned "session not found" because the
// mutator only consulted the never-populated h.instanceByID.
func TestIssue1397_HeadlessDeleteFindsExistingSession(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_1397_delete")
	s1 := seedSession(t, storage, nil, "issue1397-del-001", "existing1")
	_ = seedSession(t, storage, []*session.Instance{s1}, "issue1397-del-002", "existing2")

	m := NewWebMutator(h)
	if _, err := session.EnqueueRuntimeMessage("issue1397-del-001", "discard on committed delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StageRuntimeQueue("issue1397-del-001"); err != nil {
		t.Fatal(err)
	}

	// The Home's in-memory map is empty — this is the headless precondition.
	if len(h.instanceByID) != 0 {
		t.Fatalf("precondition: headless Home should start with empty instanceByID, got %d", len(h.instanceByID))
	}

	if err := m.DeleteSession("issue1397-del-001"); err != nil {
		t.Fatalf("DeleteSession on existing session must succeed in headless mode, got: %v", err)
	}

	// Verify the row is actually gone from storage.
	remaining, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, r := range remaining {
		if r.ID == "issue1397-del-001" {
			t.Fatalf("deleted session still present in storage")
		}
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining session, got %d", len(remaining))
	}
	if session.RuntimeQueueHasPending("issue1397-del-001") {
		t.Fatal("runtime queue survived committed web deletion")
	}
	if batch, err := session.StageRuntimeQueue("issue1397-del-001"); err != nil || batch.Token != "" || len(batch.Messages) != 0 {
		t.Fatalf("runtime WAL survived committed web deletion: %#v, %v", batch, err)
	}
}

// TestIssue1397_HeadlessDeleteUnknownStillErrors guards the inverse: deleting a
// genuinely non-existent id still fails (hydration must not paper over real
// not-found errors).
func TestIssue1397_HeadlessDeleteUnknownStillErrors(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_1397_unknown")
	_ = seedSession(t, storage, nil, "issue1397-keep-001", "keepme")

	m := NewWebMutator(h)
	if err := m.DeleteSession("does-not-exist"); err == nil {
		t.Fatal("deleting a non-existent session must still error")
	}
}

func TestHeadlessDeleteStorageOpenFailurePreservesRuntimeQueue(t *testing.T) {
	originalDataRoot := os.Getenv("XDG_DATA_HOME")
	h, storage := newHeadlessHomeForTest(t, "_test_web_delete_failure")
	_ = seedSession(t, storage, nil, "web-delete-failure", "preserve-queue")
	if _, err := session.EnqueueRuntimeMessage("web-delete-failure", "must survive failed web delete"); err != nil {
		t.Fatal(err)
	}
	m := NewWebMutator(h)
	dataRoot := t.TempDir()
	blockedProfiles := filepath.Join(dataRoot, "agent-deck", "profiles")
	if err := os.MkdirAll(filepath.Dir(blockedProfiles), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedProfiles, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", dataRoot)
	if err := m.DeleteSession("web-delete-failure"); err == nil {
		t.Fatal("web delete unexpectedly succeeded with invalid storage profile")
	}
	t.Setenv("XDG_DATA_HOME", originalDataRoot)
	if !session.RuntimeQueueHasPending("web-delete-failure") {
		t.Fatal("failed web deletion discarded runtime queue")
	}
}

func TestWebDeletePersistenceFailurePreservesRowAndRuntimeQueue(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	h, storage := newHeadlessHomeForTest(t, "_test_web_delete_commit_failure")
	inst := seedSession(t, storage, nil, "web-delete-commit-failure", "preserve-row")
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "completed before failed delete"); err != nil {
		t.Fatal(err)
	}
	completed, err := session.StageRuntimeQueue(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, valid, err := session.BeginRuntimeQueueSubmission(inst.ID, completed.Token)
	if err != nil || !valid {
		t.Fatalf("begin completion = %v, %v", valid, err)
	}
	if err := lease.Acknowledge(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "active before failed delete"); err != nil {
		t.Fatal(err)
	}
	pending, err := session.StageRuntimeQueue(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	completionPath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "agent-deck", "runtime", "runtime-queue-completed", inst.ID+".json")
	originalDelete := webDeleteInstance
	webDeleteInstance = func(*session.Storage, string) error { return fmt.Errorf("forced DeleteInstance failure") }
	t.Cleanup(func() { webDeleteInstance = originalDelete })
	if err := NewWebMutator(h).DeleteSession(inst.ID); err == nil {
		t.Fatal("web delete commit unexpectedly succeeded")
	}
	rows, _, err := storage.LoadWithGroups()
	if err != nil || len(rows) != 1 || rows[0].ID != inst.ID {
		t.Fatalf("durable row lost after failed web delete: %#v, %v", rows, err)
	}
	if batch, err := session.StageRuntimeQueue(inst.ID); err != nil || batch.Token != pending.Token {
		t.Fatalf("queue/WAL lost after failed web delete: %#v, %v", batch, err)
	}
	if _, err := os.Stat(completionPath); err != nil {
		t.Fatalf("completion state lost after failed web delete: %v", err)
	}
}

func TestWebArchivePersistenceFailurePreservesUnarchivedRowAndRuntimeQueue(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	h, storage := newHeadlessHomeForTest(t, "_test_web_archive_commit_failure")
	inst := seedSession(t, storage, nil, "web-archive-commit-failure", "preserve-unarchived")
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "preserve failed archive"); err != nil {
		t.Fatal(err)
	}
	originalPersist := webPersistArchive
	webPersistArchive = func(*WebMutator) error { return fmt.Errorf("forced archive persistence failure") }
	t.Cleanup(func() { webPersistArchive = originalPersist })
	if err := NewWebMutator(h).ArchiveSession(inst.ID); err == nil {
		t.Fatal("web archive commit unexpectedly succeeded")
	}
	rows, _, err := storage.LoadWithGroups()
	if err != nil || len(rows) != 1 || !rows[0].ArchivedAt.IsZero() {
		t.Fatalf("durable lifecycle changed after failed web archive: %#v, %v", rows, err)
	}
	if !session.RuntimeQueueHasPending(inst.ID) {
		t.Fatal("queue lost after failed web archive")
	}
	if h.instanceByID[inst.ID].IsArchived() {
		t.Fatal("failed web archive left the live model archived")
	}
}

func TestWebArchiveDiscardFailureKeepsCommittedArchivedState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	h, storage := newHeadlessHomeForTest(t, "_test_web_archive_discard_failure")
	inst := seedSession(t, storage, nil, "web-archive-discard-failure", "keep-archived")
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "discard will fail"); err != nil {
		t.Fatal(err)
	}
	originalDiscard := webDiscardQueue
	webDiscardQueue = func(*session.RuntimeQueueTransaction) error { return fmt.Errorf("forced queue discard failure") }
	t.Cleanup(func() { webDiscardQueue = originalDiscard })

	if err := NewWebMutator(h).ArchiveSession(inst.ID); err == nil {
		t.Fatal("web archive unexpectedly hid queue discard failure")
	}
	rows, _, err := storage.LoadWithGroups()
	if err != nil || len(rows) != 1 || !rows[0].IsArchived() {
		t.Fatalf("durable archive did not remain committed: %#v, %v", rows, err)
	}
	if live := h.instanceByID[inst.ID]; live == nil || !live.IsArchived() {
		t.Fatalf("live archive rolled back after post-commit discard failure: %#v", live)
	}
	if !session.RuntimeQueueHasPending(inst.ID) {
		t.Fatal("forced discard failure unexpectedly removed runtime queue")
	}
}

func TestTUIDeletePersistenceFailurePreservesRuntimeQueueAndReleasesTransaction(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_tui_delete_failure")
	inst := seedSession(t, storage, nil, "tui-delete-failure", "preserve-queue")
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.groupTree = session.NewGroupTree(h.instances)
	h.search = NewSearch()
	h.search.SetItems(h.instances)
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "must survive failed TUI delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StageRuntimeQueue(inst.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = h.updateInner(sessionDeletedMsg{deletedID: inst.ID, queueTx: tx})
	if len(h.instances) != 1 || h.instanceByID[inst.ID] != inst || len(h.undoStack) != 0 {
		t.Fatalf("failed TUI delete mutated live row/map/undo state: instances=%v mapped=%v undo=%d", h.instances, h.instanceByID[inst.ID], len(h.undoStack))
	}
	if len(h.search.allItems) != 1 || h.search.allItems[0].ID != inst.ID {
		t.Fatalf("failed TUI delete removed search item: %#v", h.search.allItems)
	}
	if group := h.groupTree.Groups[inst.GroupPath]; group == nil || len(group.Sessions) != 1 || group.Sessions[0].ID != inst.ID {
		t.Fatalf("failed TUI delete removed group-tree row: %#v", group)
	}
	if batch, err := session.StageRuntimeQueue(inst.ID); err != nil || batch.Token == "" {
		t.Fatalf("failed TUI delete did not preserve queue/WAL: %#v, %v", batch, err)
	}
	probe, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		t.Fatalf("failed TUI delete retained transaction: %v", err)
	}
	probe.Release()
}

func TestTUIArchivePersistenceFailurePreservesRuntimeQueueAndReleasesTransaction(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_tui_archive_failure")
	inst := seedSession(t, storage, nil, "tui-archive-failure", "preserve-queue")
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.groupTree = session.NewGroupTree(h.instances)
	h.search = NewSearch()
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "must survive failed TUI archive"); err != nil {
		t.Fatal(err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	inst.ArchivedAt = time.Now().UTC()
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = h.updateInner(sessionArchivedMsg{sessionID: inst.ID, queueTx: tx})
	if !session.RuntimeQueueHasPending(inst.ID) {
		t.Fatal("failed TUI archive discarded runtime queue")
	}
	probe, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		t.Fatalf("failed TUI archive retained transaction: %v", err)
	}
	probe.Release()
}

func TestTUIWorktreeFinishDeleteFailurePreservesRuntimeQueue(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_tui_worktree_delete_failure")
	inst := seedSession(t, storage, nil, "tui-worktree-delete-failure", "preserve-queue")
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.groupTree = session.NewGroupTree(h.instances)
	h.search = NewSearch()
	h.worktreeFinishDialog = NewWorktreeFinishDialog()
	h.worktreeFinishDialog.Show(inst.ID, inst.Title, "feature", "/tmp/repo", "/tmp/worktree", "main")
	if _, err := session.EnqueueRuntimeMessage(inst.ID, "must survive failed worktree finish"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StageRuntimeQueue(inst.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := session.BeginRuntimeQueueTransaction(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	_, _ = h.updateInner(worktreeFinishResultMsg{sessionID: inst.ID, sessionTitle: inst.Title, queueTx: tx})
	if len(h.instances) != 1 || h.instanceByID[inst.ID] != inst {
		t.Fatalf("failed worktree finish removed live session: instances=%v mapped=%v", h.instances, h.instanceByID[inst.ID])
	}
	if !h.worktreeFinishDialog.IsVisible() || h.worktreeFinishDialog.GetSessionID() != inst.ID || h.worktreeFinishDialog.errorMsg == "" {
		t.Fatalf("failed worktree finish did not restore dialog state: %#v", h.worktreeFinishDialog)
	}
	if batch, err := session.StageRuntimeQueue(inst.ID); err != nil || batch.Token == "" {
		t.Fatalf("failed worktree finish did not preserve queue/WAL: %#v, %v", batch, err)
	}
}

func TestTUISuccessfulRemovalPreservesPersistedGroupMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  func(*session.Instance) tea.Msg
	}{
		{name: "delete", msg: func(inst *session.Instance) tea.Msg {
			return sessionDeletedMsg{deletedID: inst.ID}
		}},
		{name: "worktree finish", msg: func(inst *session.Instance) tea.Msg {
			return worktreeFinishResultMsg{sessionID: inst.ID, sessionTitle: inst.Title}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			h, storage := newHeadlessHomeForTest(t, "_test_tui_group_metadata_"+strings.ReplaceAll(tc.name, " ", "_"))
			inst := seedSession(t, storage, nil, "tui-group-metadata-"+strings.ReplaceAll(tc.name, " ", "-"), "remove")
			inst.GroupPath = "engineering/platform"
			want := &session.GroupData{
				Name: "Platform", Path: inst.GroupPath, Expanded: true, Order: 9,
				DefaultPath: "/projects/platform", MaxConcurrent: 6,
			}
			tree := session.NewGroupTreeWithGroups([]*session.Instance{inst}, []*session.GroupData{want})
			if err := storage.SaveWithGroups([]*session.Instance{inst}, tree); err != nil {
				t.Fatal(err)
			}
			h.instances = []*session.Instance{inst}
			h.instanceByID[inst.ID] = inst
			h.groupTree = tree
			h.search = NewSearch()
			h.worktreeFinishDialog = NewWorktreeFinishDialog()

			_, _ = h.updateInner(tc.msg(inst))
			rows, groups, err := storage.LoadWithGroups()
			if err != nil || len(rows) != 0 {
				t.Fatalf("successful removal rows = %#v, %v", rows, err)
			}
			var got *session.GroupData
			for _, group := range groups {
				if group.Path == want.Path {
					got = group
					break
				}
			}
			if got == nil || got.Name != want.Name || got.Expanded != want.Expanded || got.Order != want.Order ||
				got.DefaultPath != want.DefaultPath || got.MaxConcurrent != want.MaxConcurrent {
				t.Fatalf("persisted group = %#v, want %#v (all groups %#v)", got, want, groups)
			}
		})
	}
}

// TestIssue1397_HeadlessCreateGroupDoesNotTripGuard verifies that creating a
// group while sessions exist in storage does not trip the empty-SaveInstances
// data-loss guard. Pre-fix, persistAllInstances/SaveWithGroups ran with the
// empty in-memory list and returned 500 from ErrRefusingEmptySweep.
func TestIssue1397_HeadlessCreateGroupDoesNotTripGuard(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_1397_group")
	_ = seedSession(t, storage, nil, "issue1397-grp-001", "existing1")

	m := NewWebMutator(h)
	if _, err := m.CreateGroup("newgrp", ""); err != nil {
		t.Fatalf("CreateGroup must succeed in headless mode with existing sessions, got: %v", err)
	}

	// The pre-existing session must survive the group creation (not wiped).
	remaining, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("existing session must survive group creation, got %d sessions", len(remaining))
	}
	found := false
	for _, g := range groups {
		if g.Name == "newgrp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("created group not persisted; groups=%+v", groups)
	}
}

// TestIssue1397_LiveModeDoesNotHydrate ensures the hydration path is gated on
// headless: in live-TUI mode (headless=false) beginHeadlessTx must be a no-op so
// it never races the bubbletea loop that owns the in-memory state.
func TestIssue1397_LiveModeDoesNotHydrate(t *testing.T) {
	storage, err := session.NewStorageWithProfile("_test_1397_live")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	_ = seedSession(t, storage, nil, "issue1397-live-001", "onlyondisk")

	h := &Home{
		profile:      storage.Profile(),
		storage:      storage,
		instanceByID: make(map[string]*session.Instance),
		groupTree:    session.NewGroupTree(nil),
		headless:     false, // live TUI mode
	}
	m := NewWebMutator(h)

	// In live mode, beginHeadlessTx must NOT load from storage, so the on-disk
	// session stays invisible to the (empty) in-memory map.
	unlock, err := m.beginHeadlessTx()
	if err != nil {
		t.Fatalf("beginHeadlessTx should be a no-op in live mode, got: %v", err)
	}
	unlock()
	if len(h.instanceByID) != 0 {
		t.Fatalf("live mode must not hydrate; instanceByID has %d entries", len(h.instanceByID))
	}
}

// TestIssue1397_HeadlessConcurrentMutationsNoRace fires concurrent headless
// mutations (group creates + a delete) and asserts no data race and no lost
// data: the pre-existing session survives and the deleted one is gone. Run with
// -race to exercise the hydrate->mutate->persist serialization (#1397, Codex
// review point on concurrency).
func TestIssue1397_HeadlessConcurrentMutationsNoRace(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_1397_concurrent")
	keep := seedSession(t, storage, nil, "issue1397-keep-001", "keepme")
	_ = seedSession(t, storage, []*session.Instance{keep}, "issue1397-doomed-001", "doomed")

	m := NewWebMutator(h)

	const groups = 8
	var wg sync.WaitGroup
	errs := make(chan error, groups+1)

	for i := 0; i < groups; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := m.CreateGroup(fmt.Sprintf("g%d", n), ""); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := m.DeleteSession("issue1397-doomed-001"); err != nil {
			errs <- err
		}
	}()

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent mutation error: %v", err)
	}

	remaining, grpList, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// The kept session must survive all the churn; the doomed one must be gone.
	var sawKeep, sawDoomed bool
	for _, r := range remaining {
		switch r.ID {
		case "issue1397-keep-001":
			sawKeep = true
		case "issue1397-doomed-001":
			sawDoomed = true
		}
	}
	if !sawKeep {
		t.Error("kept session was lost under concurrent mutations")
	}
	if sawDoomed {
		t.Error("doomed session should have been deleted")
	}
	// Serialization guarantees no lost updates: ALL concurrently-created groups
	// must persist (a weaker >0 check would not catch a lost-update regression).
	created := 0
	for _, g := range grpList {
		if strings.HasPrefix(g.Name, "g") {
			created++
		}
	}
	if created != groups {
		t.Errorf("expected all %d concurrent groups to persist, got %d", groups, created)
	}
}
