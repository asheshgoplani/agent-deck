package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck watcher create ntfy|slack --topic X` validated --topic and then
// dropped it: only the github path wrote a watcher.toml. The engine reads
// adapter settings back from that file's [source] table
// (internal/ui/home.go loadWatcherSourceSettings), so Settings["topic"] was
// always empty at runtime and NtfyAdapter/SlackAdapter Setup always failed —
// the watcher never ran.

// decodeWatcherSource reads the [source] table exactly the way the runtime
// engine does, so these tests fail if the file is written in a shape the
// engine cannot consume (e.g. a bare int for a map[string]string field).
func decodeWatcherSource(t *testing.T, path string) map[string]string {
	t.Helper()
	var cfg struct {
		Source map[string]string `toml:"source"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return cfg.Source
}

// TestWatcherCreate_PersistsTopicForEngine drives the real create handler and
// asserts the topic survives into the file the engine loads at startup.
func TestWatcherCreate_PersistsTopicForEngine(t *testing.T) {
	tests := []struct {
		adapterType string
		name        string
		topic       string
	}{
		{adapterType: "ntfy", name: "create-topic-ntfy", topic: "my-private-topic"},
		{adapterType: "slack", name: "create-topic-slack", topic: "my-slack-topic"},
	}

	for _, tt := range tests {
		t.Run(tt.adapterType, func(t *testing.T) {
			handleWatcherCreate("_test", []string{
				tt.adapterType, "--name", tt.name, "--topic", tt.topic,
			})

			dir, err := session.WatcherNameDir(tt.name)
			if err != nil {
				t.Fatalf("WatcherNameDir: %v", err)
			}
			source := decodeWatcherSource(t, filepath.Join(dir, "watcher.toml"))
			if source["topic"] != tt.topic {
				t.Errorf("[source].topic = %q, want %q — the engine cannot start the adapter without it",
					source["topic"], tt.topic)
			}
		})
	}
}

// TestWriteTopicWatcherSource_Writes0600 pins the file mode and the reported
// outcome for a fresh watcher directory.
func TestWriteTopicWatcherSource_Writes0600(t *testing.T) {
	dir := t.TempDir()

	written, err := writeTopicWatcherSource(dir, "ntfy", "phone-alerts")
	if err != nil {
		t.Fatalf("writeTopicWatcherSource: %v", err)
	}
	if !written {
		t.Fatal("written = false for a directory with no watcher.toml")
	}

	path := filepath.Join(dir, "watcher.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat watcher.toml: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("watcher.toml mode = %#o, want 0600", perm)
	}
	if got := decodeWatcherSource(t, path)["topic"]; got != "phone-alerts" {
		t.Errorf("[source].topic = %q, want %q", got, "phone-alerts")
	}
}

// TestWriteTopicWatcherSource_KeepsExistingConfig guards the case that matters
// most: `watcher import` writes a watcher.toml carrying [routing], and
// re-creating that watcher must not throw the routing away.
func TestWriteTopicWatcherSource_KeepsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watcher.toml")
	existing := `[source]
topic = "already-set"

[routing]
conductor = "client-a"
group = "client-a/inbox"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed watcher.toml: %v", err)
	}

	written, err := writeTopicWatcherSource(dir, "slack", "new-topic")
	if err != nil {
		t.Fatalf("writeTopicWatcherSource: %v", err)
	}
	if written {
		t.Error("written = true; an existing watcher.toml must be left alone")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read watcher.toml: %v", err)
	}
	if string(got) != existing {
		t.Errorf("watcher.toml was modified:\n--- got ---\n%s\n--- want ---\n%s", got, existing)
	}
}
