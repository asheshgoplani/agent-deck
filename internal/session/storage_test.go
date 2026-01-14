package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStorageUpdatedAtTimestamp verifies that SaveWithGroups sets the UpdatedAt timestamp
// and GetUpdatedAt() returns it correctly.
func TestStorageUpdatedAtTimestamp(t *testing.T) {
	// Create temporary directory for test
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")

	// Create storage instance
	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	// Create test data
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
	time.Sleep(10 * time.Millisecond) // Small delay to ensure timestamp differs

	err := s.SaveWithGroups(instances, nil)
	if err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // Small delay to ensure timestamp differs
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

// TestGetUpdatedAtNoFile verifies behavior when storage file doesn't exist
func TestGetUpdatedAtNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "nonexistent.json")

	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	_, err := s.GetUpdatedAt()
	if err == nil {
		t.Error("Expected error when file doesn't exist, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Expected IsNotExist error, got: %v", err)
	}
}

func TestStoragePersistsGroupDefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")

	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	now := time.Now()
	instances := []*Instance{
		{
			ID:             "1",
			Title:          "old",
			ProjectPath:    "/tmp/old",
			GroupPath:      "work",
			Command:        "claude",
			Tool:           "claude",
			Status:         StatusIdle,
			CreatedAt:      now.Add(-2 * time.Hour),
			LastAccessedAt: now.Add(-90 * time.Minute),
		},
		{
			ID:             "2",
			Title:          "new",
			ProjectPath:    "/tmp/new",
			GroupPath:      "work",
			Command:        "claude",
			Tool:           "claude",
			Status:         StatusIdle,
			CreatedAt:      now.Add(-1 * time.Hour),
			LastAccessedAt: now.Add(-30 * time.Minute),
		},
	}

	groupTree := NewGroupTree(instances)
	if err := s.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	_, groups, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups failed: %v", err)
	}

	var workGroup *GroupData
	for _, g := range groups {
		if g.Path == "work" {
			workGroup = g
			break
		}
	}
	if workGroup == nil {
		t.Fatal("work group not found in stored groups")
	}
	if workGroup.DefaultPath != "/tmp/new" {
		t.Errorf("DefaultPath = %q, want %q", workGroup.DefaultPath, "/tmp/new")
	}
}

func TestStoragePersistsWorktreeFields(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")

	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	instances := []*Instance{
		{
			ID:               "1",
			Title:            "worktree-session",
			ProjectPath:      "/tmp/repo/.worktrees/feature",
			GroupPath:        "work",
			WorktreePath:     "/tmp/repo/.worktrees/feature",
			WorktreeRepoRoot: "/tmp/repo",
			WorktreeBranch:   "feature",
			Command:          "claude",
			Tool:             "claude",
			Status:           StatusIdle,
			CreatedAt:        time.Now(),
		},
	}

	groupTree := NewGroupTree(instances)
	if err := s.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	loadedInstances, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups failed: %v", err)
	}
	if len(loadedInstances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(loadedInstances))
	}
	loaded := loadedInstances[0]
	if loaded.WorktreePath != "/tmp/repo/.worktrees/feature" {
		t.Errorf("WorktreePath = %q, want %q", loaded.WorktreePath, "/tmp/repo/.worktrees/feature")
	}
	if loaded.WorktreeRepoRoot != "/tmp/repo" {
		t.Errorf("WorktreeRepoRoot = %q, want %q", loaded.WorktreeRepoRoot, "/tmp/repo")
	}
	if loaded.WorktreeBranch != "feature" {
		t.Errorf("WorktreeBranch = %q, want %q", loaded.WorktreeBranch, "feature")
	}
}
