package watcher

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppendEventLog appends one Markdown line to <name>/task-log.md.
// Expected entry format: "## <RFC3339-ts> - <event_type>: <summary>" (no trailing newline — added here).
// Atomicity: O_APPEND on a single Write is POSIX-atomic for sizes <= PIPE_BUF (4096 on Linux, 512 on older BSD).
// The engine writerLoop is single-goroutine per engine instance so per-watcher serialization is guaranteed today;
// entries are truncated to <512 bytes by the caller (engine.go) so atomicity holds even if writerLoop becomes multi-goroutine.
func AppendEventLog(name, entry string) (err error) {
	dir, err := WatcherDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create watcher dir: %w", err)
	}
	path := filepath.Join(dir, "task-log.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open task-log.md: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	_, err = f.WriteString(entry + "\n")
	return err
}
