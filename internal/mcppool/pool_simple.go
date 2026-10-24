package mcppool

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var poolLog = logging.ForComponent(logging.CompPool)

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

	if existing, exists := p.proxies[name]; exists {
		// A socket adopted by DiscoverExistingSockets carries no command.
		// Record it now so RestartProxy can respawn the MCP locally if the
		// instance that served the socket goes away.
		if existing.command == "" && existing.mcpProcess == nil {
			existing.command, existing.args, existing.env = command, args, env
		}
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

	// Permanently failed proxies are never considered running
	if proxy.GetStatus() == StatusPermanentlyFailed {
		p.mu.RUnlock()
		return false
	}

	// Double-check: verify the socket is actually alive (not just marked as running)
	if proxy.GetStatus() == StatusRunning {
		if !isSocketAliveCheck(proxy.socketPath) {
			p.mu.RUnlock()
			poolLog.Warn("socket_dead_restart", slog.String("mcp", name))
			// Try to restart the proxy
			if err := p.RestartProxy(name); err != nil {
				poolLog.Error("restart_failed", slog.String("mcp", name), slog.String("error", err.Error()))
				return false
			}
			poolLog.Info("restart_success", slog.String("mcp", name))
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

	// Don't restart permanently failed proxies
	if proxy.GetStatus() == StatusPermanentlyFailed {
		return fmt.Errorf("proxy %s is permanently failed", name)
	}

	// Stop the old proxy (cleanup)
	_ = proxy.Stop()
	delete(p.proxies, name)

	// Remove stale socket
	removeStaleOwnSocket(proxy.socketPath)

	// Create and start new proxy
	newProxy, err := NewSocketProxy(p.ctx, name, proxy.command, proxy.args, proxy.env)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %w", err)
	}

	if err := newProxy.Start(); err != nil {
		// Clean up the failed proxy to avoid leaking its context/goroutines
		_ = newProxy.Stop()
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

	var wg sync.WaitGroup
	for name, proxy := range p.proxies {
		wg.Add(1)
		go func(n string, sp *SocketProxy) {
			defer wg.Done()
			poolLog.Info("proxy_stopping", slog.String("mcp", n))
			_ = sp.Stop()
		}(name, proxy)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		poolLog.Info("all_proxies_stopped")
	case <-time.After(10 * time.Second):
		poolLog.Warn("shutdown_timeout")
	}

	return nil
}

// StartHealthMonitor launches a background goroutine that checks for
// failed proxies every 3 seconds and restarts them automatically.
func (p *Pool) StartHealthMonitor() {
	go func() {
		ticker := time.NewTicker(3 * time.Second)
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
	poolLog.Info("health_monitor_started")
}

func (p *Pool) restartFailedProxies() {
	p.mu.RLock()
	var failedProxies []string
	for name, proxy := range p.proxies {
		// Skip external sockets (we don't own them)
		if proxy.mcpProcess == nil {
			continue
		}
		status := proxy.GetStatus()
		// Skip permanently failed proxies
		if status == StatusPermanentlyFailed {
			continue
		}
		// Reset failure counters for proxies that have been healthy for 5+ minutes
		if status == StatusRunning {
			proxy.statusMu.RLock()
			successSince := proxy.successSince
			proxy.statusMu.RUnlock()
			if !successSince.IsZero() && time.Since(successSince) > 5*time.Minute {
				if proxy.totalFailures > 0 || proxy.restartCount > 0 {
					poolLog.Info("failure_counters_reset", slog.String("mcp", name), slog.Int("prev_failures", proxy.totalFailures))
					proxy.totalFailures = 0
					proxy.restartCount = 0
				}
			}
			continue
		}
		if status == StatusFailed {
			failedProxies = append(failedProxies, name)
		}
	}
	p.mu.RUnlock()

	for _, name := range failedProxies {
		if err := p.RestartProxyWithRateLimit(name); err != nil {
			poolLog.Error("restart_failed", slog.String("mcp", name), slog.String("error", err.Error()))
		}
	}
}

// maxTotalRestartFailures is the maximum number of cumulative failures before
// a proxy is permanently disabled. This prevents infinite restart loops for
// broken MCPs (e.g., removed npm packages) from leaking memory.
const maxTotalRestartFailures = 10

// RestartProxyWithRateLimit restarts a proxy with rate limiting to prevent loops
func (p *Pool) RestartProxyWithRateLimit(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	proxy, exists := p.proxies[name]
	if !exists {
		return fmt.Errorf("proxy %s not found", name)
	}

	// Already permanently failed, nothing to do
	if proxy.GetStatus() == StatusPermanentlyFailed {
		return fmt.Errorf("proxy %s is permanently failed", name)
	}

	// Check if we've exceeded the total failure limit
	if proxy.totalFailures >= maxTotalRestartFailures {
		poolLog.Error("permanently_disabled", slog.String("mcp", name), slog.Int("total_failures", proxy.totalFailures))
		proxy.SetStatus(StatusPermanentlyFailed)
		return fmt.Errorf("proxy %s permanently disabled after %d failures", name, proxy.totalFailures)
	}

	// Rate limit: minimum 5 seconds between restarts, max 3 per minute
	if time.Since(proxy.lastRestart) < 5*time.Second {
		return fmt.Errorf("rate limited: last restart was %v ago", time.Since(proxy.lastRestart))
	}
	if proxy.restartCount >= 3 && time.Since(proxy.lastRestart) < time.Minute {
		return fmt.Errorf("rate limited: %d restarts in last minute", proxy.restartCount)
	}

	poolLog.Info("auto_restart", slog.String("mcp", name), slog.Int("total_failures", proxy.totalFailures), slog.Int("max_failures", maxTotalRestartFailures))

	// Save config before stopping
	command := proxy.command
	args := proxy.args
	env := proxy.env
	prevRestartCount := proxy.restartCount
	prevTotalFailures := proxy.totalFailures

	// Stop and remove old proxy
	_ = proxy.Stop()
	delete(p.proxies, name)
	removeStaleOwnSocket(proxy.socketPath)

	// Create and start new proxy
	newProxy, err := NewSocketProxy(p.ctx, name, command, args, env)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %w", err)
	}

	if err := newProxy.Start(); err != nil {
		// Clean up the failed proxy to avoid leaking its context/goroutines
		_ = newProxy.Stop()

		// Track the failure even though start failed - re-add to map so
		// health monitor can see it and eventually mark it permanently failed
		newProxy.totalFailures = prevTotalFailures + 1
		newProxy.restartCount = prevRestartCount + 1
		newProxy.lastRestart = time.Now()
		if newProxy.totalFailures >= maxTotalRestartFailures {
			poolLog.Error("permanently_disabled", slog.String("mcp", name), slog.Int("total_failures", newProxy.totalFailures))
			newProxy.SetStatus(StatusPermanentlyFailed)
		} else {
			newProxy.SetStatus(StatusFailed)
		}
		p.proxies[name] = newProxy

		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// Track restart history
	newProxy.restartCount = prevRestartCount + 1
	newProxy.totalFailures = prevTotalFailures
	newProxy.lastRestart = time.Now()

	p.proxies[name] = newProxy
	poolLog.Info("restart_complete", slog.String("mcp", name), slog.Int("restart_num", newProxy.restartCount))

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

// GetRunningCount returns the number of running MCP proxies
func (p *Pool) GetRunningCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, proxy := range p.proxies {
		if proxy.GetStatus() == StatusRunning {
			count++
		}
	}
	return count
}

type ProxyInfo struct {
	Name       string
	SocketPath string
	Status     string
	Clients    int
}

// legacyTmpSocketNotice fires once per process when old world-shared
// /tmp/agentdeck-mcp-*.sock files are present: they are no longer adopted.
var legacyTmpSocketNotice sync.Once

// DiscoverExistingSockets scans this user's private pool socket dir (the one
// NewSocketProxy creates sockets in) for live sockets served by another
// agent-deck instance of the same user and registers them so this instance can
// use them too. Only sockets that pass verifyOwnedSocket are adopted; the old
// world-writable /tmp/agentdeck-mcp-*.sock locations are never trusted.
// Returns count of discovered sockets.
func (p *Pool) DiscoverExistingSockets() int {
	warnLegacyTmpSockets()

	sockDir := mcpSocketDir()
	matches, err := filepath.Glob(filepath.Join(sockDir, "mcp-*.sock"))
	if err != nil {
		poolLog.Warn("socket_scan_failed", slog.String("error", err.Error()))
		return 0
	}

	discovered := 0
	for _, socketPath := range matches {
		// Extract MCP name from socket path: <sockDir>/mcp-{name}.sock
		base := filepath.Base(socketPath)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "mcp-"), ".sock")
		if name == "" {
			continue
		}

		// Skip if we already have this MCP
		p.mu.RLock()
		_, exists := p.proxies[name]
		p.mu.RUnlock()
		if exists {
			continue
		}

		if err := verifyOwnedSocket(socketPath); err != nil {
			poolLog.Warn("external_socket_rejected", slog.String("mcp", name), slog.String("error", err.Error()))
			continue
		}

		// Check if socket is alive (owned by another instance)
		if !isSocketAliveCheck(socketPath) {
			poolLog.Debug("stale_socket_removed", slog.String("mcp", name))
			os.Remove(socketPath)
			continue
		}

		// Register the external socket
		if err := p.RegisterExternalSocket(name, socketPath); err != nil {
			poolLog.Warn("external_register_failed", slog.String("mcp", name), slog.String("error", err.Error()))
			continue
		}

		poolLog.Info("external_socket_discovered", slog.String("mcp", name), slog.String("path", socketPath))
		discovered++
	}

	if discovered > 0 {
		poolLog.Info("discovery_complete", slog.Int("count", discovered))
	}
	return discovered
}

// warnLegacyTmpSockets logs, once, that legacy shared-/tmp pool sockets exist
// and are ignored. It never connects to or removes them.
func warnLegacyTmpSockets() {
	legacyTmpSocketNotice.Do(func() {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "agentdeck-mcp-*.sock"))
		if len(matches) > 0 {
			poolLog.Warn("legacy_tmp_sockets_ignored",
				slog.Int("count", len(matches)),
				slog.String("detail", "shared temp-dir pool sockets are no longer adopted; pool sockets live in the per-user socket dir"),
				slog.String("dir", mcpSocketDir()))
		}
	})
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

	// Create a SocketProxy that points to the external socket (no process to manage).
	// nextID and idMap zero values are ready to use without explicit initialization.
	proxy := &SocketProxy{
		name:       name,
		socketPath: socketPath,
		clients:    make(map[string]net.Conn),
		ctx:        p.ctx,
		Status:     StatusRunning, // External socket is alive
		// mcpProcess is nil - we don't own this process
	}

	p.proxies[name] = proxy
	return nil
}
