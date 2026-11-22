package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two installs started at the same moment (a nudge and a web click one second
// apart during the v1.16.40 rollout) must not fail on a missing .old file or
// leave the binary missing or half written.
func TestInstallSelfUpdateBinary_ConcurrentInstallsAreSafe(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Watch the target: it must exist and be whole at every instant.
	var missing atomic.Int32
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := os.Stat(execPath); err != nil {
				missing.Add(1)
			}
		}
	}()

	const n = 8
	payload := func(i int) []byte { return []byte(fmt.Sprintf("new-binary-%d-%02000d", i, i)) }
	errs := make([]error, n)
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = installSelfUpdateBinary(execPath, payload(i))
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	time.Sleep(10 * time.Millisecond)
	close(stop)
	<-watched

	for i, err := range errs {
		if err != nil {
			t.Errorf("install %d failed: %v", i, err)
		}
	}
	if m := missing.Load(); m > 0 {
		t.Errorf("binary was missing %d times during the replace", m)
	}
	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("binary missing after installs: %v", err)
	}
	ok := false
	for i := 0; i < n; i++ {
		if string(got) == string(payload(i)) {
			ok = true
		}
	}
	if !ok {
		t.Errorf("installed binary is partial or foreign (%d bytes)", len(got))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "agent-deck" && e.Name() != "agent-deck.lock" {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
}

// A second installer that cannot get the lock reports a clean, typed error
// and leaves the binary alone.
func TestInstallSelfUpdateBinary_LockHeldReportsInProgress(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireInstallLock(execPath)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	orig := installLockWait
	installLockWait = 100 * time.Millisecond
	t.Cleanup(func() { installLockWait = orig })

	err = installSelfUpdateBinary(execPath, []byte("new"))
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("got %v, want ErrUpdateInProgress", err)
	}
	if got, _ := os.ReadFile(execPath); string(got) != "old" {
		t.Errorf("binary must be untouched, got %q", got)
	}
}
