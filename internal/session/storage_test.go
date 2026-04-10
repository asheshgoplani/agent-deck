package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// newTestStorage creates a Storage backed by an in-memory-like temp dir SQLite database.
func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Storage{db: db, dbPath: dbPath, profile: "_test"}
}

// TestStorageUpdatedAtTimestamp verifies that SaveWithGroups sets the UpdatedAt timestamp
// and GetUpdatedAt() returns it correctly.
func TestStorageUpdatedAtTimestamp(t *testing.T) {
	s := newTestStorage(t)

	instances := []*Instance{
		{
			ID:          "test-1",
			Title:       "Test Session",
			ProjectPath: "/tmp/test",
			GroupPath:   "test-group",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
		},
	}

	// Save data
	beforeSave := time.Now()
	time.Sleep(10 * time.Millisecond)

	err := s.SaveWithGroups(instances, nil)
	if err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	afterSave := time.Now()

	// Get the updated timestamp
	updatedAt, err := s.GetUpdatedAt()
	if err != nil {
		t.Fatalf("GetUpdatedAt failed: %v", err)
	}

	// Verify timestamp is within expected range
	if updatedAt.Before(beforeSave) {
		t.Errorf("UpdatedAt %v is before save started %v", updatedAt, beforeSave)
	}
	if updatedAt.After(afterSave) {
		t.Errorf("UpdatedAt %v is after save completed %v", updatedAt, afterSave)
	}

	// Verify timestamp is not zero
	if updatedAt.IsZero() {
		t.Error("UpdatedAt is zero, expected a valid timestamp")
	}

	// Save again and verify timestamp updates
	time.Sleep(50 * time.Millisecond)
	firstUpdatedAt := updatedAt

	err = s.SaveWithGroups(instances, nil)
	if err != nil {
		t.Fatalf("Second SaveWithGroups failed: %v", err)
	}

	secondUpdatedAt, err := s.GetUpdatedAt()
	if err != nil {
		t.Fatalf("Second GetUpdatedAt failed: %v", err)
	}

	// Verify second timestamp is after first
	if !secondUpdatedAt.After(firstUpdatedAt) {
		t.Errorf("Second UpdatedAt %v should be after first %v", secondUpdatedAt, firstUpdatedAt)
	}
}

// TestGetUpdatedAtEmpty verifies behavior when no data has been saved
func TestGetUpdatedAtEmpty(t *testing.T) {
	s := newTestStorage(t)

	updatedAt, err := s.GetUpdatedAt()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !updatedAt.IsZero() {
		t.Errorf("Expected zero time for empty db, got %v", updatedAt)
	}
}

// TestLoadLite verifies that LoadLite returns raw InstanceData without tmux initialization
func TestLoadLite(t *testing.T) {
	s := newTestStorage(t)

	instances := []*Instance{
		{
			ID:          "test-1",
			Title:       "Test Session 1",
			ProjectPath: "/tmp/test1",
			GroupPath:   "test-group",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusWaiting,
			CreatedAt:   time.Now(),
		},
		{
			ID:          "test-2",
			Title:       "Test Session 2",
			ProjectPath: "/tmp/test2",
			GroupPath:   "other-group",
			Command:     "gemini",
			Tool:        "gemini",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
		},
	}

	err := s.SaveWithGroups(instances, nil)
	if err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	instData, groupData, err := s.LoadLite()
	if err != nil {
		t.Fatalf("LoadLite failed: %v", err)
	}

	if len(instData) != 2 {
		t.Errorf("Expected 2 instances, got %d", len(instData))
	}

	if instData[0].ID != "test-1" {
		t.Errorf("Expected first instance ID 'test-1', got '%s'", instData[0].ID)
	}
	if instData[0].Title != "Test Session 1" {
		t.Errorf("Expected first instance title 'Test Session 1', got '%s'", instData[0].Title)
	}
	if instData[0].Status != StatusWaiting {
		t.Errorf("Expected first instance status 'waiting', got '%s'", instData[0].Status)
	}

	if instData[1].ID != "test-2" {
		t.Errorf("Expected second instance ID 'test-2', got '%s'", instData[1].ID)
	}
	if instData[1].Tool != "gemini" {
		t.Errorf("Expected second instance tool 'gemini', got '%s'", instData[1].Tool)
	}

	if len(groupData) != 0 {
		t.Errorf("Expected 0 groups, got %d", len(groupData))
	}
}

