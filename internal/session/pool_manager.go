package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/mcppool"
	"github.com/asheshgoplani/agent-deck/internal/platform"
)

var (
	poolMgrLog  = logging.ForComponent(logging.CompPool)
	httpPoolLog = logging.ForComponent(logging.CompHTTP)
)

// Global MCP pool instances
var (
	globalPool     *mcppool.Pool
	globalHTTPPool *mcppool.HTTPPool
	globalPoolMu   sync.RWMutex
)

// InitializeGlobalPool creates and starts the global MCP pool
func InitializeGlobalPool(ctx context.Context, config *UserConfig, sessions []*Instance) (*mcppool.Pool, error) {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	poolMgrLog.Info("pool_init_start", slog.Int("session_count", len(sessions)))

	// Return existing pool if already initialized
	if globalPool != nil {
		poolMgrLog.Debug("pool_already_initialized")
		return globalPool, nil
	}

	// Check if pool is enabled
	if !config.MCPPool.Enabled {
		poolMgrLog.Info("pool_disabled")
		return nil, nil // Pool disabled, not an error
	}

	// Check platform compatibility for Unix sockets
	// WSL1 and Windows don't reliably support Unix domain sockets
	detectedPlatform := platform.Detect()
	if !platform.SupportsUnixSockets() {
		poolMgrLog.Info("platform_detected", slog.String("platform", string(detectedPlatform)), slog.Bool("pooling_supported", false))
		poolMgrLog.Info("pool_disabled_platform", slog.String("reason", "unix_sockets_unsupported"))
		if detectedPlatform == platform.PlatformWSL1 {
			poolMgrLog.Info("platform_upgrade_hint", slog.String("tip", "WSL2 supports socket pooling. Run 'wsl --set-version <distro> 2' to upgrade"))
		}
		return nil, nil // Platform doesn't support sockets, not an error
	}

	poolMgrLog.Info("platform_detected", slog.String("platform", string(detectedPlatform)), slog.Bool("pooling_supported", true))
	poolMgrLog.Info("pool_creating")

	// Create pool config
	// FallbackStdio is forced to true for safety (Issue #36):
	// - Pool sockets may not be ready immediately after TUI starts
	// - Instant socket check (no blocking) means fallback is essential
	// - Falling back to stdio is safe - MCPs work, just use more memory
	//
	// Note: The config field fallback_to_stdio is effectively ignored and
	// always treated as true. This ensures session creation never fails
	// due to pool initialization timing.
	poolConfig := &mcppool.PoolConfig{
		Enabled:       config.MCPPool.Enabled,
		PoolAll:       config.MCPPool.PoolAll,
		ExcludeMCPs:   config.MCPPool.ExcludeMCPs,
		PoolMCPs:      config.MCPPool.PoolMCPs,
		FallbackStdio: true, // Always true - see Issue #36
	}

	// Create pool
	pool, err := mcppool.NewPool(ctx, poolConfig)
	if err != nil {
		return nil, err
	}

	// FIRST: Discover existing sockets from another agent-deck instance
	// This allows multiple TUI instances to share the same pool
	discovered := pool.DiscoverExistingSockets()
	if discovered > 0 {
		poolMgrLog.Info("pool_sockets_reused", slog.Int("count", discovered))
	}

	// Get all available MCPs from config.toml
	availableMCPs := GetAvailableMCPs()
	poolMgrLog.Info("pool_mcps_available", slog.Int("count", len(availableMCPs)))

	// When pool_all = true, pool ALL available MCPs (not just those in use)
	// This ensures any MCP can be attached via socket immediately
	startedCount := 0
	skippedCount := 0
	for mcpName, def := range availableMCPs {
		shouldPool := pool.ShouldPool(mcpName)
		poolMgrLog.Debug("pool_mcp_check", slog.String("mcp", mcpName), slog.Bool("should_pool", shouldPool))

		if !shouldPool {
			continue // Excluded or not in pool_mcps list
		}

		// Skip if already running (discovered from another instance)
		if pool.IsRunning(mcpName) {
			poolMgrLog.Debug("pool_mcp_already_running", slog.String("mcp", mcpName))
			skippedCount++
			continue
		}

		// Start socket proxy for this MCP
		poolMgrLog.Info("pool_proxy_starting", slog.String("mcp", mcpName))
		if err := pool.Start(mcpName, def.Command, def.Args, def.Env); err != nil {
			poolMgrLog.Warn("pool_proxy_failed", slog.String("mcp", mcpName), slog.Any("error", err))
		} else {
			poolMgrLog.Info("pool_proxy_started", slog.String("mcp", mcpName))
			startedCount++
		}
	}

	poolMgrLog.Info("pool_init_complete", slog.Int("started", startedCount), slog.Int("reused", skippedCount))

	// Start health monitor for auto-restart of failed proxies
	pool.StartHealthMonitor()

	globalPool = pool

	// Initialize HTTP pool for HTTP/SSE MCPs with auto-start servers
	httpPool := mcppool.NewHTTPPool(ctx)
	httpStarted := 0
	for mcpName, def := range availableMCPs {
		if def.HasAutoStartServer() {
			httpPoolLog.Info("http_server_starting", slog.String("mcp", mcpName))
			timeout := time.Duration(def.Server.GetStartupTimeout()) * time.Millisecond
			healthCheck := def.Server.HealthCheck
			if healthCheck == "" {
				healthCheck = def.URL
			}
			if err := httpPool.Start(mcpName, def.URL, healthCheck, def.Server.Command, def.Server.Args, def.Server.Env, timeout); err != nil {
				httpPoolLog.Warn("http_server_failed", slog.String("mcp", mcpName), slog.Any("error", err))
			} else {
				httpPoolLog.Info("http_server_started", slog.String("mcp", mcpName), slog.String("url", def.URL))
				httpStarted++
			}
		}
	}
	if httpStarted > 0 {
		httpPoolLog.Info("http_pool_init_complete", slog.Int("started", httpStarted))
		httpPool.StartHealthMonitor()
	}
	globalHTTPPool = httpPool

	return pool, nil
}

