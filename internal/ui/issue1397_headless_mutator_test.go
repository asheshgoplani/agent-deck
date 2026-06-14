package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
