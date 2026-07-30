package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestWriteHookStateAtSerializesConcurrentWriters(t *testing.T) {
	t.Parallel()

	hooksDir := filepath.Join(t.TempDir(), "hooks")
	const writers = 24
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for idx := 0; idx < writers; idx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- writeHookStateAt(hooksDir, "instance-1", HookStateEvent{
				Kind: HookStatusOnly, Status: "running", SessionID: "thread-1", Event: "status",
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent writer: %v", err)
		}
	}

	if err := writeHookStateAt(hooksDir, "instance-1", HookStateEvent{
		Kind: HookTurnStarted, Status: "running", SessionID: "thread-1", Event: "turn.started",
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := writeHookStateAt(hooksDir, "instance-1", HookStateEvent{
		Kind: HookTurnCompleted, Status: "waiting", SessionID: "thread-1", Event: "turn.completed",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	state, err := readHookStateAt(filepath.Join(hooksDir, "instance-1.json"))
	if err != nil {
		t.Fatalf("readHookStateAt: %v", err)
	}
	if state.Generation != writers+2 {
		t.Fatalf("generation = %d, want %d", state.Generation, writers+2)
	}
	if state.LastTurnStartedGeneration != writers+1 || state.LastTurnCompletedGeneration != writers+2 {
		t.Fatalf("start/completion generations not retained: %#v", state)
	}

	info, err := os.Stat(filepath.Join(hooksDir, "instance-1.json"))
	if err != nil {
		t.Fatalf("stat hook state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("hook state mode = %o, want 600", got)
	}
	temps, err := filepath.Glob(filepath.Join(hooksDir, ".instance-1.*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(temps) != 0 {
		t.Fatalf("atomic temp files left behind: %v", temps)
	}
}

func TestWriteHookStateAtRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooksDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(hooksDir, "instance-1.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := writeHookStateAt(hooksDir, "instance-1", HookStateEvent{
		Kind: HookTurnStarted, Status: "running", SessionID: "thread-1", Event: "turn.started",
	})
	if err == nil || !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("symlink write error = %v, want ELOOP", err)
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(data) != "do not touch" {
		t.Fatalf("symlink target changed: %q", data)
	}
}
