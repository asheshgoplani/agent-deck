package mcppool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var proxyLog = logging.ForComponent(logging.CompPool)

// idMapping holds the reverse-lookup data for a single in-flight JSON-RPC request.
// The proxy rewrites the client-supplied ID to a proxy-scoped atomic counter value
// before forwarding to the MCP process. This struct stores both the original client
// ID (to restore in the response) and the session that issued the request (to route
// the response back to the correct client).
type idMapping struct {
	sessionID  string
	originalID interface{}
	sentAt     time.Time // For round-trip latency tracking (debug mode only)
}

// SocketProxy wraps a stdio MCP process with a Unix socket
type SocketProxy struct {
	name       string
	socketPath string
	command    string
	args       []string
	env        map[string]string

	mcpProcess *exec.Cmd
	mcpStdin   io.WriteCloser
	mcpStdout  io.ReadCloser

	listener net.Listener

	clients   map[string]net.Conn
	clientsMu sync.RWMutex

	// nextID is a proxy-scoped monotonic counter. Every incoming request ID is
	// replaced with nextID.Add(1) before being forwarded to the MCP process,
	// ensuring globally unique IDs across all sessions sharing this proxy.
	nextID atomic.Int64
	// idMap maps proxy-assigned int64 IDs to the original idMapping so responses
	// can be routed back to the correct session with the original ID restored.
	// Key type: int64; value type: idMapping.
	idMap sync.Map

	ctx    context.Context
	cancel context.CancelFunc

	logFile   string
	logWriter io.WriteCloser

	Status        ServerStatus
	statusMu      sync.RWMutex // Protects Status field
	lastRestart   time.Time    // For rate limiting restarts
	restartCount  int          // Track restart attempts
	totalFailures int          // Cumulative failures across all restarts
	successSince  time.Time    // When the proxy last became StatusRunning
}

// SetStatus safely updates the proxy status
func (p *SocketProxy) SetStatus(s ServerStatus) {
	p.statusMu.Lock()
	p.Status = s
	p.statusMu.Unlock()
}

// GetStatus safely reads the proxy status
func (p *SocketProxy) GetStatus() ServerStatus {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	return p.Status
}

type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// isSocketAlive checks if a Unix socket exists and is accepting connections
func isSocketAlive(socketPath string) bool {
	// Check if socket file exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return false
	}

	// Try to connect - if successful, socket is alive
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		// Socket file exists but no one listening - it's stale
		return false
	}
	conn.Close()
	return true
}

// dangerousEnvVars are environment variables that could be used for code injection
// (e.g., LD_PRELOAD to load arbitrary shared libraries). These are rejected when
// passed via MCP config to prevent malicious .mcp.json files from hijacking processes.
var dangerousEnvVars = map[string]bool{
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
	"DYLD_INSERT_LIBRARIES":    true,
	"DYLD_LIBRARY_PATH":        true,
	"DYLD_FRAMEWORK_PATH":      true,
	"DYLD_FALLBACK_LIBRARY_PATH": true,
}

// mcpSocketDir returns a user-private directory for MCP sockets instead of /tmp.
func mcpSocketDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("agentdeck-%d", os.Getuid()))
	}
	return filepath.Join(home, ".agent-deck", "sockets")
}

func NewSocketProxy(ctx context.Context, name, command string, args []string, env map[string]string) (*SocketProxy, error) {
	ctx, cancel := context.WithCancel(ctx)

	sockDir := mcpSocketDir()
	_ = os.MkdirAll(sockDir, 0700)
	socketPath := filepath.Join(sockDir, fmt.Sprintf("mcp-%s.sock", name))

	// Check if socket already exists and is alive (another agent-deck instance owns it)
	if isSocketAlive(socketPath) {
		proxyLog.Info("socket_reuse_external", slog.String("mcp", name))
		// Return a proxy that just points to the existing socket (no process to manage).
		// nextID and idMap zero values are ready to use without explicit initialization.
		return &SocketProxy{
			name:       name,
			socketPath: socketPath,
			command:    command,
			args:       args,
			env:        env,
			clients:    make(map[string]net.Conn),
			ctx:        ctx,
			cancel:     cancel,
			Status:     StatusRunning, // Mark as running since external socket is alive
		}, nil
	}

	// Socket doesn't exist or is stale - remove and create fresh
	os.Remove(socketPath)

	return &SocketProxy{
		name:       name,
		socketPath: socketPath,
		command:    command,
		args:       args,
		env:        env,
		clients:    make(map[string]net.Conn),
		ctx:        ctx,
		cancel:     cancel,
		Status:     StatusStarting,
	}, nil
}

