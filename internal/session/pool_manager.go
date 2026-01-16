package session

import (
	"context"
	"log"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/mcppool"
	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// Global MCP pool instance
var (
	globalPool   *mcppool.Pool
	globalPoolMu sync.RWMutex
)

// InitializeGlobalPool creates and starts the global MCP pool
func InitializeGlobalPool(ctx context.Context, config *UserConfig, sessions []*Instance) (*mcppool.Pool, error) {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	log.Printf("[Pool] InitializeGlobalPool called with %d sessions", len(sessions))

	// Return existing pool if already initialized
	if globalPool != nil {
		log.Printf("[Pool] Pool already initialized, returning existing")
		return globalPool, nil
	}

	// Check if pool is enabled
	if !config.MCPPool.Enabled {
		log.Printf("[Pool] Pool disabled in config")
		return nil, nil // Pool disabled, not an error
	}

	// Check platform compatibility for Unix sockets
	// WSL1 and Windows don't reliably support Unix domain sockets
	detectedPlatform := platform.Detect()
	if !platform.SupportsUnixSockets() {
		log.Printf("[Pool] Platform '%s' detected - MCP socket pooling disabled", detectedPlatform)
		log.Printf("[Pool] MCPs will use stdio mode (each session spawns its own MCP processes)")
		if detectedPlatform == platform.PlatformWSL1 {
			log.Printf("[Pool] Tip: WSL2 supports socket pooling. Run 'wsl --set-version <distro> 2' to upgrade")
		}
		return nil, nil // Platform doesn't support sockets, not an error
	}

	log.Printf("[Pool] Platform '%s' detected - socket pooling supported", detectedPlatform)
	log.Printf("[Pool] Pool enabled, creating pool...")

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
		log.Printf("[Pool] Reusing %d sockets from another agent-deck instance", discovered)
	}

	// Get all available MCPs from config.toml
	availableMCPs := GetAvailableMCPs()
	log.Printf("[Pool] Available MCPs in config: %d", len(availableMCPs))

	// When pool_all = true, pool ALL available MCPs (not just those in use)
	// This ensures any MCP can be attached via socket immediately
	startedCount := 0
	skippedCount := 0
	for mcpName, def := range availableMCPs {
		shouldPool := pool.ShouldPool(mcpName)
		log.Printf("[Pool] MCP '%s' - should pool: %v", mcpName, shouldPool)

		if !shouldPool {
			continue // Excluded or not in pool_mcps list
		}

		// Skip if already running (discovered from another instance)
		if pool.IsRunning(mcpName) {
			log.Printf("[Pool] %s: already running (discovered from another instance), skipping", mcpName)
			skippedCount++
			continue
		}

		// Start socket proxy for this MCP
		log.Printf("[Pool] Starting socket proxy for %s...", mcpName)
		if err := pool.Start(mcpName, def.Command, def.Args, def.Env); err != nil {
			log.Printf("[Pool] ✗ Failed to start socket proxy for %s: %v", mcpName, err)
		} else {
			log.Printf("[Pool] ✓ Socket proxy started: %s", mcpName)
			startedCount++
		}
	}

	log.Printf("[Pool] Started %d socket proxies, reused %d from other instance", startedCount, skippedCount)

	// Start health monitor for auto-restart of failed proxies
	pool.StartHealthMonitor()

	globalPool = pool
	return pool, nil
}

// GetGlobalPool returns the global pool instance (may be nil if disabled)
func GetGlobalPool() *mcppool.Pool {
	globalPoolMu.RLock()
	defer globalPoolMu.RUnlock()
	return globalPool
}

// ShutdownGlobalPool stops the global pool
func ShutdownGlobalPool() error {
	globalPoolMu.Lock()
	defer globalPoolMu.Unlock()

	if globalPool != nil {
		err := globalPool.Shutdown()
		globalPool = nil
		return err
	}

	return nil
}
