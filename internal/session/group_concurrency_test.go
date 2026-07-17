package session

import (
	"testing"
	"time"
)

// TestGroup_MaxConcurrent_DefaultSerial verifies that a newly-created group via
// GroupTree.CreateGroup defaults to MaxConcurrent=1 (serial). The default is
// only applied for groups created post-v1.9.1; pre-existing groups loaded from
// state.db keep their original MaxConcurrent value (0 → unlimited).
func TestGroup_MaxConcurrent_DefaultSerial(t *testing.T) {
	tree := NewGroupTree(nil)
	g := tree.CreateGroup("test-group")
	if g.MaxConcurrent != 1 {
		t.Errorf("CreateGroup: expected MaxConcurrent=1 (serial default), got %d", g.MaxConcurrent)
	}

	sub := tree.CreateSubgroup(g.Path, "child")
	if sub.MaxConcurrent != 1 {
		t.Errorf("CreateSubgroup: expected MaxConcurrent=1 (serial default), got %d", sub.MaxConcurrent)
	}
}

// TestGroup_LaunchOverCap_Queues verifies that ShouldQueue returns true when
// the running count is at or above max_concurrent.
func TestGroup_LaunchOverCap_Queues(t *testing.T) {
	instances := []*Instance{
		{ID: "a", GroupPath: "g", Status: StatusRunning},
		{ID: "b", GroupPath: "g", Status: StatusRunning},
	}

	// max=2, running=2 → at cap, third should queue
	if !ShouldQueue(instances, "g", 2) {
		t.Error("max_concurrent=2 with 2 running: expected ShouldQueue=true")
	}

	// max=2, running=1 (drop one) → room available
	instances[1].Status = StatusStopped
	if ShouldQueue(instances, "g", 2) {
		t.Error("max_concurrent=2 with 1 running: expected ShouldQueue=false")
	}

	// Serial cap (max=1) with one running → must queue
	instances[1].Status = StatusRunning
	instances = instances[:1]
	if !ShouldQueue(instances, "g", 1) {
		t.Error("max_concurrent=1 (serial) with 1 running: expected ShouldQueue=true")
	}
}

// TestGroup_QueueDrains verifies that FindNextQueued returns the oldest queued
// instance in the group, so a stopping session can wake one up.
func TestGroup_QueueDrains(t *testing.T) {
	now := time.Now()
	queuedNewer := &Instance{ID: "q2", GroupPath: "g", Status: StatusQueued, CreatedAt: now}
	queuedOlder := &Instance{ID: "q1", GroupPath: "g", Status: StatusQueued, CreatedAt: now.Add(-1 * time.Minute)}
	instances := []*Instance{
		{ID: "r1", GroupPath: "g", Status: StatusRunning},
		queuedNewer,
		queuedOlder,
		{ID: "other", GroupPath: "other", Status: StatusQueued, CreatedAt: now.Add(-1 * time.Hour)},
	}

	next := FindNextQueued(instances, "g")
	if next == nil {
		t.Fatal("expected FindNextQueued to return a queued instance, got nil")
	}
	if next.ID != "q1" {
		t.Errorf("expected oldest queued (q1), got %s", next.ID)
	}

	// When nothing is queued, returns nil
	queuedOlder.Status = StatusRunning
	queuedNewer.Status = StatusRunning
	if got := FindNextQueued(instances, "g"); got != nil {
		t.Errorf("expected nil when no queued instances, got %s", got.ID)
	}
}

// TestGroup_UnlimitedLegacy verifies that max_concurrent <= 0 means unlimited,
// preserving backward compatibility for groups created before v1.9.1.
func TestGroup_UnlimitedLegacy(t *testing.T) {
	instances := []*Instance{
		{ID: "a", GroupPath: "g", Status: StatusRunning},
		{ID: "b", GroupPath: "g", Status: StatusRunning},
		{ID: "c", GroupPath: "g", Status: StatusRunning},
	}

	// max=0 (legacy/unset): unlimited
	if ShouldQueue(instances, "g", 0) {
		t.Error("max_concurrent=0 (legacy unlimited): expected ShouldQueue=false")
	}

	// max<0 (explicit unlimited)
	if ShouldQueue(instances, "g", -1) {
		t.Error("max_concurrent=-1 (explicit unlimited): expected ShouldQueue=false")
	}
}