func (p *SocketProxy) Start() error {
	// If already running (reusing external socket), skip process creation
	if p.GetStatus() == StatusRunning {
		proxyLog.Info("socket_reuse_existing", slog.String("mcp", p.name))
		return nil
	}

	logDir := filepath.Join(os.Getenv("HOME"), ".agent-deck", "logs", "mcppool")
	_ = os.MkdirAll(logDir, 0700)
	p.logFile = filepath.Join(logDir, fmt.Sprintf("%s_socket.log", p.name))

	logWriter, err := os.Create(p.logFile)
	if err != nil {
		return fmt.Errorf("failed to create log: %w", err)
	}
	p.logWriter = logWriter

	p.mcpProcess = exec.CommandContext(p.ctx, p.command, p.args...)
	cmdEnv := os.Environ()
	for k, v := range p.env {
		// Reject environment variables that could be used for code injection.
		if dangerousEnvVars[k] {
			proxyLog.Warn("rejected_dangerous_env", slog.String("mcp", p.name), slog.String("var", k))
			continue
		}
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	p.mcpProcess.Env = cmdEnv

	// Create a new process group so grandchild processes (e.g., node spawned by npx,
	// python spawned by uvx) can be killed together. Without this, killing npx leaves
	// the actual MCP server process orphaned under PID 1.
	p.mcpProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Graceful shutdown: send SIGTERM to the entire process group on context cancel.
	// WaitDelay gives the group time to exit after SIGTERM before Go forcibly
	// closes I/O pipes and sends SIGKILL. This prevents shutdown hangs when child
	// processes (e.g., node spawned by npx) inherit stdout/stderr and keep Wait() blocked.
	// See: https://github.com/golang/go/issues/50436
	p.mcpProcess.Cancel = func() error {
		// Kill entire process group (negative PID) so grandchildren die too
		return syscall.Kill(-p.mcpProcess.Process.Pid, syscall.SIGTERM)
	}
	p.mcpProcess.WaitDelay = 3 * time.Second

	p.mcpStdin, err = p.mcpProcess.StdinPipe()
	if err != nil {
		return err
	}
	p.mcpStdout, err = p.mcpProcess.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, _ := p.mcpProcess.StderrPipe()

	if err := p.mcpProcess.Start(); err != nil {
		return err
	}

	proxyLog.Info("mcp_started", slog.String("mcp", p.name), slog.Int("pid", p.mcpProcess.Process.Pid))
	go func() { _, _ = io.Copy(p.logWriter, stderr) }()

	listener, err := net.Listen("unix", p.socketPath)
	if err != nil {
		_ = p.mcpProcess.Process.Kill()
		return err
	}
	p.listener = listener

	// Restrict socket permissions to owner-only to prevent other users
	// from connecting and injecting MCP requests.
	_ = os.Chmod(p.socketPath, 0600)

	proxyLog.Info("socket_listening", slog.String("mcp", p.name), slog.String("path", p.socketPath))

	go p.acceptConnections()
	go p.broadcastResponses()

	p.SetStatus(StatusRunning)
	p.statusMu.Lock()
	p.successSince = time.Now()
	p.statusMu.Unlock()
	return nil
}

// maxClientsPerProxy caps the number of concurrent client connections per MCP
// socket proxy. Each client spawns a goroutine with a scanner buffer, so
// unbounded connections (e.g., from reconnect loops) can leak gigabytes of RAM.
const maxClientsPerProxy = 100

func (p *SocketProxy) acceptConnections() {
	clientCounter := 0
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				// Listener was closed (e.g., MCP process crashed and broadcastResponses
				// closed the listener). Exit to avoid spinning in a tight loop.
				proxyLog.Warn("accept_listener_error", slog.String("mcp", p.name), slog.String("error", err.Error()))
				return
			}
		}

		// Reject new connections if at capacity to prevent unbounded goroutine growth
		p.clientsMu.RLock()
		clientCount := len(p.clients)
		p.clientsMu.RUnlock()
		if clientCount >= maxClientsPerProxy {
			proxyLog.Warn("max_clients_reached", slog.String("mcp", p.name), slog.Int("max", maxClientsPerProxy))
			conn.Close()
			continue
		}

		sessionID := fmt.Sprintf("%s-client-%d", p.name, clientCounter)
		clientCounter++

		p.clientsMu.Lock()
		p.clients[sessionID] = conn
		p.clientsMu.Unlock()

		logging.Aggregate(logging.CompPool, "client_connect", slog.String("mcp", p.name), slog.String("client", sessionID))
		go p.handleClient(sessionID, conn)
	}
}

