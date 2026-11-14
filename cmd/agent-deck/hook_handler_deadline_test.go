package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The pi extension runs hook-handler detached, with a timer in pi that kills it.
// If pi dies, that timer dies with it, so hook-handler bounds itself when the
// extension passes AGENTDECK_HOOK_DEADLINE_MS: a watchdog from entry, and a
// status lock wait that gives up at the same deadline. Without the variable,
// nothing changes (the Claude path).

type hookRun struct {
	err     error
	elapsed time.Duration
}

// runHookHandlerBinary runs the built binary on one pi status event. stdin is
// the payload, then closed, unless keepStdinOpen.
func runHookHandlerBinary(t *testing.T, instance string, extraEnv []string, keepStdinOpen bool) hookRun {
	t.Helper()
	bin := agentDeckBinaryAs(t, "agent-deck")
	cmd := exec.Command(bin, "hook-handler")
	cmd.Env = append(os.Environ(), "AGENTDECK_INSTANCE_ID="+instance, "CLAUDE_JOB_DIR=")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdin io.WriteCloser
	var err error
	if stdin, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if !keepStdinOpen {
		_, _ = stdin.Write([]byte(`{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp"}`))
		_ = stdin.Close()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return hookRun{err: err, elapsed: time.Since(start)}
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		return hookRun{err: errors.New("still running after 8 s"), elapsed: 8 * time.Second}
	}
}

func holdStatusLock(t *testing.T, instance string) *os.File {
	t.Helper()
	dir := getHooksDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, instance+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func hookEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "_test")
	// A test run from inside a session that set a hook generation would have
	// every status write rejected: the fresh home has no generation file.
	t.Setenv("AGENTDECK_HOOK_GENERATION", "")
}

func statusWritten(instance string) bool {
	_, err := os.Stat(filepath.Join(getHooksDir(), instance+".json"))
	return err == nil
}

func TestHookHandler_DeadlineBoundsAStatusLockWait(t *testing.T) {
	hookEnv(t)
	const instance = "pi-deadline-lock"
	holdStatusLock(t, instance)
	run := runHookHandlerBinary(t, instance, []string{"AGENTDECK_HOOK_DEADLINE_MS=600"}, false)
	var exit *exec.ExitError
	if !errors.As(run.err, &exit) {
		t.Fatalf("hook-handler blocked on a held lock ended with %v after %v, want a non-zero exit at its deadline", run.err, run.elapsed)
	}
	if run.elapsed < 500*time.Millisecond || run.elapsed > 2500*time.Millisecond {
		t.Errorf("hook-handler gave up after %v, want about its 600 ms deadline", run.elapsed)
	}
	if statusWritten(instance) {
		t.Error("hook-handler published a status although it never held the lock")
	}
}

func TestHookHandler_WatchdogBoundsAStdinThatNeverCloses(t *testing.T) {
	hookEnv(t)
	run := runHookHandlerBinary(t, "pi-deadline-stdin", []string{"AGENTDECK_HOOK_DEADLINE_MS=600"}, true)
	var exit *exec.ExitError
	if !errors.As(run.err, &exit) {
		t.Fatalf("hook-handler with a silent stdin ended with %v after %v, want a non-zero exit at its deadline", run.err, run.elapsed)
	}
	if run.elapsed < 500*time.Millisecond || run.elapsed > 2500*time.Millisecond {
		t.Errorf("hook-handler gave up after %v, want about its 600 ms deadline", run.elapsed)
	}
}

func TestHookHandler_DeadlineDoesNotFireOnAHandlerThatFinishes(t *testing.T) {
	hookEnv(t)
	const instance = "pi-deadline-fast"
	run := runHookHandlerBinary(t, instance, []string{"AGENTDECK_HOOK_DEADLINE_MS=5000"}, false)
	if run.err != nil || run.elapsed > 3*time.Second {
		t.Fatalf("an uncontended hook-handler ended with %v after %v, want a clean exit well inside its deadline", run.err, run.elapsed)
	}
	if !statusWritten(instance) {
		t.Error("the status was not published: the control event does not exercise the handler")
	}
}

