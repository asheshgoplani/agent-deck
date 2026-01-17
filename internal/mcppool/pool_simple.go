package mcppool

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Pool struct {
	proxies map[string]*SocketProxy
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
	config  *PoolConfig
}

type PoolConfig struct {
	Enabled       bool
	PoolAll       bool
	ExcludeMCPs   []string
	PoolMCPs      []string
	FallbackStdio bool
}

func NewPool(ctx context.Context, config *PoolConfig) (*Pool, error) {
	ctx, cancel := context.WithCancel(ctx)
	return &Pool{
		proxies: make(map[string]*SocketProxy),
		ctx:     ctx,
		cancel:  cancel,
		config:  config,
	}, nil
}

func (p *Pool) Start(name, command string, args []string, env map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.proxies[name]; exists {
		return nil
	}

	proxy, err := NewSocketProxy(p.ctx, name, command, args, env)
	if err != nil {
		return err
	}

	if err := proxy.Start(); err != nil {
		return err
	}

	p.proxies[name] = proxy
	return nil
}

func (p *Pool) ShouldPool(mcpName string) bool {
	if !p.config.Enabled {
		return false
	}

	if p.config.PoolAll {
		for _, excluded := range p.config.ExcludeMCPs {
			if excluded == mcpName {
				return false
			}
		}
		return true
	}

	for _, name := range p.config.PoolMCPs {
		if name == mcpName {
			return true
		}
	}
	return false
}

func (p *Pool) IsRunning(name string) bool {
	p.mu.RLock()
	proxy, exists := p.proxies[name]
	if !exists {
		p.mu.RUnlock()
		return false
	}

	// Double-check: verify the socket is actually alive (not just marked as running)
	if proxy.GetStatus() == StatusRunning {
		if !isSocketAliveCheck(proxy.socketPath) {
			p.mu.RUnlock()
			log.Printf("[Pool] ⚠️ %s: marked running but socket is DEAD - attempting restart", name)
			// Try to restart the proxy
			if err := p.RestartProxy(name); err != nil {
				log.Printf("[Pool] ✗ %s: restart failed: %v", name, err)
				return false
			}
			log.Printf("[Pool] ✓ %s: successfully restarted", name)
			return true
		}
		p.mu.RUnlock()
		return true
	}
	p.mu.RUnlock()
	return false
}

// RestartProxy stops and restarts a proxy that has died
func (p *Pool) RestartProxy(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	proxy, exists := p.proxies[name]
	if !exists {
		return fmt.Errorf("proxy %s not found", name)
	}

	// Stop the old proxy (cleanup)
	_ = proxy.Stop()
	delete(p.proxies, name)

	// Remove stale socket
	os.Remove(proxy.socketPath)

	// Create and start new proxy
	newProxy, err := NewSocketProxy(p.ctx, name, proxy.command, proxy.args, proxy.env)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %w", err)
	}

	if err := newProxy.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	p.proxies[name] = newProxy
	return nil
}

func (p *Pool) GetURL(name string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if proxy, exists := p.proxies[name]; exists {
		return proxy.GetSocketPath()
	}
	return ""
}

func (p *Pool) GetSocketPath(name string) string {
	return p.GetURL(name)
}

// FallbackEnabled returns whether stdio fallback is allowed when pool isn't working
func (p *Pool) FallbackEnabled() bool {
	return p.config.FallbackStdio
}

func (p *Pool) Shutdown() error {
	p.cancel()

	p.mu.Lock()
	defer p.mu.Unlock()

	for name, proxy := range p.proxies {
		log.Printf("Stopping socket proxy: %s", name)
		_ = proxy.Stop()
	}

	return nil
}

// StartHealthMonitor launches a background goroutine that checks for
// failed proxies every 10 seconds and restarts them automatically.
func (p *Pool) StartHealthMonitor() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				p.restartFailedProxies()
			}
		}
	}()
	log.Printf("[Pool] Health monitor started (10s interval)")
}

func (p *Pool) restartFailedProxies() {
	p.mu.RLock()
	var failedProxies []string
	for name, proxy := range p.proxies {
		// Skip external sockets (we don't own them)
		if proxy.mcpProcess == nil {
			continue
		}
		if proxy.GetStatus() == StatusFailed {
			failedProxies = append(failedProxies, name)
		}
	}
	p.mu.RUnlock()

	for _, name := range failedProxies {
		if err := p.RestartProxyWithRateLimit(name); err != nil {
			log.Printf("[Pool] Failed to restart %s: %v", name, err)
		}
	}
}

