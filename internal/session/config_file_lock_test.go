package session

import (
	"os"
	"testing"
	"time"
)

// Deterministic companion to TestWriteProjectMCP_ConcurrentWritesKeepEveryEntry
// (issue #1956).
//
// The lock itself now comes from main (#1957). This pins the property that
// matters and that the end state cannot show: WriteProjectMCP and
// WriteGlobalMCP write the SAME .claude.json, so they must contend on the SAME
// lock. A per-function or per-profile lock would let a global attach and a
// project attach run concurrently over one file and still lose each other's
// work, and every assertion about the final file would still pass.

// TestClaudeConfigWriters_ShareOneLock is the deterministic half of the
// concurrency proof: rather than hoping a race fires, it holds the lock and
// checks the writer actually waits.
//
// It also pins the scope of that lock. WriteProjectMCP and WriteGlobalMCP write
// the SAME file, so a per-function lock would let a global attach and a project
// attach still lose each other's work — the lock has to be keyed by the file.
func TestClaudeConfigWriters_ShareOneLock(t *testing.T) {
	configFile := claudeConfigSandbox(t)
	if err := os.WriteFile(configFile, []byte(seededClaudeConfig), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	lock, err := AcquireConfigFileLock(configFile)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- WriteGlobalMCP([]string{"ctx7"}) }()

	select {
	case err := <-done:
		lock.Release()
		t.Fatalf("WriteGlobalMCP completed (err=%v) while the Claude config lock was held: "+
			"the read-modify-write is not serialized against other writers of the same file", err)
	case <-time.After(150 * time.Millisecond):
		// Still blocked, which is the point.
	}

	lock.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WriteGlobalMCP after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WriteGlobalMCP did not complete after the lock was released")
	}
}
