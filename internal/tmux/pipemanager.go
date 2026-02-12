package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// PipeManager manages ControlPipes for all active tmux sessions.
// It provides zero-subprocess CapturePane and event-driven output detection.
// Falls back to subprocess execution when pipes are unavailable.
type PipeManager struct {
	pipes map[string]*ControlPipe // sessionName -> pipe
	mu    sync.RWMutex

	// Callback for output events (invoked when %output detected from a session)
	onOutput func(sessionName string)

	// Reconnection tracking
	reconnectMu  sync.Mutex
	reconnecting map[string]bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

// NewPipeManager creates a new PipeManager. The onOutput callback is invoked
// whenever a connected session produces terminal output (via %output events).
func NewPipeManager(ctx context.Context, onOutput func(sessionName string)) *PipeManager {
	childCtx, cancel := context.WithCancel(ctx)
	return &PipeManager{
		pipes:        make(map[string]*ControlPipe),
		onOutput:     onOutput,
		reconnecting: make(map[string]bool),
		ctx:          childCtx,
		cancel:       cancel,
	}
}

// Connect creates a control mode pipe for the given tmux session.
// If a pipe already exists and is alive, this is a no-op.
// Uses reconnecting map to prevent concurrent pipe creation for the same session.
func (pm *PipeManager) Connect(sessionName string) error {
	pm.mu.Lock()

	// Already connected and alive?
	if existing, ok := pm.pipes[sessionName]; ok && existing.IsAlive() {
		pm.mu.Unlock()
		return nil
	}

	// Clean up dead pipe if present
	if existing, ok := pm.pipes[sessionName]; ok {
		existing.Close()
		delete(pm.pipes, sessionName)
	}
	pm.mu.Unlock()

	// Prevent concurrent pipe creation for the same session (TOCTOU guard)
	pm.reconnectMu.Lock()
	if pm.reconnecting[sessionName] {
		pm.reconnectMu.Unlock()
		return nil // Another goroutine is already connecting
	}
	pm.reconnecting[sessionName] = true
	pm.reconnectMu.Unlock()

	defer func() {
		pm.reconnectMu.Lock()
		delete(pm.reconnecting, sessionName)
		pm.reconnectMu.Unlock()
	}()

	// Create new pipe (outside lock since it spawns a process)
	pipe, err := NewControlPipe(sessionName)
	if err != nil {
		return fmt.Errorf("connect pipe for %s: %w", sessionName, err)
	}

	pm.mu.Lock()
	// Double-check: another goroutine may have connected while we were creating
	if existing, ok := pm.pipes[sessionName]; ok && existing.IsAlive() {
		pm.mu.Unlock()
		pipe.Close() // Discard the one we just created
		return nil
	}
	pm.pipes[sessionName] = pipe
	pm.mu.Unlock()

	// Start output event forwarder
	go pm.forwardOutputEvents(sessionName, pipe)

	// Start reconnection watcher
	go pm.watchPipe(sessionName, pipe)

	return nil
}

// Disconnect closes and removes the pipe for the given session.
func (pm *PipeManager) Disconnect(sessionName string) {
	pm.mu.Lock()
	pipe, ok := pm.pipes[sessionName]
	if ok {
		delete(pm.pipes, sessionName)
	}
	pm.mu.Unlock()

	if pipe != nil {
		pipe.Close()
	}
	pipeLog.Debug("pipe_disconnected", slog.String("session", sessionName))
}

// GetPipe returns the ControlPipe for a session, or nil if not connected.
func (pm *PipeManager) GetPipe(sessionName string) *ControlPipe {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pipes[sessionName]
}

// CapturePane routes capture-pane through the control mode pipe if available.
// Falls back to subprocess execution if the pipe is nil, dead, or errors.
func (pm *PipeManager) CapturePane(sessionName string) (string, error) {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil || !pipe.IsAlive() {
		return "", fmt.Errorf("no pipe for session %s", sessionName)
	}

	return pipe.CapturePaneVia()
}

// GetWindowActivity sends a display-message command through the pipe to get
// the window_activity timestamp. Falls back to error if pipe unavailable.
func (pm *PipeManager) GetWindowActivity(sessionName string) (int64, error) {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil || !pipe.IsAlive() {
		return 0, fmt.Errorf("no pipe for session %s", sessionName)
	}

	output, err := pipe.SendCommand(fmt.Sprintf(`display-message -t %s -p "#{window_activity}"`, sessionName))
	if err != nil {
		return 0, err
	}

	var ts int64
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%d", &ts)
	if err != nil {
		return 0, fmt.Errorf("parse window_activity: %w", err)
	}
	return ts, nil
}

// RefreshAllActivities sends a single list-windows command through any available
// pipe to get activity timestamps for ALL sessions. This replaces the subprocess
// call in RefreshSessionCache.
func (pm *PipeManager) RefreshAllActivities() (map[string]int64, error) {
	pm.mu.RLock()
	// Find any alive pipe to send the command through
	var pipe *ControlPipe
	for _, p := range pm.pipes {
		if p.IsAlive() {
			pipe = p
			break
		}
	}
	pm.mu.RUnlock()

	if pipe == nil {
		return nil, fmt.Errorf("no alive pipes available")
	}

	// tmux control mode requires double-quoted format strings containing special chars
	output, err := pipe.SendCommand(`list-windows -a -F "#{session_name}\t#{window_activity}"`)
	if err != nil {
		return nil, fmt.Errorf("list-windows via pipe: %w", err)
	}

	result := make(map[string]int64)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		var activity int64
		_, _ = fmt.Sscanf(parts[1], "%d", &activity)
		// Keep maximum activity if session has multiple windows
		if existing, ok := result[name]; !ok || activity > existing {
			result[name] = activity
		}
	}
	return result, nil
}

