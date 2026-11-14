package ask

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestLockDirWaitsForAnotherHolder pins that the read-only lock handle still
// excludes another holder of ask/.lock (another process), and that the lock
// file is created 0600.
func TestLockDirWaitsForAnotherHolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	other, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		unlock, err := lockDir(dir)
		if err == nil {
			unlock()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("lockDir returned (%v) while another handle held the lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("lockDir after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lockDir did not take the lock after the other holder released it")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("lock file mode %o, want 600", perm)
	}
}
