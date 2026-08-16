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

// watcherCLIProfile is passed to the CLI as -p so the state db these tests open
// is the one it wrote. $AGENTDECK_PROFILE is not enough: the watcher commands
// take the profile from the global flag and fall back to "default".
const watcherCLIProfile = "watcher_create_test"

// watcherCLIHome creates the sandbox home for a runAgentDeck test and points
// this process at the same paths the child resolves, so session.WatcherNameDir
// and GetDBPathForProfile agree with the CLI about where its state landed.
func watcherCLIHome(t *testing.T) string {
	t.Helper()
	// Build before HOME moves: the build would otherwise re-download the module
	// cache into the sandbox, and leave root-owned mod cache files behind that
	// t.TempDir cleanup cannot remove.
	channelsCLIBinary(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", "")
	return home
}

// loadWatcherRow reads the row the CLI published, or nil when the watcher was
// never registered (including when the db does not exist at all).
func loadWatcherRow(t *testing.T, name string) *statedb.WatcherRow {
	t.Helper()
	dbPath, err := session.GetDBPathForProfile(watcherCLIProfile)
	if err != nil {
		t.Fatalf("GetDBPathForProfile: %v", err)
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open statedb: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row, err := db.LoadWatcherByName(name)
	if err != nil {
		t.Fatalf("LoadWatcherByName: %v", err)
	}
	return row
}

// TestWatcherCreate_DuplicateNameRejectedBeforeDBMutation pins the coherence
// contract: a watcher is only ever described by one command. Re-creating a name
// used to write the row and not the config — SaveWatcher is INSERT OR REPLACE
// over a UNIQUE name, so the second create swapped in a row with a fresh id
// (orphaning that watcher's watcher_events) while watcher.toml kept the first
// create's topic, and the second caller was told "Created watcher" for settings
// that never took effect.
//
// It also pins the disclosure contract: the rejected topic is a bearer
// capability and must not be repeated back to the terminal.
func TestWatcherCreate_DuplicateNameRejectedBeforeDBMutation(t *testing.T) {
	home := watcherCLIHome(t)

	const (
		watcherName = "dup-create-watcher"
		firstTopic  = "first-topic-that-must-survive"
		secondTopic = "second-topic-that-must-not-be-echoed"
	)

	if _, stderr, code := runAgentDeck(t, home,
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", watcherName, "--topic", firstTopic); code != 0 {
		t.Fatalf("first create failed (exit %d): %s", code, stderr)
	}

	dir, err := session.WatcherNameDir(watcherName)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	tomlPath := filepath.Join(dir, "watcher.toml")
	before, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read watcher.toml after the first create: %v", err)
	}
	firstRow := loadWatcherRow(t, watcherName)
	if firstRow == nil {
		t.Fatalf("the first create did not register %q", watcherName)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", watcherCLIProfile, "watcher", "create", "ntfy", "--name", watcherName, "--topic", secondTopic)
	if code == 0 {
		t.Fatalf("re-creating an existing watcher succeeded; the name is not fresh and its config cannot be updated by create\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// A create exits non-zero from earlier steps too (flag parsing, db open,
	// dir resolution). Match the message the freshness check emits so a broken
	// sandbox fails this test instead of passing it for the wrong reason.
	if !strings.Contains(stderr, "already exists (type:") {
		t.Fatalf("create failed before the freshness check, so this proves nothing\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	if strings.Contains(stdout+stderr, secondTopic) {
		t.Errorf("the rejected topic was echoed to the terminal; an ntfy/slack topic is a bearer capability "+
			"and stderr outlives the command in scrollback, CI logs and recordings\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	after, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read watcher.toml after the rejected create: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("watcher.toml was modified by a create that failed:\n--- got ---\n%s\n--- want ---\n%s", after, before)
	}

	secondRow := loadWatcherRow(t, watcherName)
	if secondRow == nil {
		t.Fatalf("the rejected create deleted the existing watcher row for %q", watcherName)
	}
	if secondRow.ID != firstRow.ID || !secondRow.CreatedAt.Equal(firstRow.CreatedAt) {
		t.Errorf("the rejected create still mutated the state db: row id %q -> %q. "+
			"INSERT OR REPLACE swaps in a new id, orphaning watcher_events, while the config on disk keeps the first create's topic",
			firstRow.ID, secondRow.ID)
	}
}

// TestWatcherCreate_ExistingConfigIsRejectedBeforeDBMutation covers the shape a
// losing writer sees: watcher.toml is already there (left by `watcher import`,
// by an interrupted create, or by a create that raced this one and won) with no
// row behind it. Continuing into SaveWatcher would publish a watcher whose row
// comes from this command and whose settings come from someone else's file.
func TestWatcherCreate_ExistingConfigIsRejectedBeforeDBMutation(t *testing.T) {
	home := watcherCLIHome(t)

	const (
		watcherName   = "import-leftover-watcher"
		rejectedTopic = "topic-that-must-not-be-echoed"
	)

	dir, err := session.WatcherNameDir(watcherName)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir watcher dir: %v", err)
	}
	tomlPath := filepath.Join(dir, "watcher.toml")
	// The shape `watcher import` leaves behind: a topic to fill in by hand, plus
	// routing that must not be thrown away.
	existing := `[source]
topic = ""

[routing]
conductor = "client-a"
group = "client-a/inbox"
`
	if err := os.WriteFile(tomlPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed watcher.toml: %v", err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", watcherCLIProfile, "watcher", "create", "slack", "--name", watcherName, "--topic", rejectedTopic)
	if code == 0 {
		t.Fatalf("create succeeded over a watcher.toml it did not write, so the row and the config disagree\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	// As above: pin the step that refused, not just the exit status. This one
	// must fail on the config claim, not on the db lookup — there is no row.
	if !strings.Contains(stderr, "already exists and was left untouched") {
		t.Fatalf("create failed before the config claim, so this proves nothing\nstdout: %s\nstderr: %s", stdout, stderr)
	}

	if strings.Contains(stdout+stderr, rejectedTopic) {
		t.Errorf("the rejected topic was echoed to the terminal; an ntfy/slack topic is a bearer capability "+
			"and stderr outlives the command in scrollback, CI logs and recordings\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read watcher.toml: %v", err)
	}
	if string(got) != existing {
		t.Errorf("the existing watcher.toml was modified:\n--- got ---\n%s\n--- want ---\n%s", got, existing)
	}

	if row := loadWatcherRow(t, watcherName); row != nil {
		t.Errorf("watcher %q was registered (status %q) even though its config was left untouched; "+
			"`watcher start` would run the topic in that file, not the one this command passed",
			watcherName, row.Status)
	}
}
