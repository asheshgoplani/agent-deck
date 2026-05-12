package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWatcherSourceSettings_ReadsSourceTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	name := "gh-watcher"
	dir := filepath.Join(home, ".agent-deck", "watcher", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `[watcher]
name = "gh-watcher"
type = "github"

[source]
secret = "deadbeef"
port = "19999"
bind = "127.0.0.1"

[routing]
conductor = "conductor-github"
group = "conductor"
`
	if err := os.WriteFile(filepath.Join(dir, "watcher.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := loadWatcherSourceSettings(name)
	want := map[string]string{
		"secret": "deadbeef",
		"port":   "19999",
		"bind":   "127.0.0.1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadWatcherSourceSettings_MissingFileReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := loadWatcherSourceSettings("does-not-exist")
	if got == nil {
		t.Fatal("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v; want empty map", got)
	}
}

func TestLoadWatcherSourceSettings_NoSourceSectionReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	name := "no-source"
	dir := filepath.Join(home, ".agent-deck", "watcher", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `[watcher]
name = "no-source"
type = "webhook"

[routing]
conductor = "conductor-x"
group = "g"
`
	if err := os.WriteFile(filepath.Join(dir, "watcher.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := loadWatcherSourceSettings(name)
	if got == nil {
		t.Fatal("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v; want empty map", got)
	}
}

func TestLoadWatcherSourceSettings_MalformedTOMLReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	name := "bad-toml"
	dir := filepath.Join(home, ".agent-deck", "watcher", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "watcher.toml"), []byte("this is not = valid toml ["), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := loadWatcherSourceSettings(name)
	if got == nil {
		t.Fatal("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v; want empty map", got)
	}
}