// TestLoadLiteEmptyDB verifies LoadLite returns empty slice when database is empty
func TestLoadLiteEmptyDB(t *testing.T) {
	s := newTestStorage(t)

	instData, groupData, err := s.LoadLite()
	if err != nil {
		t.Errorf("LoadLite should not return error for empty db, got: %v", err)
	}
	if len(instData) != 0 {
		t.Errorf("Expected empty instances, got %d", len(instData))
	}
	if len(groupData) != 0 {
		t.Errorf("Expected empty groups, got %d", len(groupData))
	}
}

func TestStorageSaveWithGroups_DedupsClaudeSessionIDs(t *testing.T) {
	s := newTestStorage(t)
	now := time.Now()

	older := &Instance{
		ID:               "old",
		Title:            "Older",
		ProjectPath:      "/tmp/one",
		GroupPath:        "grp",
		Command:          "claude",
		Tool:             "claude",
		Status:           StatusIdle,
		CreatedAt:        now.Add(-2 * time.Minute),
		ClaudeSessionID:  "shared-session-id",
		ClaudeDetectedAt: now.Add(-2 * time.Minute),
	}
	newer := &Instance{
		ID:               "new",
		Title:            "Newer",
		ProjectPath:      "/tmp/two",
		GroupPath:        "grp",
		Command:          "claude",
		Tool:             "claude",
		Status:           StatusIdle,
		CreatedAt:        now.Add(-1 * time.Minute),
		ClaudeSessionID:  "shared-session-id",
		ClaudeDetectedAt: now.Add(-1 * time.Minute),
	}
	otherTool := &Instance{
		ID:          "gem",
		Title:       "Gemini",
		ProjectPath: "/tmp/gem",
		GroupPath:   "grp",
		Command:     "gemini",
		Tool:        "gemini",
		Status:      StatusIdle,
		CreatedAt:   now,
	}

	// Intentionally unsorted to ensure dedup logic does not rely on caller order.
	instances := []*Instance{newer, otherTool, older}
	if err := s.SaveWithGroups(instances, nil); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	if older.ClaudeSessionID != "shared-session-id" {
		t.Fatalf("older session should keep shared ID, got %q", older.ClaudeSessionID)
	}
	if newer.ClaudeSessionID != "" {
		t.Fatalf("newer duplicate should be cleared, got %q", newer.ClaudeSessionID)
	}

	loaded, _, err := s.LoadLite()
	if err != nil {
		t.Fatalf("LoadLite failed: %v", err)
	}

	byID := make(map[string]*InstanceData, len(loaded))
	for _, inst := range loaded {
		byID[inst.ID] = inst
	}
	if byID["old"] == nil || byID["new"] == nil {
		t.Fatalf("expected old and new instances in DB, got keys: %#v", byID)
	}
	if byID["old"].ClaudeSessionID != "shared-session-id" {
		t.Fatalf("db old session ID = %q, want shared-session-id", byID["old"].ClaudeSessionID)
	}
	if byID["new"].ClaudeSessionID != "" {
		t.Fatalf("db newer session ID = %q, want empty", byID["new"].ClaudeSessionID)
	}
}

