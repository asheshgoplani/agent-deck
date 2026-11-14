package watcher

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSourceSettings_ReadsSourceTable(t *testing.T) {
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

	got, err := loadSourceSettings(name)
	if err != nil {
		t.Fatalf("loadSourceSettings: %v", err)
	}
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

func TestLoadSourceSettings_MissingFileReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := loadSourceSettings("does-not-exist")
	if err != nil {
		t.Fatalf("loadSourceSettings: %v", err)
	}
	if got == nil {
		t.Fatal("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v; want empty map", got)
	}
}

func TestLoadSourceSettings_NoSourceSectionReturnsEmpty(t *testing.T) {
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

	got, err := loadSourceSettings(name)
	if err != nil {
		t.Fatalf("loadSourceSettings: %v", err)
	}
	if got == nil {
		t.Fatal("got nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %v; want empty map", got)
	}
}

// A watcher.toml that exists but does not parse is an error naming the
// file, not empty settings: for a webhook, empty settings would mean an
// unsigned listener.
func TestLoadSourceSettings_MalformedTOMLIsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	name := "bad-toml"
	dir := filepath.Join(home, ".agent-deck", "watcher", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "watcher.toml")
	if err := os.WriteFile(path, []byte("this is not = valid toml ["), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := loadSourceSettings(name)
	if err == nil {
		t.Fatalf("got %v and no error; want a parse error", got)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
}

// At engine start a webhook watcher whose watcher.toml does not parse fails
// its Setup, with the file named in the log, instead of starting unsigned
// with default settings.
func TestStartEngine_MalformedWatcherTomlFailsSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	db := newTestDB(t)
	saveTestWatcher(t, db, "w-bad", "bad-hook", "webhook")
	dir := filepath.Join(home, ".agent-deck", "watcher", "bad-hook")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "watcher.toml")
	if err := os.WriteFile(path, []byte("[source\nsecret = \"0123456789abcdef0123456789abcdef\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	eng := startEngine(HostConfig{Profile: "badtoml", DB: db}, slog.New(slog.NewTextHandler(&logs, nil)))
	if eng == nil {
		t.Fatal("startEngine returned no engine")
	}
	t.Cleanup(eng.Stop)

	adapters := eng.Adapters()
	if len(adapters) != 1 {
		t.Fatalf("engine has %d adapters, want 1", len(adapters))
	}
	if adapters[0].Running {
		t.Fatal("the webhook watcher runs although its watcher.toml does not parse")
	}
	if !strings.Contains(logs.String(), path) {
		t.Fatalf("log does not name %s:\n%s", path, logs.String())
	}
}