// GetGlobalPool returns the global socket pool instance (may be nil if disabled)
func GetGlobalPool() *mcppool.Pool {
	globalPoolMu.RLock()
	defer globalPoolMu.RUnlock()
	return globalPool
}

// GetGlobalHTTPPool returns the global HTTP pool instance (may be nil)
func GetGlobalHTTPPool() *mcppool.HTTPPool {
	globalPoolMu.RLock()
	defer globalPoolMu.RUnlock()
	return globalHTTPPool
}

// GetGlobalPoolRunningCount returns the number of running MCPs in the global pool
func GetGlobalPoolRunningCount() int {
	globalPoolMu.RLock()
	defer globalPoolMu.RUnlock()

	if globalPool != nil {
		return globalPool.GetRunningCount()
	}
	return 0
}

// ShutdownGlobalPool stops the global pools if shouldShutdown is true.
// If shouldShutdown is false, it disconnects from the pools but leaves processes running.
func ShutdownGlobalPool(shouldShutdown bool) error {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	// Shutdown socket pool
	if globalPool != nil {
		if shouldShutdown {
			poolMgrLog.Info("pool_shutdown", slog.String("action", "kill"))
			err := globalPool.Shutdown()
			globalPool = nil
			if err != nil {
				return err
			}
		} else {
			// Just disconnect - leave MCPs running for next instance
			poolMgrLog.Info("pool_disconnect", slog.Int("running_count", globalPool.GetRunningCount()))
			globalPool = nil
		}
	}

	// Shutdown HTTP pool
	if globalHTTPPool != nil {
		if shouldShutdown {
			httpPoolLog.Info("http_pool_shutdown", slog.String("action", "kill"))
			err := globalHTTPPool.Shutdown()
			globalHTTPPool = nil
			if err != nil {
				return err
			}
		} else {
			httpPoolLog.Info("http_pool_disconnect", slog.Int("running_count", globalHTTPPool.GetRunningCount()))
			globalHTTPPool = nil
		}
	}

	return nil
}

// StartHTTPServer starts an HTTP MCP server on demand
// This is called when an HTTP MCP with server config is attached to a session
func StartHTTPServer(name string, def *MCPDef) error {
	if !def.HasAutoStartServer() {
		return nil // No server config, nothing to start
	}

	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	// Create HTTP pool if it doesn't exist
	if globalHTTPPool == nil {
		globalHTTPPool = mcppool.NewHTTPPool(context.Background())
		globalHTTPPool.StartHealthMonitor()
	}

	// Check if already running
	if globalHTTPPool.IsRunning(name) {
		httpPoolLog.Debug("http_server_already_running", slog.String("mcp", name))
		return nil
	}

	// Start the server
	timeout := time.Duration(def.Server.GetStartupTimeout()) * time.Millisecond
	healthCheck := def.Server.HealthCheck
	if healthCheck == "" {
		healthCheck = def.URL
	}

	httpPoolLog.Info("http_server_starting", slog.String("mcp", name))
	if err := globalHTTPPool.Start(name, def.URL, healthCheck, def.Server.Command, def.Server.Args, def.Server.Env, timeout); err != nil {
		return err
	}
	httpPoolLog.Info("http_server_started", slog.String("mcp", name), slog.String("url", def.URL))
	return nil
}

// GetHTTPServerStatus returns the status of an HTTP MCP server
func GetHTTPServerStatus(name string) string {
	globalPoolMu.RLock()
	defer globalPoolMu.RUnlock()

	if globalHTTPPool == nil {
		return "not_initialized"
	}

	server := globalHTTPPool.GetServer(name)
	if server == nil {
		return "not_found"
	}

	return server.GetStatus().String()
}
