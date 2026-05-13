package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestSession creates a tmux session for testing and returns its name.
// Caller must defer cleanup.
func createTestSession(t *testing.T, suffix string) string {
	t.Helper()
	skipIfNoTmuxServer(t)

	name := SessionPrefix + "cptest-" + suffix
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	require.NoError(t, cmd.Run(), "failed to create test session %s", name)

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	return name
}

// createTestSessionStrict is like createTestSession but only skips when the
// tmux binary itself is missing. Used by #927 regression tests so they
// actively run in CI rather than silent-skipping on cold-boot envs (where
// the only live session is the TestMain bootstrap).
func createTestSessionStrict(t *testing.T, suffix string) string {
	t.Helper()
	skipIfNoTmuxBinary(t)

	name := SessionPrefix + "cptest-" + suffix
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
	require.NoError(t, cmd.Run(), "failed to create test session %s", name)

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	return name
}

func TestControlPipe_ConnectAndClose(t *testing.T) {
	name := createTestSession(t, "connect")

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	defer pipe.Close()

	assert.True(t, pipe.IsAlive())

	pipe.Close()
	// Give reader goroutine time to exit
	time.Sleep(100 * time.Millisecond)
	assert.False(t, pipe.IsAlive())
}

func TestControlPipe_CapturePaneVia(t *testing.T) {
	name := createTestSession(t, "capture")

	// Send some content to the session
	_ = exec.Command("tmux", "send-keys", "-t", name, "echo hello-from-pipe-test", "Enter").Run()
	time.Sleep(300 * time.Millisecond)

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	defer pipe.Close()

	content, err := pipe.CapturePaneVia()
	require.NoError(t, err)
	assert.Contains(t, content, "hello-from-pipe-test")
}

func TestControlPipe_OutputEvents(t *testing.T) {
	name := createTestSession(t, "output")

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	defer pipe.Close()

	// Small delay to let pipe fully connect
	time.Sleep(200 * time.Millisecond)

	// Drain any initial output events from session startup
	drainEvents(pipe.OutputEvents(), 200*time.Millisecond)

	// Send output to the session
	_ = exec.Command("tmux", "send-keys", "-t", name, "echo output-event-test", "Enter").Run()

	// Wait for output event
	select {
	case <-pipe.OutputEvents():
		// Got an output event, verify lastOutput was updated
		lastOut := pipe.LastOutputTime()
		assert.False(t, lastOut.IsZero(), "lastOutput should be set after %output event")
		assert.WithinDuration(t, time.Now(), lastOut, 2*time.Second)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for output event")
	}
}

func TestControlPipe_SendCommand(t *testing.T) {
	name := createTestSession(t, "sendcmd")

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	defer pipe.Close()

	// Send display-message to get window activity
	output, err := pipe.SendCommand("display-message -t " + name + " -p '#{window_activity}'")
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(output))
}

func TestControlPipe_DeadSession(t *testing.T) {
	skipIfNoTmuxServer(t)

	// Try connecting to a non-existent session
	_, err := NewControlPipe("agentdeck_nonexistent_session_12345", "")
	// Should either fail to connect or die quickly
	if err == nil {
		// Wait a moment for pipe to realize session doesn't exist
		time.Sleep(500 * time.Millisecond)
	}
	// This is expected behavior - non-existent sessions may fail differently
}

func TestControlPipe_CloseIdempotent(t *testing.T) {
	name := createTestSession(t, "closeidempotent")

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)

	// Close multiple times should not panic
	pipe.Close()
	pipe.Close()
	pipe.Close()
}

// --- PipeManager Tests ---

func TestPipeManager_ConnectDisconnect(t *testing.T) {
	name := createTestSession(t, "pm-conn")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))
	assert.True(t, pm.IsConnected(name))
	assert.Equal(t, 1, pm.ConnectedCount())

	pm.Disconnect(name)
	assert.False(t, pm.IsConnected(name))
	assert.Equal(t, 0, pm.ConnectedCount())
}

func TestPipeManager_CapturePane(t *testing.T) {
	name := createTestSession(t, "pm-capture")

	_ = exec.Command("tmux", "send-keys", "-t", name, "echo pm-capture-test", "Enter").Run()
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))

	content, err := pm.CapturePane(name)
	require.NoError(t, err)
	assert.Contains(t, content, "pm-capture-test")
}

func TestPipeManager_CapturePaneFallback(t *testing.T) {
	skipIfNoTmuxServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	// CapturePane on unconnected session should return error (caller falls back to subprocess)
	_, err := pm.CapturePane("nonexistent_session")
	assert.Error(t, err)
}

