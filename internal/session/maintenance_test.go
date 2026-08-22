package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneGeminiLogs(t *testing.T) {
	// Create temp dir with Gemini-like structure: tmp/hash1/*.txt and tmp/hash1/chats/*.json
	base := t.TempDir()
	tmpDir := filepath.Join(base, "tmp")
	hashDir := filepath.Join(tmpDir, "abc123hash")
	chatsDir := filepath.Join(hashDir, "chats")

	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create .txt log files (should be pruned)
	txtFiles := []string{"output.txt", "debug.txt", "session.txt"}
	for _, f := range txtFiles {
		if err := os.WriteFile(filepath.Join(hashDir, f), []byte("log data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create .json chat files (should be preserved)
	jsonFiles := []string{"chat1.json", "chat2.json"}
	for _, f := range jsonFiles {
		if err := os.WriteFile(filepath.Join(chatsDir, f), []byte(`{"role":"user"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Call pruneGeminiLogs
	pruneGeminiLogs(base)

	// Verify .txt files were deleted
	for _, f := range txtFiles {
		p := filepath.Join(hashDir, f)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be deleted, but it still exists", f)
		}
	}

	// Verify .json chat files were preserved
	for _, f := range jsonFiles {
		p := filepath.Join(chatsDir, f)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to be preserved, but got error: %v", f, err)
		}
	}
}

func TestCleanupDeckBackups(t *testing.T) {
	// Create temp dir with backup files having staggered mtimes
	base := t.TempDir()
	profileDir := filepath.Join(base, "profile1")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Create .bak.1 through .bak.5 with staggered times (oldest to newest)
	for i := 1; i <= 5; i++ {
		p := filepath.Join(profileDir, "sessions.json.bak."+string(rune('0'+i)))
		if err := os.WriteFile(p, []byte("backup data"), 0o644); err != nil {
			t.Fatal(err)
		}
		// .bak.1 is oldest (5 hours ago), .bak.5 is newest (1 hour ago)
		mtime := now.Add(-time.Duration(6-i) * time.Hour)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	// Call cleanupDeckBackups
	cleanupDeckBackups(base)

	// Verify only 3 most recent backups kept (.bak.3, .bak.4, .bak.5)
	for i := 1; i <= 5; i++ {
		p := filepath.Join(profileDir, "sessions.json.bak."+string(rune('0'+i)))
		_, err := os.Stat(p)
		if i <= 2 {
			// Oldest two should be deleted
			if !os.IsNotExist(err) {
				t.Errorf("expected .bak.%d to be deleted (oldest), but it still exists", i)
			}
		} else {
			// Newest three should be kept
			if err != nil {
				t.Errorf("expected .bak.%d to be preserved (recent), but got error: %v", i, err)
			}
		}
	}
}

func TestArchiveBloatedSessions(t *testing.T) {
	base := t.TempDir()
	profileDir := filepath.Join(base, "profiles", "default")
	archiveDir := filepath.Join(base, "profiles", "default", "archive")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	oldTime := now.Add(-48 * time.Hour) // 2 days ago

	// Create a 31MB old file (should be archived)
	bigOldFile := filepath.Join(profileDir, "big_old_session.json")
	bigData := make([]byte, 31*1024*1024) // 31MB
	if err := os.WriteFile(bigOldFile, bigData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bigOldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create a 1KB old file (should NOT be archived - too small)
	smallOldFile := filepath.Join(profileDir, "small_old_session.json")
	if err := os.WriteFile(smallOldFile, []byte("small data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(smallOldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create a recent large file (should NOT be archived - too recent)
	bigNewFile := filepath.Join(profileDir, "big_new_session.json")
	if err := os.WriteFile(bigNewFile, bigData, 0o644); err != nil {
		t.Fatal(err)
	}
	// Leave mtime as now (recent)

	// Create additional small files to reach 5+ total files in the directory
	for i := 0; i < 3; i++ {
		filler := filepath.Join(profileDir, "filler_"+string(rune('a'+i))+".json")
		if err := os.WriteFile(filler, []byte("filler"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Call archiveBloatedSessions
	archiveBloatedSessions(base)

	// Verify only the old large file was moved to archive/
	if _, err := os.Stat(bigOldFile); !os.IsNotExist(err) {
		t.Errorf("expected big_old_session.json to be moved to archive, but it still exists")
	}
	archived := filepath.Join(archiveDir, "big_old_session.json")
	if _, err := os.Stat(archived); err != nil {
		t.Errorf("expected big_old_session.json in archive/, but got error: %v", err)
	}

	// Verify small old file was NOT archived
	if _, err := os.Stat(smallOldFile); err != nil {
		t.Errorf("expected small_old_session.json to remain, but got error: %v", err)
	}

	// Verify recent large file was NOT archived
	if _, err := os.Stat(bigNewFile); err != nil {
		t.Errorf("expected big_new_session.json to remain (recent), but got error: %v", err)
	}
}

func TestStartMaintenanceWorkerDisabled(t *testing.T) {
	// Create temp dir as HOME with no config (maintenance disabled by default)
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create minimal agent-deck dir (no maintenance config)
	if err := os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	callback := func(r MaintenanceResult) {
		called = true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ClearUserConfigCache()
	StartMaintenanceWorker(ctx, callback)

	// Wait for context to expire
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond) // small grace period

	if called {
		t.Error("expected callback NOT to be called when maintenance is disabled")
	}
}

func TestStartMaintenanceWorkerDoneClosesAfterCancel(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o755); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	ctx, cancel := context.WithCancel(context.Background())
	done := StartMaintenanceWorker(ctx, nil)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance worker did not report completion after cancellation")
	}
}

func TestStartMaintenanceWorkerCallback(t *testing.T) {
	// Create temp dir as HOME with maintenance enabled in config
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	deckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(deckDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write config.toml with maintenance enabled
	configContent := []byte("[maintenance]\nenabled = true\ninterval_hours = 0\n")
	if err := os.WriteFile(filepath.Join(deckDir, "config.toml"), configContent, 0o644); err != nil {
		t.Fatal(err)
	}

	called := make(chan struct{}, 1)
	callback := func(r MaintenanceResult) {
		select {
		case called <- struct{}{}:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ClearUserConfigCache()
	StartMaintenanceWorker(ctx, callback)

	select {
	case <-called:
		// Success: callback was fired
	case <-ctx.Done():
		t.Error("expected callback to be called when maintenance is enabled, but it was not")
	}
}
