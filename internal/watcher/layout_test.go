package watcher

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// restoreDefaultLogger restores the default slog logger after test-local capture.
func restoreDefaultLogger(t *testing.T, prev *slog.Logger) {
	t.Helper()
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})
}

// captureLog redirects slog output to a buffer and returns a pointer to it.
// The previous default logger is restored via t.Cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))
	restoreDefaultLogger(t, prev)
	return &buf
}

// legacyAgentDeckDir returns ~/.agent-deck for the current HOME.
func legacyAgentDeckDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, ".agent-deck")
}

func effectiveAgentDeckDir(t *testing.T) string {
	t.Helper()
	watcherDir, err := session.WatcherDir()
	if err != nil {
		t.Fatalf("WatcherDir: %v", err)
	}
	return filepath.Dir(watcherDir)
}

func isolateWatcherLayoutHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
}

// TestLayout_FreshInstallCreatesLayout verifies that ScaffoldWatcherLayout creates
// watcher/{CLAUDE.md, POLICY.md, LEARNINGS.md, clients.json} under the effective
// XDG data dir when the directory does not yet exist.
func TestLayout_FreshInstallCreatesLayout(t *testing.T) {
	isolateWatcherLayoutHome(t)

	if err := ScaffoldWatcherLayout(); err != nil {
		t.Fatalf("ScaffoldWatcherLayout: %v", err)
	}

	deck := effectiveAgentDeckDir(t)
	for _, name := range []string{"CLAUDE.md", "POLICY.md", "LEARNINGS.md", "clients.json"} {
		path := filepath.Join(deck, "watcher", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected %s to exist, got: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("expected %s to be non-empty", name)
		}
	}
}

