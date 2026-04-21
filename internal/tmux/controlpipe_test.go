package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
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
	name := createTestSession(t, "stale-ctrl")

	// Use NewControlPipe to create a proper control-mode client that we know works,
	// then simulate it being "stale" by tracking its PID before killing it via our function.
	stalePipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	stalePID := stalePipe.cmd.Process.Pid
	t.Cleanup(func() { stalePipe.Close() })

	// Verify the control client is registered
	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 3*time.Second, 100*time.Millisecond, "control client should register")

	// Kill stale clients — this should kill the pipe's process
	killStaleControlClients(name, "")

	// Verify the control client is no longer listed by tmux
	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return !strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 2*time.Second, 100*time.Millisecond, "stale control client should be gone from tmux client list")
}

func TestPipeManager_ConnectCleansStaleClients(t *testing.T) {
	name := createTestSession(t, "pm-stale")

	// Create a stale control pipe (simulates orphan from previous TUI)
	stalePipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	stalePID := stalePipe.cmd.Process.Pid
	t.Cleanup(func() { stalePipe.Close() })

	// Verify stale client is registered
	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", stalePID))
	}, 3*time.Second, 100*time.Millisecond)

	// Connect via PipeManager — should kill stale client and create a new one
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, nil)
	defer pm.Close()

	require.NoError(t, pm.Connect(name, ""))
	assert.True(t, pm.IsConnected(name))

	// Stale client should be gone
	out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
	assert.NotContains(t, string(out), fmt.Sprintf("1 %d", stalePID), "stale client should have been killed by Connect")
}

func TestKillStaleControlClients_PreservesOwnProcess(t *testing.T) {
	name := createTestSession(t, "own-proc")

	// killStaleControlClients should not kill our own PID
	// (this is mostly a safety check — our PID is never a tmux control client)
	killStaleControlClients(name, "") // should not panic or kill us
}

// --- Helpers ---

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
