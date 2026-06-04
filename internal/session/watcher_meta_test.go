package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testWatcherHome(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", "")
	return tmpDir
}

func testWatcherNameDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := WatcherNameDir(name)
	if err != nil {
		t.Fatalf("WatcherNameDir(%q): %v", name, err)
	}
	return dir
}

func TestWatcherMetaRoundTrip(t *testing.T) {
	// Use a temp HOME directory to avoid touching real ~/.agent-deck
	testWatcherHome(t)

	meta := &WatcherMeta{
		Name:      "test-watcher",
		Type:      "webhook",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta: %v", err)
	}

	// Verify file was created at expected path
	expectedPath := filepath.Join(testWatcherNameDir(t, "test-watcher"), "meta.json")
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
	testWatcherHome(t)

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
	testWatcherHome(t)

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
	metaPath := filepath.Join(testWatcherNameDir(t, "backfill-test"), "meta.json")
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

// TestWatcherMetaRoundTrip_GmailFields verifies WatchExpiry + WatchHistoryID
// round-trip through Save/Load and that empty values omit from JSON so that
// legacy Phase 13/14 watchers (webhook, ntfy, github, slack) still parse cleanly.
func TestWatcherMetaRoundTrip_GmailFields(t *testing.T) {
	testWatcherHome(t)

	expiry := time.Now().Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	meta := &WatcherMeta{
		Name:           "gmail-test",
		Type:           "gmail",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		WatchExpiry:    expiry,
		WatchHistoryID: "1234567890",
	}
	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta: %v", err)
	}

	loaded, err := LoadWatcherMeta("gmail-test")
	if err != nil {
		t.Fatalf("LoadWatcherMeta: %v", err)
	}
	if loaded.WatchExpiry != expiry {
		t.Errorf("WatchExpiry mismatch: got %q want %q", loaded.WatchExpiry, expiry)
	}
	if loaded.WatchHistoryID != "1234567890" {
		t.Errorf("WatchHistoryID mismatch: got %q want %q", loaded.WatchHistoryID, "1234567890")
	}

	// Backward compatibility: a WatcherMeta WITHOUT the gmail fields must still
	// round-trip and the loaded struct must have empty WatchExpiry/WatchHistoryID.
	legacy := &WatcherMeta{
		Name:      "legacy",
		Type:      "ntfy",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveWatcherMeta(legacy); err != nil {
		t.Fatalf("SaveWatcherMeta(legacy): %v", err)
	}
	loadedLegacy, err := LoadWatcherMeta("legacy")
	if err != nil {
		t.Fatalf("LoadWatcherMeta(legacy): %v", err)
	}
	if loadedLegacy.WatchExpiry != "" || loadedLegacy.WatchHistoryID != "" {
		t.Errorf("legacy WatcherMeta should have empty gmail fields, got expiry=%q history=%q",
			loadedLegacy.WatchExpiry, loadedLegacy.WatchHistoryID)
	}
}

// TestSaveWatcherMeta_AtomicWrite verifies that a successful save leaves
// exactly meta.json on disk with no .tmp file remnant, and that a stale
// .tmp file left behind by a previous crashed run is overwritten cleanly.
func TestSaveWatcherMeta_AtomicWrite(t *testing.T) {
	testWatcherHome(t)

	meta := &WatcherMeta{
		Name:      "atomic-test",
		Type:      "gmail",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta: %v", err)
	}

	dir := testWatcherNameDir(t, "atomic-test")
	finalPath := filepath.Join(dir, "meta.json")
	tmpPath := finalPath + ".tmp"

	// Final file must exist
	if _, err := os.Stat(finalPath); err != nil {
		t.Errorf("final meta.json not found: %v", err)
	}
	// Temp file must NOT exist after successful save
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("meta.json.tmp leaked after successful save")
	}

	// Simulate a crash that left a stale .tmp file behind from a previous run.
	// SaveWatcherMeta should overwrite it cleanly (write to .tmp then rename).
	if err := os.WriteFile(tmpPath, []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed stale tmp: %v", err)
	}
	meta.Type = "gmail-updated"
	if err := SaveWatcherMeta(meta); err != nil {
		t.Fatalf("SaveWatcherMeta after stale tmp: %v", err)
	}
	loaded, err := LoadWatcherMeta("atomic-test")
	if err != nil {
		t.Fatalf("LoadWatcherMeta: %v", err)
	}
	if loaded.Type != "gmail-updated" {
		t.Errorf("Type mismatch after recovery: got %q want %q", loaded.Type, "gmail-updated")
	}
	// Temp file must NOT exist after this save either
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("meta.json.tmp leaked after second save")
	}
}

func TestWatcherDirHelpers(t *testing.T) {
	tmpDir := testWatcherHome(t)

	dir, err := WatcherDir()
	if err != nil {
		t.Fatalf("WatcherDir: %v", err)
	}
	expected := filepath.Join(tmpDir, ".local", "share", "agent-deck", "watcher")
	if dir != expected {
		t.Errorf("WatcherDir() = %q, want %q", dir, expected)
	}

	nameDir, err := WatcherNameDir("my-watcher")
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	expectedName := filepath.Join(expected, "my-watcher")
	if nameDir != expectedName {
		t.Errorf("WatcherNameDir() = %q, want %q", nameDir, expectedName)
	}
}
