package mcppool

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var httpLog = logging.ForComponent(logging.CompHTTP)

// HTTPServer manages an HTTP MCP server process
type HTTPServer struct {
	name           string
	url            string
	healthCheckURL string
	command        string
	args           []string
	env            map[string]string
	startupTimeout time.Duration

	process   *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	logFile   string
	logWriter io.WriteCloser

	mu          sync.RWMutex
	status      ServerStatus
	startedByUs bool  // True if we started the server vs. discovered external
	lastError   error // Last error encountered

	// startMu serializes concurrent Start calls on the same server. Without
	// it, two callers could both observe StatusStarting (which the existing
	// early-return only catches as StatusRunning) and both reach `s.process
	// = exec.CommandContext(...)` — clobbering the reference and orphaning
	// one child. Reproducer: the HTTPPool's "exists -> existing.Start()"
	// path can fire concurrently under load (v1.9 cascade dedup).
	// Held for the full Start so waitReady's polling uses a stable s.process.
	// Distinct from `mu` because waitReady acquires mu.RLock, and `mu` is
	// not reentrant.
	startMu sync.Mutex

	// processDone is closed by monitorProcess() once it has Wait()'d on the
	// current s.process. killLeftoverProcess waits on it (with a 2s safety
	// net) instead of polling Cmd.ProcessState directly, which would race
	// with Wait()'s write to that field. Re-allocated per spawn.
	processDone chan struct{}
}

// NewHTTPServer creates a new HTTP server manager
func NewHTTPServer(ctx context.Context, name, url, healthCheckURL, command string, args []string, env map[string]string, startupTimeout time.Duration) *HTTPServer {
	ctx, cancel := context.WithCancel(ctx)

	// Use main URL for health check if not specified
	if healthCheckURL == "" {
		healthCheckURL = url
	}

	// Default timeout
	if startupTimeout <= 0 {
		startupTimeout = 5 * time.Second
	}

	return &HTTPServer{
		name:           name,
		url:            url,
		healthCheckURL: healthCheckURL,
		command:        command,
		args:           args,
		env:            env,
		startupTimeout: startupTimeout,
		ctx:            ctx,
		cancel:         cancel,
		status:         StatusStopped,
	}
}

// SetStatus safely updates the server status
func (s *HTTPServer) SetStatus(status ServerStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

// GetStatus safely reads the server status
func (s *HTTPServer) GetStatus() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// IsRunning returns true if the server is running
func (s *HTTPServer) IsRunning() bool {
	return s.GetStatus() == StatusRunning
}

// StartedByUs returns true if we started this server (vs. external discovery)
func (s *HTTPServer) StartedByUs() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.startedByUs
}

// GetURL returns the HTTP endpoint URL
func (s *HTTPServer) GetURL() string {
	return s.url
}

// GetName returns the server name
func (s *HTTPServer) GetName() string {
	return s.name
}

// GetLastError returns the last error encountered
func (s *HTTPServer) GetLastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// killLeftoverProcess SIGKILLs any previously-spawned child whose
// reference is still in s.process and which has not been reaped yet.
// Called at the top of Start (under startMu) so the new spawn cannot
// run alongside an orphaned older one. Idempotent.
//
// We don't call Wait() here because monitorProcess() is already calling
// it on the same Cmd, and exec.Cmd.Wait is not idempotent — a second call
// returns "exec: Wait was already called". Instead we send SIGKILL and
// wait on s.processDone, which monitorProcess closes after its Wait()
// returns. Polling Cmd.ProcessState directly would race with Wait()'s
// write to that field (caught by -race).
func (s *HTTPServer) killLeftoverProcess() {
	s.mu.Lock()
	old := s.process
	done := s.processDone
	s.mu.Unlock()

	if old == nil || old.Process == nil || done == nil {
		return
	}
	// Fast path: monitorProcess already finished — nothing to kill.
	select {
	case <-done:
		return
	default:
	}

	httpLog.Info("kill_leftover_process",
		slog.String("mcp", s.name),
		slog.Int("pid", old.Process.Pid))
	_ = old.Process.Kill()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		httpLog.Warn("leftover_process_reap_timeout",
			slog.String("mcp", s.name),
			slog.Int("pid", old.Process.Pid))
	}
}