// Without the variable the lock wait is unbounded exactly as before: the handler
// waits for the holder, then publishes.
func TestHookHandler_WithoutADeadlineTheLockStillBlocksUntilReleased(t *testing.T) {
	hookEnv(t)
	const instance = "pi-nodeadline-lock"
	// Build the binary before the release timer starts: a cold build inside
	// runHookHandlerBinary would use up the 900 ms hold.
	channelsCLIBinary(t)
	lock := holdStatusLock(t, instance)
	// The release runs in a timer goroutine that reads lock.Fd(): join it before
	// the cleanup closes the file.
	released := make(chan struct{})
	time.AfterFunc(900*time.Millisecond, func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		close(released)
	})
	t.Cleanup(func() { <-released })
	run := runHookHandlerBinary(t, instance, nil, false)
	if run.err != nil {
		t.Fatalf("hook-handler without a deadline ended with %v, want it to wait for the lock and succeed", run.err)
	}
	if run.elapsed < 800*time.Millisecond {
		t.Errorf("hook-handler finished after %v while the lock was held for 900 ms", run.elapsed)
	}
	if !statusWritten(instance) {
		t.Error("the status was not published after the lock was released")
	}
}

// hook-handler's pi paths start no child processes, so a self-timeout (os.Exit)
// has no descendants to orphan and needs no process-group kill. Read from the
// code: the pi status events (session_start, turn_start, turn_end,
// agent_settled, session_shutdown) go through spoolCommsFromHook, the status
// file write under the flock, applyClaudeTitleSync/applyClaudeCwdSync (a
// storage read, and only a rename when Claude names the session, which pi does
// not) and writeCostEvent: none of them has an exec.Command, os.StartProcess or
// tmux call on these events (hook_handler.go, hook_events.go, hook_name_sync.go,
// hook_cwd_sync.go, hook_recall.go). The test below is the dynamic check: every
// tool a spawn could resolve on PATH is a recording shim, and none may run. It
// cannot see a spawn by absolute path; extend it with before_agent_start once
// that event has a hook-handler branch.
func TestHookHandler_PiStatusPathsSpawnNoChildProcesses(t *testing.T) {
	hookEnv(t)
	shims := t.TempDir()
	log := filepath.Join(t.TempDir(), "spawned.log")
	for _, name := range []string{"tmux", "git", "ps", "pgrep", "sh", "bash", "agent-deck", "node", "sleep", "kill", "env", "ssh"} {
		body := "#!/bin/sh\necho " + name + " \"$@\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(shims, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := agentDeckBinaryAs(t, "agent-deck")
	for _, payload := range []string{
		`{"hook_event_name":"session_start","source":"pi","cwd":"/tmp"}`,
		`{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp"}`,
		`{"hook_event_name":"turn_end","source":"pi","cwd":"/tmp"}`,
		`{"hook_event_name":"agent_settled","source":"pi","cwd":"/tmp","session_id":"pi-sess","turn_id":"t1","last_assistant_message":"done","prompt":"p"}`,
		`{"hook_event_name":"session_shutdown","source":"pi","cwd":"/tmp"}`,
	} {
		cmd := exec.Command(bin, "hook-handler")
		cmd.Env = append(os.Environ(), "AGENTDECK_INSTANCE_ID=pi-nospawn", "CLAUDE_JOB_DIR=", "PATH="+shims, "AGENTDECK_HOOK_DEADLINE_MS=5000")
		cmd.Stdin = strings.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook-handler on %s: %v\n%s", payload, err, out)
		}
	}
	if !statusWritten("pi-nospawn") {
		t.Fatal("no status was written: the events do not exercise the handler")
	}
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Errorf("hook-handler started child processes on pi events:\n%s", data)
	}
}