func TestPipeManager_OutputCallback(t *testing.T) {
	name := createTestSession(t, "pm-output")

	var mu sync.Mutex
	var callbackSession string

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, func(sessionName string) {
		mu.Lock()
		callbackSession = sessionName
		mu.Unlock()
	})
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))
	time.Sleep(200 * time.Millisecond)

	// Drain initial events
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	callbackSession = ""
	mu.Unlock()

	// Generate output
	_ = exec.Command("tmux", "send-keys", "-t", name, "echo callback-test", "Enter").Run()

	// Wait for callback
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		got := callbackSession
		mu.Unlock()
		if got == name {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for output callback")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestPipeManager_RefreshAllActivities(t *testing.T) {
	name := createTestSession(t, "pm-refresh")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))

	activities, windows, err := pm.RefreshAllActivities()
	require.NoError(t, err)
	assert.NotEmpty(t, activities, "should return at least one session's activity")
	assert.NotNil(t, windows, "should return window data")

	// Our test session should be in the results
	_, found := activities[name]
	assert.True(t, found, "test session %s should appear in activities", name)
}

func TestPipeManager_ConnectIdempotent(t *testing.T) {
	name := createTestSession(t, "pm-idempotent")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	// Connect twice should not error and should maintain one connection
	require.NoError(t, pm.Connect(name, ""))
	require.NoError(t, pm.Connect(name, ""))
	assert.Equal(t, 1, pm.ConnectedCount())
}

func TestPipeManager_GlobalSingleton(t *testing.T) {
	// Singleton should be nil initially (or from previous test state)
	old := GetPipeManager()
	defer SetPipeManager(old) // Restore

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm := NewPipeManager(ctx, nil)
	SetPipeManager(pm)

	assert.Equal(t, pm, GetPipeManager())

	SetPipeManager(nil)
	assert.Nil(t, GetPipeManager())
}

func TestKillStaleControlClients(t *testing.T) {
	name := createTestSessionStrict(t, "stale-ctrl")

	// Simulate the #595 orphan scenario: a previous agent-deck TUI crashed,
	// leaving its `tmux -C attach-session` child reparented to init/systemd.
	// We materialize that state by spawning a helper subprocess that creates
	// the control client and then exits — the grandchild outlives the helper
	// and is now truly orphaned (PPID reparented away from any live TUI).
	stalePID := spawnOrphanControlClient(t, name)

	// Verify the orphan registered with tmux.
	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 3*time.Second, 100*time.Millisecond, "orphaned control client should register")

	// Kill stale clients — orphan should be reaped.
	killStaleControlClients(name, "")

	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return !strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 2*time.Second, 100*time.Millisecond, "orphaned control client should be gone from tmux client list")
}

func TestPipeManager_ConnectCleansStaleClients(t *testing.T) {
	name := createTestSessionStrict(t, "pm-stale")

	// True orphan: spawned via a helper subprocess that exits, so the
	// `tmux -C attach-session` grandchild is reparented away.
	stalePID := spawnOrphanControlClient(t, name)

	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 3*time.Second, 100*time.Millisecond)

	// Connect via PipeManager — should kill stale orphan and create a new one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))
	assert.True(t, pm.IsConnected(name))

	out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
	assert.NotContains(t, string(out), fmt.Sprintf("1 %d", stalePID), "orphaned client should have been killed by Connect")
}

// TestKillStaleControlClients_PreservesLiveSibling is the #927 regression
// guard. With allow_multiple=true (default), two simultaneous agent-deck
// TUIs share a tmux server and each spawns its own control-mode client per
// session. Before the fix, each TUI's killStaleControlClients sweep treated
// the OTHER TUI's clients as orphans and SIGTERM'd them — both pipes
// oscillated to StatusError within ~20s, bricking the two-TUI use case
// (PC + phone via SSH).
//
// The simulated live sibling here is a ControlPipe spawned directly by the
// test binary. Its parent (the test process) is alive AND has an
// agent-deck-like executable path (matches os.Executable()), so the fixed
// killStaleControlClients must classify it as a live sibling and leave it
// alone.
//
// Unlike the legacy stale-client tests, this one uses skipIfNoTmuxBinary
// (rather than skipIfNoTmuxServer) so it actively runs in CI — the duel
// regression is meaningless if its guard silent-skips.
func TestKillStaleControlClients_PreservesLiveSibling(t *testing.T) {
	name := createTestSessionStrict(t, "live-sibling")

	siblingPipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	siblingPID := siblingPipe.cmd.Process.Pid
	t.Cleanup(func() { siblingPipe.Close() })

	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", siblingPID))
	}, 3*time.Second, 100*time.Millisecond, "sibling control client should register")

	// Simulate the other TUI's cleanup sweep.
	killStaleControlClients(name, "")

	// Give a soft-kill SIGTERM enough time to land if the fix is absent.
	// controlClientKillGrace is 500ms; we wait a bit longer than the full
	// SIGTERM→SIGKILL cycle (1s) to catch escalation too.
	time.Sleep(controlClientKillGrace + 750*time.Millisecond)

	assert.True(t, siblingPipe.IsAlive(),
		"live sibling TUI's control client must survive killStaleControlClients (#927)")

	// Sibling must remain in tmux's client list.
	out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
	assert.Contains(t, string(out), fmt.Sprintf("1 %d", siblingPID),
		"live sibling's pid must still be tracked by tmux after kill sweep")
}

