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

// Start starts the HTTP server process
// If the URL is already reachable, marks as external and skips process creation
func (s *HTTPServer) Start() error {
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

	// Start the server process
	s.process = exec.CommandContext(s.ctx, s.command, s.args...)
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

	httpLog.Info("server_process_started", slog.String("mcp", s.name), slog.Int("pid", s.process.Process.Pid))

	// Monitor process exit in background
	go s.monitorProcess()

	// Wait for server to become ready
	if err := s.waitReady(); err != nil {
		// Process may have already exited
		s.SetStatus(StatusFailed)
		s.mu.Lock()
		s.lastError = err
		s.mu.Unlock()
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

	for time.Now().Before(deadline) {
		// Check if process exited
		s.mu.RLock()
		if s.process != nil && s.process.ProcessState != nil {
			s.mu.RUnlock()
			return fmt.Errorf("process exited before becoming ready")
		}
		s.mu.RUnlock()

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

// monitorProcess watches for process exit and updates status
func (s *HTTPServer) monitorProcess() {
	if s.process == nil {
		return
	}

	err := s.process.Wait()
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
