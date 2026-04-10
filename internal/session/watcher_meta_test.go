package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherMetaRoundTrip(t *testing.T) {
	// Use a temp HOME directory to avoid touching real ~/.agent-deck
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	meta := &WatcherMeta{
		Name:      "test-watcher",
		Type:      "webhook",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta: %v", err)
	}

	// Verify file was created at expected path
	expectedPath := filepath.Join(tmpDir, ".agent-deck", "watchers", "test-watcher", "meta.json")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("meta.json not created at expected path: %s", expectedPath)
	}

	loaded, err := LoadWatcherMeta("test-watcher")
	if err != nil {
		t.Fatalf("LoadWatcherMeta: %v", err)
	}

	if loaded.Name != meta.Name {
		t.Errorf("Name mismatch: got %q, want %q", loaded.Name, meta.Name)
	}
	if loaded.Type != meta.Type {
		t.Errorf("Type mismatch: got %q, want %q", loaded.Type, meta.Type)
	}
	if loaded.CreatedAt != meta.CreatedAt {
		t.Errorf("CreatedAt mismatch: got %q, want %q", loaded.CreatedAt, meta.CreatedAt)
	}
}

func TestWatcherMetaSaveValidation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// nil meta should error
	if err := SaveWatcherMeta(nil); err == nil {
		t.Error("SaveWatcherMeta(nil) should return error")
	}

	// empty name should error
	if err := SaveWatcherMeta(&WatcherMeta{}); err == nil {
		t.Error("SaveWatcherMeta with empty name should return error")
	}
}

func TestWatcherMetaLoadBackfillsName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Save a meta with a name, then manually edit to remove the name field
	meta := &WatcherMeta{
		Name:      "backfill-test",
		Type:      "ntfy",
		CreatedAt: "2026-04-10T12:00:00Z",
	}
	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta: %v", err)
	}

	// Overwrite with JSON missing the name field
	metaPath := filepath.Join(tmpDir, ".agent-deck", "watchers", "backfill-test", "meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"type":"ntfy","created_at":"2026-04-10T12:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("overwrite meta.json: %v", err)
	}

	loaded, err := LoadWatcherMeta("backfill-test")
	if err != nil {
		t.Fatalf("LoadWatcherMeta: %v", err)
	}
	if loaded.Name != "backfill-test" {
		t.Errorf("expected Name to be backfilled to %q, got %q", "backfill-test", loaded.Name)
	}
}

func TestWatcherDirHelpers(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	dir, err := WatcherDir()
	if err != nil {
		t.Fatalf("WatcherDir: %v", err)
	}
	expected := filepath.Join(tmpDir, ".agent-deck", "watchers")
	if dir != expected {
		t.Errorf("WatcherDir() = %q, want %q", dir, expected)
	}

	nameDir, err := WatcherNameDir("my-watcher")
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	expectedName := filepath.Join(tmpDir, ".agent-deck", "watchers", "my-watcher")
	if nameDir != expectedName {
		t.Errorf("WatcherNameDir() = %q, want %q", nameDir, expectedName)
	}
}
