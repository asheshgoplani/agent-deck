package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

// TestWriteTopicWatcherSource_ConcurrentCreateKeepsOneWriter pins the
// no-overwrite guarantee under a race. A stat-then-write cannot hold this:
// every goroutine would see "absent" and the last writer would win. Exactly one
// caller must report written=true, and the file must match that caller.
func TestWriteTopicWatcherSource_ConcurrentCreateKeepsOneWriter(t *testing.T) {
	dir := t.TempDir()

	const writers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	for i := 0; i < writers; i++ {
		topic := "topic-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			written, err := writeTopicWatcherSource(dir, "ntfy", topic)
			if err != nil {
				t.Errorf("writeTopicWatcherSource(%s): %v", topic, err)
				return
			}
			if written {
				mu.Lock()
				winners = append(winners, topic)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d writers reported written=true, want exactly 1: %v", len(winners), winners)
	}
	if got := decodeWatcherSource(t, filepath.Join(dir, "watcher.toml"))["topic"]; got != winners[0] {
		t.Errorf("[source].topic = %q, want %q (the one writer that claimed the file)", got, winners[0])
	}

	// The temp file each loser wrote must not survive as clutter.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "watcher.toml" {
			t.Errorf("leftover file in watcher dir: %s", e.Name())
		}
	}
}

// TestWatcherCreate_FailedSourceWriteLeavesNoWatcher proves the ordering: when
// the [source] write fails, `watcher create` must not leave behind a registered
// watcher that can never start. handleWatcherCreate calls os.Exit, so the create
// runs in a subprocess and the parent inspects the state db it left behind.
func TestWatcherCreate_FailedSourceWriteLeavesNoWatcher(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory would not block the write")
	}

	const watcherName = "create-order-test"

	if os.Getenv("AGENT_DECK_CREATE_ORDER_CHILD") == "1" {
		// Child: HOME is already the sandbox the parent set up.
		handleWatcherCreate("_test", []string{
			"ntfy", "--name", watcherName, "--topic", "should-not-persist",
		})
		return
	}

	home := t.TempDir()

	// Resolve the watcher dir under the child's HOME and make it unwritable, so
	// the [source] write is the step that fails.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	watcherDir, err := session.WatcherNameDir(watcherName)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	if err := os.MkdirAll(watcherDir, 0o700); err != nil {
		t.Fatalf("mkdir watcher dir: %v", err)
	}
	if err := os.Chmod(watcherDir, 0o500); err != nil {
		t.Fatalf("chmod watcher dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(watcherDir, 0o700) })

	dbPath, err := session.GetDBPathForProfile("_test")
	if err != nil {
		t.Fatalf("GetDBPathForProfile: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestWatcherCreate_FailedSourceWriteLeavesNoWatcher")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_CREATE_ORDER_CHILD=1",
		// Keep TestMain from re-isolating HOME on top of the sandbox above.
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+home,
		"XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "XDG_CACHE_HOME=",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("create succeeded despite an unwritable watcher dir; output:\n%s", out)
	}
	// handleWatcherCreate exits non-zero from several earlier steps too (db open,
	// dir resolution). Match the message the [source] write emits, so a sandbox
	// that broke one of those steps fails this test instead of passing it for
	// the wrong reason.
	if !strings.Contains(string(out), "Error writing watcher config") {
		t.Fatalf("create failed before the [source] write, so this proves nothing; output:\n%s", out)
	}

	// The DB may not exist at all, which also satisfies "no watcher published".
	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		return
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open statedb: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row, err := db.LoadWatcherByName(watcherName)
	if err != nil {
		t.Fatalf("LoadWatcherByName: %v", err)
	}
	if row != nil {
		t.Errorf("watcher %q was registered even though its [source] write failed (status=%q); "+
			"a failed write must not publish a watcher that can never start", watcherName, row.Status)
	}
}