// TestGroup_NewGroupDefault_ConfigUnsetSerial verifies that with no
// DefaultMaxConcurrent seeded on the tree (nil), CreateGroup keeps the
// built-in serial default (1) — byte-for-byte v1.9.1 behavior.
func TestGroup_NewGroupDefault_ConfigUnsetSerial(t *testing.T) {
	tree := NewGroupTree(nil)
	if tree.DefaultMaxConcurrent != nil {
		t.Fatalf("expected nil DefaultMaxConcurrent on a fresh tree, got %v", *tree.DefaultMaxConcurrent)
	}
	g := tree.CreateGroup("g")
	if g.MaxConcurrent != 1 {
		t.Errorf("config unset: expected new group MaxConcurrent=1 (serial), got %d", g.MaxConcurrent)
	}
}

// TestGroup_NewGroupDefault_ConfigZeroUnlimited verifies that a seeded
// DefaultMaxConcurrent of *0 makes new groups unlimited (0), for both
// CreateGroup and CreateSubgroup, and that 0 means "never queue".
func TestGroup_NewGroupDefault_ConfigZeroUnlimited(t *testing.T) {
	zero := 0
	tree := NewGroupTree(nil)
	tree.DefaultMaxConcurrent = &zero

	g := tree.CreateGroup("g")
	if g.MaxConcurrent != 0 {
		t.Errorf("config 0: expected new group MaxConcurrent=0 (unlimited), got %d", g.MaxConcurrent)
	}
	sub := tree.CreateSubgroup(g.Path, "child")
	if sub.MaxConcurrent != 0 {
		t.Errorf("config 0: expected new subgroup MaxConcurrent=0 (unlimited), got %d", sub.MaxConcurrent)
	}

	// max=0 (unlimited) with two running → must NOT queue.
	instances := []*Instance{
		{ID: "a", GroupPath: g.Path, Status: StatusRunning},
		{ID: "b", GroupPath: g.Path, Status: StatusRunning},
	}
	if ShouldQueue(instances, g.Path, g.MaxConcurrent) {
		t.Error("config 0 (unlimited) with 2 running: expected ShouldQueue=false")
	}
}

// TestGroup_NewGroupDefault_ConfigN verifies that a seeded DefaultMaxConcurrent
// of *N is copied verbatim into new groups created via CreateGroup and
// CreateSubgroup.
func TestGroup_NewGroupDefault_ConfigN(t *testing.T) {
	n := 4
	tree := NewGroupTree(nil)
	tree.DefaultMaxConcurrent = &n

	g := tree.CreateGroup("g")
	if g.MaxConcurrent != 4 {
		t.Errorf("config 4: expected new group MaxConcurrent=4, got %d", g.MaxConcurrent)
	}
	sub := tree.CreateSubgroup(g.Path, "child")
	if sub.MaxConcurrent != 4 {
		t.Errorf("config 4: expected new subgroup MaxConcurrent=4, got %d", sub.MaxConcurrent)
	}
}

// TestGroup_CountRunningInGroup verifies the count helper only includes
// running sessions in the target group (not queued, not other groups).
func TestGroup_CountRunningInGroup(t *testing.T) {
	instances := []*Instance{
		{GroupPath: "g", Status: StatusRunning},
		{GroupPath: "g", Status: StatusRunning},
		{GroupPath: "g", Status: StatusQueued},
		{GroupPath: "g", Status: StatusStopped},
		{GroupPath: "other", Status: StatusRunning},
	}
	if got := CountRunningInGroup(instances, "g"); got != 2 {
		t.Errorf("CountRunningInGroup: expected 2, got %d", got)
	}
}
