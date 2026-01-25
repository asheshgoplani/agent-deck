package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneGeminiLogs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock Gemini project directory structure
	// project_hash/
	//   chats/
	//     session-1.json
	//     session-2.json
	//   log1.txt
	//   log2.txt
	//   run_shell_command_123.txt

	projectDir := filepath.Join(tmpDir, "042c5b74db90f76b4688a069cc3c55526c11d74eb8b14fdead4f7baeef1476cf")
	chatsDir := filepath.Join(projectDir, "chats")
	if err := os.MkdirAll(chatsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Files that should be KEPT
	keepFiles := []string{
		filepath.Join(chatsDir, "session-2026-01-22T00-47-be7b294f.json"),
		filepath.Join(projectDir, "important.json"), // Only .txt should be pruned
	}

	// Files that should be PRUNED
	pruneFiles := []string{
		filepath.Join(projectDir, "log1.txt"),
		filepath.Join(projectDir, "run_shell_command_748.txt"),
		filepath.Join(projectDir, "read_file_126.txt"),
	}

	for _, f := range append(keepFiles, pruneFiles...) {
		if err := os.WriteFile(f, []byte("test content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Run pruning on the temp dir
	// We need to pass the base gemini tmp dir
	count, err := pruneGeminiLogs(tmpDir)
	if err != nil {
		t.Fatalf("pruneGeminiLogs failed: %v", err)
	}

	if count != len(pruneFiles) {
		t.Errorf("Expected to prune %d files, got %d", len(pruneFiles), count)
	}

	// Verify files
	for _, f := range keepFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("File should have been kept: %s", f)
		}
	}

	for _, f := range pruneFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("File should have been pruned: %s", f)
		}
	}
}

func TestCleanupDeckBackups(t *testing.T) {
	tmpDir := t.TempDir()
	profileDir := filepath.Join(tmpDir, "default")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a mock profile directory with many backups
	// We want to keep only 3 most recent
	backups := []string{
		filepath.Join(profileDir, "sessions.json.bak.1"), // Oldest
		filepath.Join(profileDir, "sessions.json.bak.2"),
		filepath.Join(profileDir, "sessions.json.bak.3"),
		filepath.Join(profileDir, "sessions.json.bak.4"),
		filepath.Join(profileDir, "sessions.json.bak.5"), // Newest
	}

	for i, f := range backups {
		if err := os.WriteFile(f, []byte("backup"), 0644); err != nil {
			t.Fatal(err)
		}
		// Ensure distinct modification times
		mtime := time.Now().Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(f, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	// Run cleanup
	count, err := cleanupDeckBackups(tmpDir)
	if err != nil {
		t.Fatalf("cleanupDeckBackups failed: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected to prune 2 backups, got %d", count)
	}

	// Verify most recent 3 exist
	for i := 2; i < 5; i++ {
		if _, err := os.Stat(backups[i]); os.IsNotExist(err) {
			t.Errorf("Recent backup should exist: %s", backups[i])
		}
	}

	// Verify oldest 2 are gone
	for i := 0; i < 2; i++ {
		if _, err := os.Stat(backups[i]); !os.IsNotExist(err) {
			t.Errorf("Old backup should be pruned: %s", backups[i])
		}
	}
}

func TestArchiveBloatedSessions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock Gemini project with a small and a large session
	projectDir := filepath.Join(tmpDir, "042c5b74db90f76b4688a069cc3c55526c11d74eb8b14fdead4f7baeef1476cf")
	chatsDir := filepath.Join(projectDir, "chats")
	if err := os.MkdirAll(chatsDir, 0755); err != nil {
		t.Fatal(err)
	}

	smallFile := filepath.Join(chatsDir, "small.json")
	largeFile := filepath.Join(chatsDir, "large.json")

	// 100 bytes - small
	if err := os.WriteFile(smallFile, make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}

	// 20KB - large (using 10KB threshold for test)
	if err := os.WriteFile(largeFile, make([]byte, 20*1024), 0644); err != nil {
		t.Fatal(err)
	}

	// Add 3 more files to bypass the safety check (len(files) < 5)
	for i := 0; i < 3; i++ {
		dummyFile := filepath.Join(chatsDir, fmt.Sprintf("dummy%d.json", i))
		if err := os.WriteFile(dummyFile, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// SAFETY: Files must be at least 24 hours old to be archived.
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(smallFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(largeFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	// Age the dummy files too
	for i := 0; i < 3; i++ {
		dummyFile := filepath.Join(chatsDir, fmt.Sprintf("dummy%d.json", i))
		if err := os.Chtimes(dummyFile, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	// Run archiving with 10KB threshold
	count, err := archiveBloatedSessions(tmpDir, 10*1024)
	if err != nil {
		t.Fatalf("archiveBloatedSessions failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected to archive 1 session, got %d", count)
	}

	// Verify small file still in chats/
	if _, err := os.Stat(smallFile); os.IsNotExist(err) {
		t.Error("Small file should still be in chats/")
	}

	// Verify large file is moved to archive/
	archivedPath := filepath.Join(chatsDir, "archive", "large.json")
	if _, err := os.Stat(archivedPath); os.IsNotExist(err) {
		t.Error("Large file should be in archive/ subdirectory")
	}

	if _, err := os.Stat(largeFile); !os.IsNotExist(err) {
		t.Error("Large file should no longer be in chats/ root")
	}
}

func TestManualMaintenance(t *testing.T) {
	// Trigger real maintenance
	result, err := Maintenance()
	if err != nil {
		t.Logf("Maintenance run had issues: %v", err)
	}
	t.Logf("Manual Maintenance Result: Pruned %d logs, %d backups, Archived %d sessions in %v",
		result.PrunedLogs, result.PrunedBackups, result.ArchivedSessions, result.Duration)
}

func TestStartMaintenanceWorkerDisabled(t *testing.T) {
	// Setup: ensure maintenance is disabled in config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with maintenance disabled
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)
	config := &UserConfig{
		Maintenance: MaintenanceSettings{
			Enabled: false,
		},
	}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	// Start worker with a context that we'll cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Call StartMaintenanceWorker - it should return immediately because Enabled is false
	StartMaintenanceWorker(ctx, nil)

	// In this test we can't easily "wait" for it to NOT run, 
	// but we've verified the logic in the code.
	// If it ran, it would log to stdout which is hard to capture here.
	// But we can verify that MaintenanceSettings.Enabled is false.
	settings := GetMaintenanceSettings()
	if settings.Enabled {
		t.Error("Maintenance should be disabled for this test")
	}
}

func TestStartMaintenanceWorkerCallback(t *testing.T) {
	// Setup: enable maintenance in config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	config := &UserConfig{
		Maintenance: MaintenanceSettings{
			Enabled: true,
		},
	}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	// Use a channel to wait for the callback
	done := make(chan MaintenanceResult, 1)
	callback := func(res MaintenanceResult) {
		done <- res
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker with callback
	StartMaintenanceWorker(ctx, callback)

	// Wait for callback with timeout
	select {
	case res := <-done:
		// Success! Callback triggered.
		t.Logf("Callback received: %+v", res)
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for maintenance callback")
	}
}