// TestLayout_LegacyMigrationAtomic covers the one-shot watchers -> watcher rename plus
// relative symlink creation, the collision path, the symlink-traversal refusal (T-21-SL),
// and idempotent re-run.
func TestLayout_LegacyMigrationAtomic(t *testing.T) {
	t.Run("happy_path", func(t *testing.T) {
		isolateWatcherLayoutHome(t)
		buf := captureLog(t)

		deck := legacyAgentDeckDir(t)
		// Seed legacy directory with sub-dir + file.
		legacyDir := filepath.Join(deck, "watchers", "alpha")
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatalf("MkdirAll legacy: %v", err)
		}
		content := []byte(`{"name":"alpha"}`)
		if err := os.WriteFile(filepath.Join(legacyDir, "meta.json"), content, 0o644); err != nil {
			t.Fatalf("WriteFile meta.json: %v", err)
		}
		// Also seed clients.json in old location.
		if err := os.WriteFile(filepath.Join(deck, "watchers", "clients.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile clients.json: %v", err)
		}

		if err := MigrateLegacyWatchersDir(); err != nil {
			t.Fatalf("MigrateLegacyWatchersDir: %v", err)
		}

		// (a) alpha/meta.json moved.
		movedPath := filepath.Join(deck, "watcher", "alpha", "meta.json")
		got, err := os.ReadFile(movedPath)
		if err != nil {
			t.Fatalf("alpha/meta.json should exist at new location: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("meta.json contents changed: want %q, got %q", content, got)
		}

		// (b) ~/.agent-deck/watchers is now a symlink.
		watchersPath := filepath.Join(deck, "watchers")
		fi, err := os.Lstat(watchersPath)
		if err != nil {
			t.Fatalf("Lstat watchers: %v", err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected watchers to be a symlink, got mode %v", fi.Mode())
		}

		// (c) readlink returns "watcher" (relative).
		target, err := os.Readlink(watchersPath)
		if err != nil {
			t.Fatalf("Readlink: %v", err)
		}
		if target != "watcher" {
			t.Errorf("symlink target: want %q, got %q", "watcher", target)
		}

		// (d) log contains migration prefix.
		logStr := buf.String()
		if !strings.Contains(logStr, "watcher: migrated legacy") {
			t.Errorf("log should contain 'watcher: migrated legacy', got:\n%s", logStr)
		}

		// (e) log explicitly calls out issue-watcher NOT migrated.
		if !strings.Contains(logStr, "issue-watcher/ NOT migrated") {
			t.Errorf("log should mention 'issue-watcher/ NOT migrated', got:\n%s", logStr)
		}
	})

	t.Run("collision", func(t *testing.T) {
		isolateWatcherLayoutHome(t)
		buf := captureLog(t)

		deck := legacyAgentDeckDir(t)
		// Seed both as real directories.
		if err := os.MkdirAll(filepath.Join(deck, "watchers"), 0o755); err != nil {
			t.Fatalf("MkdirAll watchers: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(deck, "watcher"), 0o755); err != nil {
			t.Fatalf("MkdirAll watcher: %v", err)
		}

		if err := MigrateLegacyWatchersDir(); err != nil {
			t.Fatalf("MigrateLegacyWatchersDir collision: %v", err)
		}

		// Neither should be renamed.
		watchersInfo, err := os.Lstat(filepath.Join(deck, "watchers"))
		if err != nil {
			t.Fatalf("Lstat watchers: %v", err)
		}
		if watchersInfo.Mode()&os.ModeSymlink != 0 {
			t.Error("watchers should remain a real dir (not symlink) after collision path")
		}

		logStr := buf.String()
		if !strings.Contains(logStr, "both ~/.agent-deck/watchers/ and ~/.agent-deck/watcher/ exist") {
			t.Errorf("log should mention collision, got:\n%s", logStr)
		}
	})

	t.Run("symlink_attack", func(t *testing.T) {
		isolateWatcherLayoutHome(t)

		deck := legacyAgentDeckDir(t)
		if err := os.MkdirAll(deck, 0o755); err != nil {
			t.Fatalf("MkdirAll deck: %v", err)
		}
		// Pre-create watcher/ as a symlink pointing outside the deck dir (T-21-SL).
		outside := t.TempDir()
		watcherPath := filepath.Join(deck, "watcher")
		if err := os.Symlink(outside, watcherPath); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		err := MigrateLegacyWatchersDir()
		if err == nil {
			t.Fatal("expected error for symlink traversal attack, got nil")
		}
		if !strings.Contains(err.Error(), "refusing migration") {
			t.Errorf("error should contain 'refusing migration', got: %v", err)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		isolateWatcherLayoutHome(t)

		deck := legacyAgentDeckDir(t)
		legacyDir := filepath.Join(deck, "watchers")
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatalf("MkdirAll legacy: %v", err)
		}

		// First call — migrates.
		if err := MigrateLegacyWatchersDir(); err != nil {
			t.Fatalf("first MigrateLegacyWatchersDir: %v", err)
		}

		// Second call — should be a no-op with no error.
		if err := MigrateLegacyWatchersDir(); err != nil {
			t.Fatalf("second MigrateLegacyWatchersDir (idempotent): %v", err)
		}
	})
}

// TestLayout_SymlinkResolves verifies that after migration, the compatibility symlink
// allows reads through watchers/clients.json that resolve to watcher/clients.json.
func TestLayout_SymlinkResolves(t *testing.T) {
	isolateWatcherLayoutHome(t)

	deck := legacyAgentDeckDir(t)
	// Seed clients.json in the legacy location.
	legacyDir := filepath.Join(deck, "watchers")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll legacy: %v", err)
	}
	clientsContent := []byte(`{"key":"value"}`)
	if err := os.WriteFile(filepath.Join(legacyDir, "clients.json"), clientsContent, 0o644); err != nil {
		t.Fatalf("WriteFile clients.json: %v", err)
	}

	if err := MigrateLegacyWatchersDir(); err != nil {
		t.Fatalf("MigrateLegacyWatchersDir: %v", err)
	}

	// Both paths should resolve to the same content (symlink traversal).
	viaNew, err := os.ReadFile(filepath.Join(deck, "watcher", "clients.json"))
	if err != nil {
		t.Fatalf("ReadFile via watcher/: %v", err)
	}
	viaOld, err := os.ReadFile(filepath.Join(deck, "watchers", "clients.json"))
	if err != nil {
		t.Fatalf("ReadFile via watchers/ (symlink): %v", err)
	}
	if !bytes.Equal(viaNew, viaOld) {
		t.Errorf("content mismatch: watcher/clients.json=%q, watchers/clients.json=%q", viaNew, viaOld)
	}
}

// TestLayout_StateRoundtrip verifies SaveState/LoadState round-trip all WatcherState fields,
// and that LoadState returns (nil, nil) when state.json is absent.
func TestLayout_StateRoundtrip(t *testing.T) {
	isolateWatcherLayoutHome(t)

	t0 := time.Now().UTC().Truncate(time.Second)
	original := &WatcherState{
		LastEventTS:    t0,
		ErrorCount:     2,
		AdapterHealthy: true,
		HealthWindow: []HealthSample{
			{TS: t0, OK: true},
		},
		DedupCursor: "abc",
	}

	if err := SaveState("alpha", original); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState("alpha")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil, want non-nil")
	}
	if !loaded.LastEventTS.Equal(t0) {
		t.Errorf("LastEventTS: want %v, got %v", t0, loaded.LastEventTS)
	}
	if loaded.ErrorCount != 2 {
		t.Errorf("ErrorCount: want 2, got %d", loaded.ErrorCount)
	}
	if !loaded.AdapterHealthy {
		t.Errorf("AdapterHealthy: want true, got false")
	}
	if loaded.DedupCursor != "abc" {
		t.Errorf("DedupCursor: want %q, got %q", "abc", loaded.DedupCursor)
	}

	// Missing state.json => (nil, nil).
	missing, err := LoadState("missing-watcher")
	if err != nil {
		t.Fatalf("LoadState missing: expected nil error, got: %v", err)
	}
	if missing != nil {
		t.Errorf("LoadState missing: expected nil state, got: %+v", missing)
	}
}