// Start starts the HTTP server process
// If the URL is already reachable, marks as external and skips process creation
func (s *HTTPServer) Start() error {
	// Serialize concurrent Starts so the s.process write at line 156 isn't
	// raced. After we acquire startMu, any other Start that was already
	// in-flight has finished — we re-check status under mu and short-circuit
	// if the server is now running. Without this, two goroutines could both
	// pass the StatusRunning check while in StatusStarting and both spawn
	// a child process, leaking the first one. (v1.9 cascade dedup.)
	s.startMu.Lock()
	defer s.startMu.Unlock()

	// Reap any leftover child from a previous failed Start (e.g. waitReady
	// timed out and we returned without sending the spawned MCP a kill
	// signal). Without this, every Start cycle of an unhealthy MCP leaks
	// one alive child — the cascade-triggering pattern observed when 43
	// `@upstash/context7-mcp` instances accumulated before systemd-oomd
	// fired. Idempotent: returns immediately if there is no leftover.
	s.killLeftoverProcess()

	s.mu.Lock()

	// Already running?
	if s.status == StatusRunning {
		s.mu.Unlock()
		return nil
	}

	// Check if URL is already reachable (external server)
	if s.isURLReachable() {
		httpLog.Info("external_server_detected", slog.String("mcp", s.name))
		s.status = StatusRunning
		s.startedByUs = false
		s.mu.Unlock()
		return nil
	}

	// No command configured? Can't start
	if s.command == "" {
		s.mu.Unlock()
		return fmt.Errorf("HTTP MCP %s: URL not reachable and no server command configured", s.name)
	}

	s.status = StatusStarting
	s.mu.Unlock()

	// Create log file
	logDir := filepath.Join(os.Getenv("HOME"), ".agent-deck", "logs", "http-servers")
	_ = os.MkdirAll(logDir, 0700)
	s.logFile = filepath.Join(logDir, fmt.Sprintf("%s.log", s.name))

	logWriter, err := os.Create(s.logFile)
	if err != nil {
		s.SetStatus(StatusFailed)
		return fmt.Errorf("failed to create log file: %w", err)
	}
	s.logWriter = logWriter

	// If Start() returns before s.process.Start() succeeds, the caller has
	// no Stop() path and logWriter would leak its FD on every failed
	// start. Track whether the underlying process actually started: if not,
	// close logWriter on return. After process.Start() succeeds, monitorProcess
	// + s.process.Stderr need logWriter alive, and Stop() owns Close.
	// (V1.9 T5, critical-hunt #4.)
	processStarted := false
	defer func() {
		if !processStarted {
			_ = logWriter.Close()
			s.logWriter = nil
		}
	}()

	// Start the server process. On Linux+systemd we wrap each MCP child
	// inside its own transient user scope so systemd-oomd's per-cgroup
	// kill heuristic targets the misbehaving MCP, not the conductor.
	launchCmd, launchArgs, scopeWrapped, scopeUnit := wrapMCPCommand(
		fmt.Sprintf("%d", os.Getpid()), s.name, s.command, s.args)
	s.process = exec.CommandContext(s.ctx, launchCmd, launchArgs...)
	if scopeWrapped {
		httpLog.Info("mcp_isolation_scope",
			slog.String("mcp", s.name),
			slog.String("unit", scopeUnit))
	}
	cmdEnv := os.Environ()
	for k, v := range s.env {
		// Reject environment variables that could be used for code injection.
		if dangerousEnvVars[k] {
			httpLog.Warn("rejected_dangerous_env", slog.String("server", s.name), slog.String("var", k))
			continue
		}
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	s.process.Env = cmdEnv

	// Graceful shutdown: send SIGTERM on context cancel
	s.process.Cancel = func() error {
		return s.process.Process.Signal(syscall.SIGTERM)
	}
	s.process.WaitDelay = 3 * time.Second

	// Capture stderr for debugging
	s.process.Stderr = logWriter

	// Start process
	if err := s.process.Start(); err != nil {
		s.SetStatus(StatusFailed)
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
		return fmt.Errorf("failed to start HTTP server: %w", err)
	}
	// Process is now alive and owns logWriter via Stderr; deferred
	// fallback close must not run.
	processStarted = true

	httpLog.Info("server_process_started", slog.String("mcp", s.name), slog.Int("pid", s.process.Process.Pid))

	// Allocate the per-spawn done-channel before launching the watcher.
	// killLeftoverProcess uses this channel to wait for monitorProcess's
	// Wait() to return; polling cmd.ProcessState directly is racy.
	s.mu.Lock()
	s.processDone = make(chan struct{})
	done := s.processDone
	proc := s.process
	s.mu.Unlock()

	// Monitor process exit in background
	go func() {
		defer close(done)
		err := proc.Wait()
		if err != nil {
			httpLog.Warn("process_exit_error", slog.String("mcp", s.name), slog.String("error", err.Error()))
		} else {
			httpLog.Info("process_exited", slog.String("mcp", s.name))
		}
		s.mu.Lock()
		// Only mark as failed if we were running (not during shutdown)
		if s.status == StatusRunning {
			s.status = StatusFailed
			s.lastError = err
		}
		s.mu.Unlock()
	}()

	// Wait for server to become ready
	if err := s.waitReady(); err != nil {
		// Caller has no Stop() path on a failed Start, so we must
		// clean up the child + log FD ourselves here. Without this,
		// every Start cycle of an unhealthy MCP leaked one log FD
		// (TestHTTPServer_Start_FailingCommand_NoFDLeak, v1.9
		// release blocker). Kill, then wait on processDone (closed
		// by monitorProcess after Wait()) before closing logWriter
		// so the child's stderr can't write into a closed file.
		if proc != nil && proc.Process != nil {
			_ = proc.Process.Kill()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				httpLog.Warn("waitready_reap_timeout",
					slog.String("mcp", s.name))
			}
		}
		_ = logWriter.Close()
		s.mu.Lock()
		s.logWriter = nil
		s.lastError = err
		s.mu.Unlock()
		s.SetStatus(StatusFailed)
		return fmt.Errorf("HTTP server %s failed to become ready: %w", s.name, err)
	}

	s.mu.Lock()
	s.status = StatusRunning
	s.startedByUs = true
	s.mu.Unlock()

	httpLog.Info("server_ready", slog.String("mcp", s.name), slog.String("url", s.url))
	return nil
}