// TestPipeManager_ConnectPreservesLiveSibling verifies that the fix carries
// through the high-level PipeManager.Connect path that calls
// killStaleControlClients(). Two simultaneous TUIs reconnecting to the same
// session must not delete each other's control pipes.
func TestPipeManager_ConnectPreservesLiveSibling(t *testing.T) {
	name := createTestSessionStrict(t, "pm-live-sibling")

	// "Sibling TUI" pipe.
	siblingPipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	siblingPID := siblingPipe.cmd.Process.Pid
	t.Cleanup(func() { siblingPipe.Close() })

	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", siblingPID))
	}, 3*time.Second, 100*time.Millisecond)

	// "This TUI" connects — must not kill the sibling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))
	assert.True(t, pm.IsConnected(name))

	time.Sleep(controlClientKillGrace + 750*time.Millisecond)
	assert.True(t, siblingPipe.IsAlive(),
		"sibling pipe must remain alive after PipeManager.Connect (#927)")

	out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
	assert.Contains(t, string(out), fmt.Sprintf("1 %d", siblingPID))
}

func TestKillStaleControlClients_PreservesOwnProcess(t *testing.T) {
	name := createTestSession(t, "own-proc")

	// killStaleControlClients should not kill our own PID
	// (this is mostly a safety check — our PID is never a tmux control client)
	killStaleControlClients(name, "") // should not panic or kill us
}

// --- Helpers ---

// runOrphanControlClientHelper is the TestMain child-helper entry point for
// ORPHAN_CONTROL_CLIENT_HELPER. It spawns a `tmux -C attach-session` against
// the given session, prints the spawned PID to stdout, releases the process,
// and exits. The grandchild outlives this helper and is reparented away from
// the original test binary, materializing the post-crash orphan state.
//
// The child's stdin is wired to /dev/zero so it survives this helper's exit.
// `tmux -C` exits on stdin EOF; a stdin pipe would EOF the moment the helper
// process closes its FD on exit, defeating the orphan setup. A /dev/zero
// reader's open FD is inherited by the child (fork-exec dup) and is
// independent of this helper's lifetime, so the child reads null bytes
// indefinitely without seeing EOF — exactly the long-lived stale-client
// state #595 was added to clean up.
//
// Failure modes are funneled to stderr+non-zero exit so the parent's
// `cmd.Output()` returns a useful error.
func runOrphanControlClientHelper(sessionName string) {
	zero, err := os.Open("/dev/zero")
	if err != nil {
		fmt.Fprintf(os.Stderr, "orphan helper: open /dev/zero: %v\n", err)
		os.Exit(2)
	}
	cmd := exec.Command("tmux", "-C", "attach-session", "-t", sessionName)
	cmd.Stdin = zero
	// Put the grandchild in its own process group so it survives this
	// helper's exit cleanly (mirrors ControlPipe's own Setpgid).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "orphan helper: start tmux -C failed: %v\n", err)
		os.Exit(2)
	}
	fmt.Println(cmd.Process.Pid)
	// Release so the Go runtime doesn't wait on the grandchild. The kernel
	// will reparent it to init / systemd-user on exit.
	_ = cmd.Process.Release()
	os.Exit(0)
}

// spawnOrphanControlClient runs a helper subprocess (the test binary with
// ORPHAN_CONTROL_CLIENT_HELPER set; see testmain_test.go) that creates a
// `tmux -C attach-session` child and then exits. The grandchild is reparented
// to init / systemd-user / launchd — i.e., truly orphaned, the exact state
// killStaleControlClients was added to clean up in #595. Returns the orphan
// PID. The caller is responsible for triggering its cleanup (via
// killStaleControlClients or a t.Cleanup of its own).
func spawnOrphanControlClient(t *testing.T, sessionName string) int {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"ORPHAN_CONTROL_CLIENT_HELPER="+sessionName,
	)
	out, err := cmd.Output()
	require.NoError(t, err, "orphan helper subprocess failed")

	line := strings.TrimSpace(string(out))
	pid, err := strconv.Atoi(line)
	require.NoErrorf(t, err, "orphan helper produced non-numeric pid: %q", line)

	// Best-effort safety net: if the test fails before killStaleControlClients
	// reaps the orphan, make sure it dies with the test instead of leaking.
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	return pid
}

func drainEvents(ch <-chan struct{}, duration time.Duration) {
	deadline := time.After(duration)
	for {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}
