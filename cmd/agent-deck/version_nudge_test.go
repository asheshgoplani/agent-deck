package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// writeTestCache mirrors update.saveCache on-disk layout so the offline
// CachedUpdateInfo read path finds what it expects. Kept local rather
// than exporting internals.
func writeTestCache(t *testing.T, cache *update.UpdateCache) error {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, update.CacheFileName), data, 0o644)
}

func isolateVersionUpdatePaths(t *testing.T) {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, "xdg-cache"))
}

// Conductor task #45 — `agent-deck --version` should append
// "(update available: vX.Y.Z)" when the disk cache shows the user is
// behind. The annotation must be cache-only (no network hit — --version
// should stay instant).

func TestVersionOutput_AppendsUpdateAnnotationWhenBehind(t *testing.T) {
	isolateVersionUpdatePaths(t)

	// Seed a cache entry claiming 1.7.20 is well behind 1.7.58.
	cache := &update.UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.20",
		ReleasesBehind: 38,
	}
	if err := writeTestCache(t, cache); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var buf bytes.Buffer
	writeVersionOutput(&buf, "1.7.20")
	got := buf.String()

	want := "Agent Deck v1.7.20 (update available: v1.7.58)\n"
	if got != want {
		t.Fatalf("version output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestVersionOutput_NoAnnotationWhenUpToDate(t *testing.T) {
	isolateVersionUpdatePaths(t)

	cache := &update.UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.58",
		ReleasesBehind: 0,
	}
	if err := writeTestCache(t, cache); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var buf bytes.Buffer
	writeVersionOutput(&buf, "1.7.58")
	got := buf.String()

	want := "Agent Deck v1.7.58\n"
	if got != want {
		t.Fatalf("version output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestVersionOutput_NoAnnotationWhenNoCache(t *testing.T) {
	// Fresh install: no cache file yet. --version must still print
	// cleanly — we never hit the network on --version.
	isolateVersionUpdatePaths(t)

	var buf bytes.Buffer
	writeVersionOutput(&buf, "1.7.20")
	got := buf.String()

	want := "Agent Deck v1.7.20\n"
	if got != want {
		t.Fatalf("version output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestVersionOutput_NoAnnotationWhenEnvSkipped(t *testing.T) {
	// AGENTDECK_SKIP_UPDATE_CHECK must strip the annotation too — some
	// users export this to silence all update nagging.
	isolateVersionUpdatePaths(t)
	t.Setenv("AGENTDECK_SKIP_UPDATE_CHECK", "1")

	cache := &update.UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.58",
		CurrentVersion: "1.7.20",
		ReleasesBehind: 38,
	}
	if err := writeTestCache(t, cache); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	var buf bytes.Buffer
	writeVersionOutput(&buf, "1.7.20")
	got := buf.String()

	want := "Agent Deck v1.7.20\n"
	if got != want {
		t.Fatalf("version output mismatch:\n got: %q\nwant: %q", got, want)
	}
}
