package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRemotePollTransportInheritedPipes(t *testing.T) {
	dir := t.TempDir()
	// A surviving child models ControlPersist retaining the command's pipes.
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey)' >&2\nsleep 2 &\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	// Allow the shell to start under race instrumentation. This test bounds
	// pipe draining after exit; the latency test covers a short command deadline.
	r := &SSHRunner{Host: "example.invalid", commandTimeout: 3 * time.Second}
	started := time.Now()
	_, err := r.Run(context.Background(), "list")
	if time.Since(started) > time.Second {
		t.Errorf("inherited pipes delayed return: %v", time.Since(started))
	}
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Fatalf("wanted captured SSH error, got %v", err)
	}
}

func TestRemotePollTransportCloseDoesNotWait(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ch := newRemoteChannel("test", "test", nil)
	var terminated atomic.Bool
	ch.closeFn = func() { terminated.Store(true); go func() { <-release }() }
	ch.up.Store(true)
	done := make(chan struct{})
	go func() { ch.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close waited for transport cleanup")
	}
	if !terminated.Load() {
		t.Fatal("Close returned before signaling transport termination")
	}
	if ch.Connected() {
		t.Fatal("closed channel remains connected")
	}
}

func TestRemotePollTransportCloseCancelsDial(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ch := newRemoteChannel("test", "test", func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-release:
		}
		return nil, nil, nil, context.Canceled
	})
	go ch.ensureConnected()
	<-entered
	ch.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel dial")
	}
}

func TestRemotePollTransportBlockedWriteHonorsDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	ch := newRemoteChannel("test", "test", nil)
	ch.stdin = writer
	ch.closeFn = func() { writer.Close() }
	ch.up.Store(true)
	defer ch.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ch.Request(ctx, []string{"list"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored deadline")
	}
}

func TestRemotePollTransportLatencyDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey)' >&2\nsleep 2 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (&SSHRunner{Host: "example.invalid"}).MeasureLatency(ctx)
	if time.Since(started) > time.Second {
		t.Errorf("latency probe exceeded bound: %v", time.Since(started))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted deadline error, got %v", err)
	}
}

func TestRemotePollTransportDoesNotPrintDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho unexpected-output\necho 'Permission denied (publickey)' >&2\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	stdout, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(dir, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	oldOut, oldErr, oldLog := os.Stdout, os.Stderr, sessionLog
	logs := transportLogBuffer{written: make(chan struct{}, 8)}
	sessionLog = slog.New(slog.NewTextHandler(&logs, nil))
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr, sessionLog = oldOut, oldErr, oldLog }()
	cleaned := false
	r := &SSHRunner{Host: "example.invalid", cleanChannelSocketsFn: func() { cleaned = true; cleanStaleSSHSocketsIn(dir) }}
	for _, run := range []func() error{
		func() error { _, err := r.Run(context.Background(), "list"); return err },
		func() error { _, err := r.MeasureLatency(context.Background()); return err },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
			t.Fatalf("missing captured diagnostic: %v", err)
		}
	}

	// Persistent channel diagnostics follow the same logging-only path.
	_, output, closeChannel, err := r.dialRemoteAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(output)
	closeChannel()
	if !cleaned {
		t.Fatal("dial bypassed isolated socket cleanup")
	}
	for i := 0; i < 3; i++ {
		select {
		case <-logs.written:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for captured diagnostics")
		}
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, f := range []*os.File{stdout, stderr} {
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 0 {
			t.Errorf("terminal %s received %q", f.Name(), data)
		}
	}
	if strings.Count(logs.String(), "Permission denied (publickey)") != 3 {
		t.Fatalf("default-level logs lost diagnostics: %s", logs.String())
	}
}

// A synchronized sink lets the asynchronous reaper finish logging before the
// test restores globals or inspects the captured diagnostics.
type transportLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	written chan struct{}
}

func (b *transportLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.Buffer.Write(p)
	b.written <- struct{}{}
	return n, err
}
func (b *transportLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestRemotePollTransportExitingProcessTerminatesSSH(t *testing.T) {
	if os.Getenv("AGENTDECK_EXIT_TRANSPORT_HELPER") == "1" {
		runtime.GOMAXPROCS(1)
		r := &SSHRunner{Host: "example.invalid", cleanChannelSocketsFn: func() {}}
		stdin, _, closeTransport, err := r.dialRemoteAgent(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// EOF alone cannot terminate this fake SSH. Wait for it to acknowledge
		// EOF before testing shutdown, eliminating descriptor-close as an escape.
		_ = stdin.Close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("AGENTDECK_EOF_FILE")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("fake SSH did not acknowledge EOF")
			}
			time.Sleep(time.Millisecond)
		}
		ch := newRemoteChannel("exit-test", "exit-test", nil)
		if os.Getenv("AGENTDECK_PENDING_HELLO") != "1" {
			ch.closeFn = closeTransport
		}
		ch.up.Store(true)
		remoteChannels["exit-test"] = ch
		CloseRemoteChannels()
		// No scheduler yield: a deferred goroutine cannot be relied upon at exit.
		os.Exit(0)
	}
	for _, phase := range []string{"connected", "pending-hello"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			// Model another client's shared master with an independently owned
			// process. Channel shutdown must not kill unrelated SSH processes.
			master := exec.Command("sleep", "60")
			if err := master.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Process.Kill(); _ = master.Wait() }()
			pidFile, eofFile := filepath.Join(dir, "pid"), filepath.Join(dir, "eof")
			fake := "#!/bin/sh\necho $$ > \"$AGENTDECK_PID_FILE\"\ncat >/dev/null\ntouch \"$AGENTDECK_EOF_FILE\"\nexec sleep 60\n"
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRemotePollTransportExitingProcessTerminatesSSH$")
			cmd.Env = append(os.Environ(), "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0", "AGENTDECK_EXIT_TRANSPORT_HELPER=1", "AGENTDECK_PID_FILE="+pidFile, "AGENTDECK_EOF_FILE="+eofFile, "PATH="+dir+":"+os.Getenv("PATH"))
			if phase == "pending-hello" {
				cmd.Env = append(cmd.Env, "AGENTDECK_PENDING_HELLO=1")
			}
			started := time.Now()
			output, helperErr := cmd.CombinedOutput()
			elapsed := time.Since(started)
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			defer process.Kill()
			if helperErr != nil {
				t.Fatalf("shutdown helper: %v: %s", helperErr, output)
			}
			// Linux containers can retain zombies until their init reaps them. A zombie
			// is terminated; signal 0 alone cannot distinguish it from a live orphan.
			deadline := time.Now().Add(time.Second)
			for {
				output, psErr := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
				if psErr != nil && !errors.As(psErr, new(*exec.ExitError)) {
					t.Fatal(psErr)
				}
				state := strings.TrimSpace(string(output))
				if process.Signal(syscall.Signal(0)) != nil || strings.HasPrefix(state, "Z") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("owned SSH pid %d survived parent exit (state %q, exit %v)", pid, state, elapsed)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := master.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated shared-master process was terminated: %v", err)
			}
			if elapsed > 5*time.Second {
				t.Fatalf("shutdown exceeded bound: %v", elapsed)
			}
			t.Logf("parent exited in %v; owned SSH pid %d terminated after confirmed stdin EOF", elapsed, pid)
		})
	}
}