// TestLayout_EventLogAppendAtomic verifies AppendEventLog writes complete lines,
// and that concurrent appends do not produce torn lines.
func TestLayout_EventLogAppendAtomic(t *testing.T) {
	isolateWatcherLayoutHome(t)

	entries := []string{
		"## 2026-04-16T12:00:00Z - webhook: evt1",
		"## 2026-04-16T12:00:01Z - webhook: evt2",
		"## 2026-04-16T12:00:02Z - webhook: evt3",
	}
	for _, entry := range entries {
		if err := AppendEventLog("alpha", entry); err != nil {
			t.Fatalf("AppendEventLog %q: %v", entry, err)
		}
	}

	deck := effectiveAgentDeckDir(t)
	logPath := filepath.Join(deck, "watcher", "alpha", "task-log.md")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile task-log.md: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d:\n%s", len(lines), string(data))
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") {
			t.Errorf("line %d should start with '## ', got: %q", i, line)
		}
		if !strings.Contains(line, entries[i][3:]) { // strip "## " prefix for contains check
			t.Errorf("line %d should contain entry text, got: %q", i, line)
		}
		if len(line) >= 512 {
			t.Errorf("line %d is >= 512 bytes: %d", i, len(line))
		}
	}

	// Concurrency sub-test: 2 goroutines × 50 appends = 100 total lines, no torn lines.
	t.Run("concurrent", func(t *testing.T) {
		isolateWatcherLayoutHome(t)

		lineRe := regexp.MustCompile(`^## .+$`)
		var wg sync.WaitGroup
		const goroutines = 2
		const perGoroutine = 50
		for g := range goroutines {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for i := range perGoroutine {
					entry := strings.Repeat(
						"x",
						10,
					)
					_ = AppendEventLog("beta", "## 2026-04-16T00:00:00Z - g"+
						string(rune('0'+id))+"i"+string(rune('0'+i%10))+
						": "+entry)
				}
			}(g)
		}
		wg.Wait()

		deck2 := effectiveAgentDeckDir(t)
		logPath2 := filepath.Join(deck2, "watcher", "beta", "task-log.md")
		data2, err := os.ReadFile(logPath2)
		if err != nil {
			t.Fatalf("ReadFile concurrent task-log.md: %v", err)
		}
		scanner2 := bufio.NewScanner(bytes.NewReader(data2))
		total := 0
		for scanner2.Scan() {
			line := scanner2.Text()
			total++
			if !lineRe.MatchString(line) {
				t.Errorf("torn line detected: %q", line)
			}
		}
		if total != goroutines*perGoroutine {
			t.Errorf("expected %d lines, got %d", goroutines*perGoroutine, total)
		}
	})
}