// RestartProxyWithRateLimit restarts a proxy with rate limiting to prevent loops
func (p *Pool) RestartProxyWithRateLimit(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	proxy, exists := p.proxies[name]
	if !exists {
		return fmt.Errorf("proxy %s not found", name)
	}

	// Rate limit: minimum 5 seconds between restarts, max 3 per minute
	if time.Since(proxy.lastRestart) < 5*time.Second {
		return fmt.Errorf("rate limited: last restart was %v ago", time.Since(proxy.lastRestart))
	}
	if proxy.restartCount >= 3 && time.Since(proxy.lastRestart) < time.Minute {
		return fmt.Errorf("rate limited: %d restarts in last minute", proxy.restartCount)
	}

	log.Printf("[Pool] Auto-restarting failed proxy: %s", name)

	// Save config before stopping
	command := proxy.command
	args := proxy.args
	env := proxy.env
	prevRestartCount := proxy.restartCount

	// Stop and remove old proxy
	_ = proxy.Stop()
	delete(p.proxies, name)
	os.Remove(proxy.socketPath)

	// Create and start new proxy
	newProxy, err := NewSocketProxy(p.ctx, name, command, args, env)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %w", err)
	}

	if err := newProxy.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// Track restart history
	newProxy.restartCount = prevRestartCount + 1
	newProxy.lastRestart = time.Now()

	p.proxies[name] = newProxy
	log.Printf("[Pool] Successfully restarted %s (restart #%d)", name, newProxy.restartCount)

	return nil
}

func (p *Pool) ListServers() []ProxyInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	list := []ProxyInfo{}
	for _, proxy := range p.proxies {
		list = append(list, ProxyInfo{
			Name:       proxy.name,
			SocketPath: proxy.socketPath,
			Status:     proxy.GetStatus().String(),
			Clients:    proxy.GetClientCount(),
		})
	}
	return list
}

type ProxyInfo struct {
	Name       string
	SocketPath string
	Status     string
	Clients    int
}

// DiscoverExistingSockets scans for existing pool sockets owned by another agent-deck instance
// and registers them so this instance can use them too. Returns count of discovered sockets.
func (p *Pool) DiscoverExistingSockets() int {
	pattern := filepath.Join("/tmp", "agentdeck-mcp-*.sock")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		log.Printf("[Pool] Failed to scan for existing sockets: %v", err)
		return 0
	}

	discovered := 0
	for _, socketPath := range matches {
		// Extract MCP name from socket path: /tmp/agentdeck-mcp-{name}.sock
		base := filepath.Base(socketPath)
		if !strings.HasPrefix(base, "agentdeck-mcp-") || !strings.HasSuffix(base, ".sock") {
			continue
		}
		name := strings.TrimPrefix(base, "agentdeck-mcp-")
		name = strings.TrimSuffix(name, ".sock")

		// Skip if we already have this MCP
		p.mu.RLock()
		_, exists := p.proxies[name]
		p.mu.RUnlock()
		if exists {
			continue
		}

		// Check if socket is alive (owned by another instance)
		if !isSocketAliveCheck(socketPath) {
			log.Printf("[Pool] Socket %s exists but not alive, skipping", name)
			continue
		}

		// Register the external socket
		if err := p.RegisterExternalSocket(name, socketPath); err != nil {
			log.Printf("[Pool] Failed to register external socket %s: %v", name, err)
			continue
		}

		log.Printf("[Pool] ✓ Discovered external socket: %s → %s", name, socketPath)
		discovered++
	}

	if discovered > 0 {
		log.Printf("[Pool] Discovered %d existing sockets from another agent-deck instance", discovered)
	}
	return discovered
}

// isSocketAliveCheck checks if a Unix socket exists and is accepting connections
func isSocketAliveCheck(socketPath string) bool {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return false
	}
	conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// RegisterExternalSocket registers an external socket owned by another agent-deck instance.
// This creates a proxy entry that points to the existing socket without starting a new process.
func (p *Pool) RegisterExternalSocket(name, socketPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.proxies[name]; exists {
		return nil // Already registered
	}

	// Create a SocketProxy that points to the external socket (no process to manage)
	proxy := &SocketProxy{
		name:       name,
		socketPath: socketPath,
		clients:    make(map[string]net.Conn),
		requestMap: make(map[interface{}]string),
		ctx:        p.ctx,
		Status:     StatusRunning, // External socket is alive
		// mcpProcess is nil - we don't own this process
	}

	p.proxies[name] = proxy
	return nil
}
