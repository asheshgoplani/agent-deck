package mcppool

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var httpPoolLog = logging.ForComponent(logging.CompHTTP)

// HTTPPool manages a pool of HTTP MCP servers
type HTTPPool struct {
	servers map[string]*HTTPServer
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewHTTPPool creates a new HTTP server pool
func NewHTTPPool(ctx context.Context) *HTTPPool {
	ctx, cancel := context.WithCancel(ctx)
	return &HTTPPool{
		servers: make(map[string]*HTTPServer),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start starts an HTTP server for the given MCP
func (p *HTTPPool) Start(name, url, healthCheckURL, command string, args []string, env map[string]string, startupTimeout time.Duration) error {
	p.mu.Lock()

	// Already exists?
	if existing, exists := p.servers[name]; exists {
		p.mu.Unlock()
		// If already running, nothing to do
		if existing.IsRunning() {
			return nil
		}
		// Otherwise try to restart
		return existing.Start()
	}

	// Create new server
	server := NewHTTPServer(p.ctx, name, url, healthCheckURL, command, args, env, startupTimeout)
	p.servers[name] = server
	p.mu.Unlock()

	// Start it
	return server.Start()
}

// Stop stops an HTTP server
func (p *HTTPPool) Stop(name string) error {
	p.mu.Lock()
	server, exists := p.servers[name]
	if !exists {
		p.mu.Unlock()
		return fmt.Errorf("HTTP server %s not found", name)
	}
	p.mu.Unlock()

	return server.Stop()
}

// IsRunning checks if an HTTP server is running
func (p *HTTPPool) IsRunning(name string) bool {
	p.mu.RLock()
	server, exists := p.servers[name]
	p.mu.RUnlock()

	if !exists {
		return false
	}

	return server.IsRunning()
}

// GetServer returns the HTTP server by name
func (p *HTTPPool) GetServer(name string) *HTTPServer {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.servers[name]
}

// Shutdown stops all HTTP servers
func (p *HTTPPool) Shutdown() error {
	p.cancel()

	p.mu.Lock()
	defer p.mu.Unlock()

	var wg sync.WaitGroup
	for name, server := range p.servers {
		// Only stop servers we started
		if server.StartedByUs() {
			wg.Add(1)
			go func(n string, s *HTTPServer) {
				defer wg.Done()
				httpPoolLog.Info("server_stopping", slog.String("mcp", n))
				_ = s.Stop()
			}(name, server)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		httpPoolLog.Info("all_servers_stopped")
	case <-time.After(10 * time.Second):
		httpPoolLog.Warn("shutdown_timeout")
	}

	return nil
}

// StartHealthMonitor launches a background goroutine that checks for
// failed HTTP servers and restarts them automatically
func (p *HTTPPool) StartHealthMonitor() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.restartFailedServers()
			}
		}
	}()
	httpPoolLog.Info("health_monitor_started", slog.String("interval", "10s"))
}

// restartFailedServers restarts any servers that have failed
func (p *HTTPPool) restartFailedServers() {
	p.mu.RLock()
	var failedServers []string
	for name, server := range p.servers {
		// Only restart servers we started; skip permanently failed
		status := server.GetStatus()
		if status == StatusPermanentlyFailed {
			continue
		}
		if server.StartedByUs() && status == StatusFailed {
			failedServers = append(failedServers, name)
		}
	}
	p.mu.RUnlock()

	for _, name := range failedServers {
		p.mu.RLock()
		server := p.servers[name]
		p.mu.RUnlock()

		if server != nil {
			httpPoolLog.Info("auto_restart", slog.String("mcp", name))
			if err := server.Restart(); err != nil {
				httpPoolLog.Error("restart_failed", slog.String("mcp", name), slog.String("error", err.Error()))
			} else {
				httpPoolLog.Info("restart_success", slog.String("mcp", name))
			}
		}
	}
}

// ListServers returns info about all HTTP servers
func (p *HTTPPool) ListServers() []HTTPServerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	list := make([]HTTPServerInfo, 0, len(p.servers))
	for _, server := range p.servers {
		list = append(list, HTTPServerInfo{
			Name:        server.name,
			URL:         server.url,
			Status:      server.GetStatus().String(),
			StartedByUs: server.StartedByUs(),
		})
	}
	return list
}

// GetRunningCount returns the number of running HTTP servers
func (p *HTTPPool) GetRunningCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, server := range p.servers {
		if server.IsRunning() {
			count++
		}
	}
	return count
}

// RegisterExternal registers an external HTTP server (already running)
func (p *HTTPPool) RegisterExternal(name, url string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.servers[name]; exists {
		return nil // Already registered
	}

	// Create a server entry that points to external URL
	server := NewHTTPServer(p.ctx, name, url, url, "", nil, nil, 0)
	server.mu.Lock()
	server.status = StatusRunning
	server.startedByUs = false
	server.mu.Unlock()

	p.servers[name] = server
	httpPoolLog.Info("external_server_registered", slog.String("mcp", name), slog.String("url", url))
	return nil
}

// HTTPServerInfo provides info about an HTTP server
type HTTPServerInfo struct {
	Name        string
	URL         string
	Status      string
	StartedByUs bool
}