func (p *SocketProxy) handleClient(sessionID string, conn net.Conn) {
	defer func() {
		// Clean up all idMap entries that belong to this session so in-flight
		// requests for a disconnected client don't linger and accumulate.
		p.idMap.Range(func(k, v interface{}) bool {
			if v.(idMapping).sessionID == sessionID {
				p.idMap.Delete(k)
			}
			return true
		})

		p.clientsMu.Lock()
		delete(p.clients, sessionID)
		p.clientsMu.Unlock()
		conn.Close()
		logging.Aggregate(logging.CompPool, "client_disconnect", slog.String("mcp", p.name), slog.String("client", sessionID))
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024) // 10MB max for large MCP requests
	for scanner.Scan() {
		line := scanner.Bytes()

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		if req.ID != nil {
			// Rewrite the client-supplied ID with a proxy-scoped unique int64.
			// This prevents collisions when multiple sessions send requests with
			// the same ID (e.g., Claude Code always starts at id:1).
			proxyID := p.nextID.Add(1)
			var sentAt time.Time
			if logging.IsDebugEnabled() {
				sentAt = time.Now()
			}
			p.idMap.Store(proxyID, idMapping{
				sessionID:  sessionID,
				originalID: req.ID,
				sentAt:     sentAt,
			})
			req.ID = proxyID
			if rewritten, err := json.Marshal(req); err == nil {
				line = rewritten
			}
		}

		_, _ = p.mcpStdin.Write(line)
		_, _ = p.mcpStdin.Write([]byte("\n"))

		logging.Aggregate(logging.CompPool, "mcp_request",
			slog.String("mcp", p.name),
			slog.String("client", sessionID),
			slog.String("method", req.Method))
	}
}

func (p *SocketProxy) broadcastResponses() {
	scanner := bufio.NewScanner(p.mcpStdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024) // 10MB max for large MCP responses
	for scanner.Scan() {
		line := scanner.Bytes()

		var resp JSONRPCResponse
		if json.Unmarshal(line, &resp) != nil {
			p.broadcastToAll(line)
			continue
		}

		if resp.ID != nil {
			p.routeToClient(resp.ID, line)
		} else {
			p.broadcastToAll(line)
		}
	}

	// Log error when scanner exits
	if err := scanner.Err(); err != nil {
		proxyLog.Warn("broadcast_scanner_error", slog.String("mcp", p.name), slog.String("error", err.Error()))
	} else {
		proxyLog.Info("broadcast_exited", slog.String("mcp", p.name))
	}

	// Mark proxy as failed so health monitor can restart it
	p.SetStatus(StatusFailed)

	// Close all client connections so reconnecting proxies know to retry
	p.closeAllClientsOnFailure()

	// Close listener so new connections fail fast (will be recreated on restart)
	if p.listener != nil {
		p.listener.Close()
	}
}

// closeAllClientsOnFailure closes all client connections when the MCP process dies.
// This signals reconnecting proxies to retry their connection.
func (p *SocketProxy) closeAllClientsOnFailure() {
	p.clientsMu.Lock()
	for sessionID, conn := range p.clients {
		conn.Close()
		proxyLog.Debug("client_closed_on_failure", slog.String("mcp", p.name), slog.String("client", sessionID))
	}
	p.clients = make(map[string]net.Conn)
	p.clientsMu.Unlock()

	// Clear all in-flight ID mappings to prevent stale entries across proxy restarts.
	p.idMap.Clear()
}