// Stop stops the HTTP server process
func (s *HTTPServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel context to trigger graceful shutdown
	if s.cancel != nil {
		s.cancel()
	}

	// Only kill process if we started it
	if s.process != nil && s.startedByUs {
		// Context cancel triggers SIGTERM, WaitDelay handles escalation
		done := make(chan error, 1)
		go func() {
			done <- s.process.Wait()
		}()

		select {
		case err := <-done:
			if err != nil {
				httpLog.Warn("process_exit_error", slog.String("mcp", s.name), slog.String("error", err.Error()))
			}
		case <-time.After(5 * time.Second):
			httpLog.Warn("process_wait_timeout", slog.String("mcp", s.name))
			_ = s.process.Process.Kill()
			<-done
		}
		httpLog.Info("process_stopped", slog.String("mcp", s.name))
	} else if s.process == nil && !s.startedByUs {
		httpLog.Info("external_server_disconnected", slog.String("mcp", s.name))
	}

	// Close log writer
	if s.logWriter != nil {
		s.logWriter.Close()
	}

	s.status = StatusStopped
	return nil
}

// HealthCheck checks if the server is responding
func (s *HTTPServer) HealthCheck() error {
	if !s.isURLReachable() {
		return fmt.Errorf("server not responding at %s", s.healthCheckURL)
	}
	return nil
}

// isURLReachable checks if the URL is reachable with a short timeout
func (s *HTTPServer) isURLReachable() bool {
	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	resp, err := client.Get(s.healthCheckURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Accept any 2xx or 4xx status (4xx means server is up but maybe auth required)
	// 5xx typically means server error
	return resp.StatusCode < 500
}

// waitReady polls the URL until it becomes reachable or timeout
func (s *HTTPServer) waitReady() error {
	pollInterval := 100 * time.Millisecond
	deadline := time.Now().Add(s.startupTimeout)

	// Snapshot the per-spawn done channel. monitorProcess closes it after
	// Wait() returns, so it is the Wait-safe signal that the child exited.
	// Reading s.process.ProcessState directly races with Wait()'s write to
	// that field (see killLeftoverProcess comment for the same pattern).
	s.mu.RLock()
	done := s.processDone
	s.mu.RUnlock()

	for time.Now().Before(deadline) {
		// Check if process exited via the done channel — no race with
		// the monitor goroutine's Wait().
		if done != nil {
			select {
			case <-done:
				return fmt.Errorf("process exited before becoming ready")
			default:
			}
		}

		// Check if URL is reachable
		if s.isURLReachable() {
			return nil
		}

		// Wait before next poll
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	return fmt.Errorf("timeout waiting for server to start (waited %v)", s.startupTimeout)
}

// Restart stops and restarts the server
func (s *HTTPServer) Restart() error {
	if err := s.Stop(); err != nil {
		httpLog.Error("stop_error", slog.String("mcp", s.name), slog.String("error", err.Error()))
	}

	// Create new context
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mu.Unlock()

	return s.Start()
}