func TestStorageSaveWithGroups_PersistsSandboxConfig(t *testing.T) {
	s := newTestStorage(t)

	cpu := "2.0"
	mem := "4g"
	instances := []*Instance{
		{
			ID:               "sandboxed-1",
			Title:            "Sandboxed Session",
			ProjectPath:      "/tmp/sandboxed",
			GroupPath:        "grp",
			Command:          "claude --dangerously-skip-permissions",
			Tool:             "claude",
			Status:           StatusIdle,
			CreatedAt:        time.Now(),
			Sandbox:          &SandboxConfig{Enabled: true, Image: "ghcr.io/example/sandbox:latest", CPULimit: &cpu, MemoryLimit: &mem},
			SandboxContainer: "agent-deck-sandbox-sandboxed-1",
		},
	}

	if err := s.SaveWithGroups(instances, nil); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	lite, _, err := s.LoadLite()
	if err != nil {
		t.Fatalf("LoadLite failed: %v", err)
	}
	if len(lite) != 1 {
		t.Fatalf("expected 1 lite instance, got %d", len(lite))
	}
	if lite[0].Sandbox == nil || !lite[0].Sandbox.Enabled {
		t.Fatal("expected sandbox config to be restored in LoadLite")
	}
	if lite[0].Sandbox.Image != "ghcr.io/example/sandbox:latest" {
		t.Fatalf("sandbox image = %q, want ghcr.io/example/sandbox:latest", lite[0].Sandbox.Image)
	}
	if lite[0].SandboxContainer != "agent-deck-sandbox-sandboxed-1" {
		t.Fatalf("sandbox container = %q, want agent-deck-sandbox-sandboxed-1", lite[0].SandboxContainer)
	}

	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups failed: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded instance, got %d", len(loaded))
	}
	if !loaded[0].IsSandboxed() {
		t.Fatal("expected loaded instance to remain sandboxed after SQLite round-trip")
	}
	if loaded[0].Sandbox == nil || loaded[0].Sandbox.Image != "ghcr.io/example/sandbox:latest" {
		t.Fatalf("loaded sandbox image = %#v", loaded[0].Sandbox)
	}
	if loaded[0].SandboxContainer != "agent-deck-sandbox-sandboxed-1" {
		t.Fatalf("loaded sandbox container = %q, want agent-deck-sandbox-sandboxed-1", loaded[0].SandboxContainer)
	}
}

// TestSaveSessionData_PreservesGroupSortOrder verifies that saving session data
// with stored groups preserves the sort_order, matching the fix in #465.
func TestSaveSessionData_PreservesGroupSortOrder(t *testing.T) {
	s := newTestStorage(t)
	now := time.Now()

	instances := []*Instance{
		{
			ID:          "s1",
			Title:       "Session Alpha",
			ProjectPath: "/tmp/alpha",
			GroupPath:   "backend",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   now,
		},
		{
			ID:          "s2",
			Title:       "Session Beta",
			ProjectPath: "/tmp/beta",
			GroupPath:   "frontend",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   now,
		},
		{
			ID:          "s3",
			Title:       "Session Gamma",
			ProjectPath: "/tmp/gamma",
			GroupPath:   "infra",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   now,
		},
	}

	// Simulate user-reordered groups: infra=0, frontend=1, backend=2.
	// Alphabetical would be backend=0, frontend=1, infra=2.
	storedGroups := []*GroupData{
		{Name: "infra", Path: "infra", Expanded: true, Order: 0},
		{Name: "frontend", Path: "frontend", Expanded: true, Order: 1},
		{Name: "backend", Path: "backend", Expanded: false, Order: 2},
	}

	// Save using NewGroupTreeWithGroups (the fixed path).
	groupTree := NewGroupTreeWithGroups(instances, storedGroups)
	if err := s.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	// Reload and verify sort_order is preserved.
	_, reloadedGroups, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups failed: %v", err)
	}

	groupByPath := make(map[string]*GroupData, len(reloadedGroups))
	for _, g := range reloadedGroups {
		groupByPath[g.Path] = g
	}

	for _, want := range storedGroups {
		got, ok := groupByPath[want.Path]
		if !ok {
			t.Fatalf("group %q not found after reload", want.Path)
		}
		if got.Order != want.Order {
			t.Errorf("group %q: Order = %d, want %d", want.Path, got.Order, want.Order)
		}
		if got.Expanded != want.Expanded {
			t.Errorf("group %q: Expanded = %v, want %v", want.Path, got.Expanded, want.Expanded)
		}
	}

	// Verify the bug: NewGroupTree (without stored groups) loses custom order.
	resetTree := NewGroupTree(instances)
	infraGroup := resetTree.Groups["infra"]
	backendGroup := resetTree.Groups["backend"]
	if infraGroup.Order == 0 && backendGroup.Order == 2 {
		t.Error("NewGroupTree unexpectedly preserved custom order; test premise is wrong")
	}
}
