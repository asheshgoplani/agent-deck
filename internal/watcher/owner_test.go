package watcher

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// TestEngineOwner_SecondOwnerWaitsForTheFirst covers #2530: two engine owners
// on the same runtime dir. The second fails with the already running error,
// naming the owner's pid, and acquires once the first is closed.
func TestEngineOwner_SecondOwnerWaitsForTheFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", EngineLockFile)
	first, err := AcquireEngineOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireEngineOwner(path)
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("second AcquireEngineOwner = %v, want AlreadyRunningError{pid %d}", err, os.Getpid())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireEngineOwner(path)
	if err != nil {
		t.Fatalf("AcquireEngineOwner after the first owner closed: %v", err)
	}
	_ = again.Close()

	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir = %v, %v; want mode 0700", st, err)
	}
}

// TestEngineOwner_LockDiesWithItsProcess: an owner killed with SIGKILL runs no
// cleanup, and the next process still acquires at once, because the flock is
// released with the process.
func TestEngineOwner_LockDiesWithItsProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", EngineLockFile)
	cmd := exec.Command(os.Args[0], "-test.run=^TestEngineOwner_HelperHoldsTheLock$")
	cmd.Env = append(os.Environ(), "WATCHER_ENGINE_LOCK_HELPER="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper did not take the lock: %q, %v", line, err)
	}

	_, err = AcquireEngineOwner(path)
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != cmd.Process.Pid {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("AcquireEngineOwner while the helper holds the lock = %v, want AlreadyRunningError{pid %d}", err, cmd.Process.Pid)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	owner, err := AcquireEngineOwner(path)
	if err != nil {
		t.Fatalf("AcquireEngineOwner after the owner was killed: %v", err)
	}
	_ = owner.Close()
}

// TestEngineOwner_HelperHoldsTheLock is the child process of
// TestEngineOwner_LockDiesWithItsProcess; it does nothing on its own.
func TestEngineOwner_HelperHoldsTheLock(t *testing.T) {
	path := os.Getenv("WATCHER_ENGINE_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process for TestEngineOwner_LockDiesWithItsProcess")
	}
	if _, err := AcquireEngineOwner(path); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute) // held until the parent kills this process
}

// TestEngineLockPath_IsInTheProfileRuntimeDir: the lock sits next to the
// profile daemon's daemon.lock.
func TestEngineLockPath_IsInTheProfileRuntimeDir(t *testing.T) {
	got, err := EngineLockPath("work")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := agentpaths.ProfileRuntimeDir("work")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "watcher-engine.lock"); got != want {
		t.Fatalf("EngineLockPath(work) = %q, want %q", got, want)
	}
	if _, err := EngineLockPath("../work"); err == nil {
		t.Fatal("EngineLockPath accepted a profile that is not one path segment")
	}
}
