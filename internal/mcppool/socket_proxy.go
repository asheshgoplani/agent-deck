package mcppool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

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

	requestMap map[interface{}]string
	requestMu  sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	logFile   string
	logWriter io.WriteCloser

	Status       ServerStatus
	statusMu     sync.RWMutex // Protects Status field
	lastRestart  time.Time    // For rate limiting restarts
	restartCount int          // Track restart attempts
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

func NewSocketProxy(ctx context.Context, name, command string, args []string, env map[string]string) (*SocketProxy, error) {
	ctx, cancel := context.WithCancel(ctx)
	socketPath := filepath.Join("/tmp", fmt.Sprintf("agentdeck-mcp-%s.sock", name))

	// Check if socket already exists and is alive (another agent-deck instance owns it)
	if isSocketAlive(socketPath) {
		log.Printf("[Pool] Socket %s already alive (owned by another agent-deck), reusing", name)
		// Return a proxy that just points to the existing socket (no process to manage)
		return &SocketProxy{
			name:       name,
			socketPath: socketPath,
			command:    command,
			args:       args,
			env:        env,
			clients:    make(map[string]net.Conn),
			requestMap: make(map[interface{}]string),
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
		requestMap: make(map[interface{}]string),
		ctx:        ctx,
		cancel:     cancel,
		Status:     StatusStarting,
	}, nil
}

func (p *SocketProxy) Start() error {
	// If already running (reusing external socket), skip process creation
	if p.GetStatus() == StatusRunning {
		log.Printf("[Pool] %s: Reusing existing socket, no process to start", p.name)
		return nil
	}

	logDir := filepath.Join(os.Getenv("HOME"), ".agent-deck", "logs", "mcppool")
	_ = os.MkdirAll(logDir, 0755)
	p.logFile = filepath.Join(logDir, fmt.Sprintf("%s_socket.log", p.name))

	logWriter, err := os.Create(p.logFile)
	if err != nil {
		return fmt.Errorf("failed to create log: %w", err)
	}
	p.logWriter = logWriter

	p.mcpProcess = exec.CommandContext(p.ctx, p.command, p.args...)
	cmdEnv := os.Environ()
	for k, v := range p.env {
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
	}
	p.mcpProcess.Env = cmdEnv

	// Graceful shutdown: send SIGTERM on context cancel (instead of default SIGKILL).
	// WaitDelay gives the process time to exit after SIGTERM before Go forcibly
	// closes I/O pipes and sends SIGKILL. This prevents shutdown hangs when child
	// processes (e.g., node spawned by npx) inherit stdout/stderr and keep Wait() blocked.
	// See: https://github.com/golang/go/issues/50436
	p.mcpProcess.Cancel = func() error {
		return p.mcpProcess.Process.Signal(syscall.SIGTERM)
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

	log.Printf("Started MCP %s (PID: %d)", p.name, p.mcpProcess.Process.Pid)
	go func() { _, _ = io.Copy(p.logWriter, stderr) }()

	listener, err := net.Listen("unix", p.socketPath)
	if err != nil {
		_ = p.mcpProcess.Process.Kill()
		return err
	}
	p.listener = listener

	log.Printf("Socket proxy %s at: %s", p.name, p.socketPath)

	go p.acceptConnections()
	go p.broadcastResponses()

	p.SetStatus(StatusRunning)
	return nil
}

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
				log.Printf("[%s] acceptConnections: listener error (exiting): %v", p.name, err)
				return
			}
		}

		sessionID := fmt.Sprintf("%s-client-%d", p.name, clientCounter)
		clientCounter++

		p.clientsMu.Lock()
		p.clients[sessionID] = conn
		p.clientsMu.Unlock()

		log.Printf("[%s] Client connected: %s", p.name, sessionID)
		go p.handleClient(sessionID, conn)
	}
}

func (p *SocketProxy) handleClient(sessionID string, conn net.Conn) {
	defer func() {
		p.clientsMu.Lock()
		delete(p.clients, sessionID)
		p.clientsMu.Unlock()
		conn.Close()
		log.Printf("[%s] Client disconnected: %s", p.name, sessionID)
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
			p.requestMu.Lock()
			p.requestMap[req.ID] = sessionID
			p.requestMu.Unlock()
		}

		_, _ = p.mcpStdin.Write(line)
		_, _ = p.mcpStdin.Write([]byte("\n"))
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
		log.Printf("[Pool] %s: broadcastResponses scanner error: %v", p.name, err)
	} else {
		log.Printf("[Pool] %s: broadcastResponses exited (MCP stdout closed)", p.name)
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
		log.Printf("[Pool] %s: closed client %s (MCP failure)", p.name, sessionID)
	}
	p.clients = make(map[string]net.Conn)
	p.clientsMu.Unlock()
}

func (p *SocketProxy) routeToClient(responseID interface{}, line []byte) {
	p.requestMu.Lock()
	sessionID, exists := p.requestMap[responseID]
	if exists {
		delete(p.requestMap, responseID)
	}
	p.requestMu.Unlock()

	if !exists {
		p.broadcastToAll(line)
		return
	}

	p.clientsMu.RLock()
	conn, exists := p.clients[sessionID]
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
		log.Printf("[Pool] %s: Closed client connection: %s", p.name, sessionID)
	}
	p.clients = make(map[string]net.Conn)
	p.clientsMu.Unlock()

	// Clear request map to prevent memory leak
	p.requestMu.Lock()
	p.requestMap = make(map[interface{}]string)
	p.requestMu.Unlock()

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
				log.Printf("[Pool] %s: process exited with: %v", p.name, err)
			}
		case <-time.After(5 * time.Second):
			// Final safety net: force kill if Wait() somehow still blocks
			log.Printf("[Pool] %s: Wait() timed out after 5s, force killing", p.name)
			_ = p.mcpProcess.Process.Kill()
			<-done // Wait must return after Kill
		}
		os.Remove(p.socketPath)
		log.Printf("[Pool] %s: Stopped owned process and removed socket", p.name)
	} else {
		log.Printf("[Pool] %s: Disconnected from external socket (not removing)", p.name)
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