// RefreshAllPaneInfo sends a single list-panes command through any available
// pipe to get pane titles and current commands for ALL sessions. This provides
// the data needed for title-based state detection without subprocess spawns.
func (pm *PipeManager) RefreshAllPaneInfo() (map[string]PaneInfo, error) {
	pm.mu.RLock()
	var pipe *ControlPipe
	for _, p := range pm.pipes {
		if p.IsAlive() {
			pipe = p
			break
		}
	}
	pm.mu.RUnlock()

	if pipe == nil {
		return nil, fmt.Errorf("no alive pipes available")
	}

	output, err := pipe.SendCommand(`list-panes -a -F "#{session_name}\t#{pane_title}\t#{pane_current_command}"`)
	if err != nil {
		return nil, fmt.Errorf("list-panes via pipe: %w", err)
	}

	result := make(map[string]PaneInfo)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		// Keep last pane per session (most sessions have one pane)
		result[parts[0]] = PaneInfo{
			Title:          parts[1],
			CurrentCommand: parts[2],
		}
	}
	return result, nil
}

// LastOutputTime returns the last output time for a session from its pipe.
// Returns zero time if no pipe or no output recorded.
func (pm *PipeManager) LastOutputTime(sessionName string) time.Time {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil {
		return time.Time{}
	}
	return pipe.LastOutputTime()
}

// IsConnected returns true if a session has an alive pipe.
func (pm *PipeManager) IsConnected(sessionName string) bool {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()
	return pipe != nil && pipe.IsAlive()
}

// ConnectedCount returns the number of alive pipes.
func (pm *PipeManager) ConnectedCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, p := range pm.pipes {
		if p.IsAlive() {
			count++
		}
	}
	return count
}

// Close shuts down all pipes and cancels the context.
func (pm *PipeManager) Close() {
	pm.cancel()

	pm.mu.Lock()
	pipes := make(map[string]*ControlPipe, len(pm.pipes))
	maps.Copy(pipes, pm.pipes)
	pm.pipes = make(map[string]*ControlPipe)
	pm.mu.Unlock()

	for name, pipe := range pipes {
		pipe.Close()
		pipeLog.Debug("pipe_shutdown", slog.String("session", name))
	}
}

// forwardOutputEvents reads from a pipe's output events channel and calls
// the onOutput callback. Runs until the pipe dies or context is cancelled.
func (pm *PipeManager) forwardOutputEvents(sessionName string, pipe *ControlPipe) {
	for {
		select {
		case <-pm.ctx.Done():
			return
		case _, ok := <-pipe.OutputEvents():
			if !ok {
				return
			}
			if pm.onOutput != nil {
				pm.onOutput(sessionName)
			}
		case <-pipe.Done():
			return
		}
	}
}

// watchPipe monitors a pipe and attempts reconnection when it dies.
// Uses exponential backoff: 2s, 4s, 8s, 16s, 30s max.
// Stops retrying if the tmux session no longer exists.
func (pm *PipeManager) watchPipe(sessionName string, pipe *ControlPipe) {
	select {
	case <-pipe.Done():
		// Pipe died
	case <-pm.ctx.Done():
		return
	}

	pipeLog.Debug("pipe_died_scheduling_reconnect", slog.String("session", sessionName))

	// Check if already reconnecting
	pm.reconnectMu.Lock()
	if pm.reconnecting[sessionName] {
		pm.reconnectMu.Unlock()
		return
	}
	pm.reconnecting[sessionName] = true
	pm.reconnectMu.Unlock()

	defer func() {
		pm.reconnectMu.Lock()
		delete(pm.reconnecting, sessionName)
		pm.reconnectMu.Unlock()
	}()

	backoff := 2 * time.Second
	maxBackoff := 30 * time.Second
	maxRetries := 5

	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-pm.ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Check if session still exists before trying to reconnect.
		// Avoids infinite reconnect loops for deleted/non-existent sessions.
		if !tmuxSessionExists(sessionName) {
			pipeLog.Debug("pipe_reconnect_session_gone", slog.String("session", sessionName))
			pm.mu.Lock()
			delete(pm.pipes, sessionName)
			pm.mu.Unlock()
			return
		}

		err := pm.Connect(sessionName)
		if err == nil {
			pipeLog.Info("pipe_reconnected", slog.String("session", sessionName))
			return
		}

		pipeLog.Debug("pipe_reconnect_failed",
			slog.String("session", sessionName),
			slog.String("error", err.Error()),
			slog.Int("attempt", attempt+1),
			slog.Duration("next_retry", backoff))

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	pipeLog.Debug("pipe_reconnect_gave_up", slog.String("session", sessionName), slog.Int("max_retries", maxRetries))
	pm.mu.Lock()
	delete(pm.pipes, sessionName)
	pm.mu.Unlock()
}

// tmuxSessionExists checks if a tmux session exists (lightweight subprocess).
func tmuxSessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// --- Global singleton ---

var (
	globalPipeManager   *PipeManager
	globalPipeManagerMu sync.RWMutex
)

// SetPipeManager sets the global PipeManager instance (called once at startup).
func SetPipeManager(pm *PipeManager) {
	globalPipeManagerMu.Lock()
	globalPipeManager = pm
	globalPipeManagerMu.Unlock()
}

// GetPipeManager returns the global PipeManager instance.
// Returns nil if not initialized (control pipes disabled or not yet started).
func GetPipeManager() *PipeManager {
	globalPipeManagerMu.RLock()
	defer globalPipeManagerMu.RUnlock()
	return globalPipeManager
}