func (p *SocketProxy) routeToClient(responseID interface{}, line []byte) {
	// Responses from the MCP process use the proxy-assigned int64 IDs.
	// encoding/json unmarshals JSON numbers into float64 when the target is interface{},
	// so we must normalize to int64 via a type switch before looking up the idMap.
	var proxyKey int64
	switch v := responseID.(type) {
	case float64:
		proxyKey = int64(v)
	case int64:
		proxyKey = v
	case json.Number:
		n, _ := v.Int64()
		proxyKey = n
	default:
		// Non-integer IDs were not proxy-assigned; broadcast to all clients.
		p.broadcastToAll(line)
		return
	}

	val, ok := p.idMap.LoadAndDelete(proxyKey)
	if !ok {
		p.broadcastToAll(line)
		return
	}

	mapping := val.(idMapping)

	// Track round-trip latency (debug mode only)
	if !mapping.sentAt.IsZero() {
		rtt := time.Since(mapping.sentAt)
		logging.Aggregate(logging.CompPool, "mcp_rtt",
			slog.String("mcp", p.name),
			slog.String("client", mapping.sessionID),
			slog.Duration("rtt", rtt))
		if rtt > 1*time.Second {
			proxyLog.Warn("slow_mcp_rtt",
				slog.String("mcp", p.name),
				slog.String("client", mapping.sessionID),
				slog.Duration("rtt", rtt))
		}
	}

	// Restore the original client-supplied ID before forwarding the response.
	var resp JSONRPCResponse
	if err := json.Unmarshal(line, &resp); err == nil {
		resp.ID = mapping.originalID
		if restored, err := json.Marshal(resp); err == nil {
			line = restored
		}
	}

	p.clientsMu.RLock()
	conn, exists := p.clients[mapping.sessionID]
	p.clientsMu.RUnlock()

	if exists {
		_, _ = conn.Write(line)
		_, _ = conn.Write([]byte("\n"))
	}
}

func (p *SocketProxy) broadcastToAll(line []byte) {
	p.clientsMu.RLock()
	defer p.clientsMu.RUnlock()

	for _, conn := range p.clients {
		_, _ = conn.Write(line)
		_, _ = conn.Write([]byte("\n"))
	}
}

func (p *SocketProxy) Stop() error {
	// cancel may be nil for external socket proxies (discovered from another instance)
	if p.cancel != nil {
		p.cancel()
	}

	// Close all client connections first
	p.clientsMu.Lock()
	for sessionID, conn := range p.clients {
		conn.Close()
		proxyLog.Debug("client_closed_on_stop", slog.String("mcp", p.name), slog.String("client", sessionID))
	}
	p.clients = make(map[string]net.Conn)
	p.clientsMu.Unlock()

	// Clear in-flight ID mappings to prevent memory leak on shutdown.
	p.idMap.Clear()

	if p.listener != nil {
		p.listener.Close()
	}

	// Only kill process and remove socket if we OWN it (mcpProcess != nil)
	if p.mcpProcess != nil {
		p.mcpStdin.Close()
		// Context cancel above triggers cmd.Cancel (SIGTERM), then WaitDelay handles
		// escalation to SIGKILL + pipe close after 3s. Add 5s safety net.
		done := make(chan error, 1)
		go func() {
			done <- p.mcpProcess.Wait()
		}()
		select {
		case err := <-done:
			if err != nil {
				proxyLog.Warn("process_exit_error", slog.String("mcp", p.name), slog.String("error", err.Error()))
			}
		case <-time.After(5 * time.Second):
			// Final safety net: force kill entire process group if SIGTERM didn't work
			proxyLog.Warn("process_wait_timeout", slog.String("mcp", p.name))
			_ = syscall.Kill(-p.mcpProcess.Process.Pid, syscall.SIGKILL)
			<-done // Wait must return after Kill
		}
		os.Remove(p.socketPath)
		proxyLog.Info("proxy_stopped", slog.String("mcp", p.name))
	} else {
		// Clean up external socket files on shutdown to prevent stale sockets
		os.Remove(p.socketPath)
		proxyLog.Info("external_socket_disconnected", slog.String("mcp", p.name))
	}

	if p.logWriter != nil {
		p.logWriter.Close()
	}

	p.SetStatus(StatusStopped)
	return nil
}

func (p *SocketProxy) GetSocketPath() string {
	return p.socketPath
}

func (p *SocketProxy) GetClientCount() int {
	p.clientsMu.RLock()
	defer p.clientsMu.RUnlock()
	return len(p.clients)
}

func (p *SocketProxy) HealthCheck() error {
	if p.mcpProcess == nil {
		return fmt.Errorf("process not running")
	}
	if err := p.mcpProcess.Process.Signal(syscall.Signal(0)); err != nil {
		return err
	}
	if _, err := os.Stat(p.socketPath); err != nil {
		return err
	}
	return nil
}
