package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2481 item 7: the TUI's log maintenance shares the logs directory with
// agent-deck's own logs. It deleted transition-notifier.log as an "orphaned
// session log" whenever the file had not been written for an hour (the log was
// lost on 4 of 5 hosts) and would truncate it past max_size_mb. Only per-session
// logs (<SessionPrefix>*.log) are maintenance candidates.
func TestIssue2481_LogMaintenanceSparesAgentDeckLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")

	dir := LogDir()
	if !strings.HasPrefix(dir, home) {
		t.Fatalf("LogDir %q escaped the test HOME %q", dir, home)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	write := func(name string, size int) string {
		path := filepath.Join(dir, name)
		line := strings.Repeat("y", 99) + "\n"
		if err := os.WriteFile(path, []byte(strings.Repeat(line, size/len(line)+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}

	ownLogs := []string{"transition-notifier.log", "notifier-missed.log", "notifier-orphans.log", "notifier-probe-stalls.log"}

	t.Run("orphan cleanup", func(t *testing.T) {
		for _, name := range ownLogs {
			write(name, 4096)
		}
		orphan := write(SessionPrefix+"gone_deadbeef.log", 100)
		if _, _, err := CleanupOrphanedLogs(); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
		for _, name := range ownLogs {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s was removed as an orphaned session log: %v", name, err)
			}
		}
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphaned session log kept (err=%v): cleanup did not run", err)
		}
	})

	t.Run("size truncation", func(t *testing.T) {
		sizes := map[string]int64{}
		for _, name := range ownLogs {
			info, err := os.Stat(write(name, 2<<20))
			if err != nil {
				t.Fatal(err)
			}
			sizes[name] = info.Size()
		}
		big := write(SessionPrefix+"big_cafef00d.log", 2<<20)
		if _, err := TruncateLargeLogFiles(1, 10); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		for _, name := range ownLogs {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Errorf("%s was removed by log maintenance: %v", name, err)
			} else if info.Size() != sizes[name] {
				t.Errorf("%s was truncated by log maintenance: %d -> %d bytes", name, sizes[name], info.Size())
			}
		}
		if info, err := os.Stat(big); err != nil || info.Size() >= 2<<20 {
			t.Fatalf("oversized session log not truncated (err=%v)", err)
		}
	})
}