// TestLayout_HotReloadSafe verifies LoadState always reads from disk (no in-process cache).
func TestLayout_HotReloadSafe(t *testing.T) {
	isolateWatcherLayoutHome(t)

	t0 := time.Now().UTC().Truncate(time.Second)
	stateV1 := &WatcherState{LastEventTS: t0, ErrorCount: 1, AdapterHealthy: true}
	if err := SaveState("alpha", stateV1); err != nil {
		t.Fatalf("SaveState v1: %v", err)
	}

	// Overwrite state.json externally (simulating external write).
	t1 := t0.Add(10 * time.Second)
	stateV2 := &WatcherState{LastEventTS: t1, ErrorCount: 5, AdapterHealthy: false}
	if err := SaveState("alpha", stateV2); err != nil {
		t.Fatalf("SaveState v2: %v", err)
	}

	loaded, err := LoadState("alpha")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil")
	}
	if !loaded.LastEventTS.Equal(t1) {
		t.Errorf("hot-reload: expected LastEventTS=%v (v2), got %v", t1, loaded.LastEventTS)
	}
	if loaded.ErrorCount != 5 {
		t.Errorf("hot-reload: expected ErrorCount=5 (v2), got %d", loaded.ErrorCount)
	}
}

// TestLayout_Integration_ThreeEvents simulates writerLoop calling AppendEventLog + SaveState
// three times and checks that task-log.md has 3 lines and LastEventTS equals the third event's ts.
func TestLayout_Integration_ThreeEvents(t *testing.T) {
	isolateWatcherLayoutHome(t)

	// Seed meta.json for "alpha".
	deck := effectiveAgentDeckDir(t)
	alphaDir := filepath.Join(deck, "watcher", "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll alpha: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "meta.json"), []byte(`{"name":"alpha"}`), 0o644); err != nil {
		t.Fatalf("WriteFile meta.json: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	var lastTS time.Time
	for i := range 3 {
		ts := base.Add(time.Duration(i) * time.Second)
		lastTS = ts
		entry := "## " + ts.Format(time.RFC3339) + " - webhook: event" + string(rune('0'+i+1))
		if err := AppendEventLog("alpha", entry); err != nil {
			t.Fatalf("AppendEventLog[%d]: %v", i, err)
		}
		state := &WatcherState{LastEventTS: ts, ErrorCount: 0, AdapterHealthy: true}
		if err := SaveState("alpha", state); err != nil {
			t.Fatalf("SaveState[%d]: %v", i, err)
		}
	}

	// Verify task-log.md has 3 lines.
	data, err := os.ReadFile(filepath.Join(alphaDir, "task-log.md"))
	if err != nil {
		t.Fatalf("ReadFile task-log.md: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 3 {
		t.Errorf("expected 3 lines in task-log.md, got %d:\n%s", len(lines), string(data))
	}

	// Verify state.json.LastEventTS equals the third event's timestamp.
	loaded, err := LoadState("alpha")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil")
	}
	if !loaded.LastEventTS.Equal(lastTS) {
		t.Errorf("LastEventTS: want %v, got %v", lastTS, loaded.LastEventTS)
	}
}

// TestLayout_WatcherDir_RejectsMaliciousNames verifies T-21-PI: path traversal names are rejected.
func TestLayout_WatcherDir_RejectsMaliciousNames(t *testing.T) {
	isolateWatcherLayoutHome(t)

	cases := []struct {
		name    string
		wantMsg string
	}{
		{"../etc", "invalid watcher name"},
		{"a/b", "invalid watcher name"},
		{".hidden", "invalid watcher name"},
		{"", "cannot be empty"},
	}
	for _, c := range cases {
		_, err := WatcherDir(c.name)
		if err == nil {
			t.Errorf("WatcherDir(%q): expected error, got nil", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("WatcherDir(%q): want error containing %q, got: %v", c.name, c.wantMsg, err)
		}
	}
}
