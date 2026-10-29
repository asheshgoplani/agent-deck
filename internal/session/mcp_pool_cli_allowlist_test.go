package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/mcppool"
	"github.com/stretchr/testify/require"
)

// cliPoolTwoHomes models two config homes of one user on one machine. They
// share the pool socket dir, because sockets are keyed by MCP name alone.
// "Home B" runs a real pool serving `served`; "home A" (the config the CLI
// path reads) gets homeAConfig. Returns home B's socket path per name.
func cliPoolTwoHomes(t *testing.T, homeAConfig string, served ...string) map[string]string {
	t.Helper()
	// Short root: unix socket paths are capped at ~108 bytes.
	root, err := os.MkdirTemp("/tmp", "adp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "c"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "d"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "k"))
	t.Setenv("AGENT_DECK_TEST_HOME_ISOLATED", "1")
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")

	ctx, cancel := context.WithCancel(context.Background())
	poolB, err := mcppool.NewPool(ctx, &mcppool.PoolConfig{Enabled: true, PoolAll: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = poolB.Shutdown(); cancel() })
	paths := map[string]string{}
	for _, n := range served {
		require.NoError(t, poolB.Start(n, "cat", nil, nil))
		require.Eventually(t, func() bool { return poolB.IsRunning(n) }, 5*time.Second, 20*time.Millisecond)
		paths[n] = poolB.GetSocketPath(n)
	}

	dir := filepath.Join(root, "c", "agent-deck")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(homeAConfig), 0o600))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return paths
}

// CLI mode (pool == nil: `launch -mcp`, `mcp attach`) still finds a pool
// socket for a name this config pools.
func TestCLIPoolFindsAllowlistedSocket(t *testing.T) {
	paths := cliPoolTwoHomes(t, "[mcp_pool]\nenabled = true\npool_mcps = [\"shared\"]\n", "shared")
	cfg, used := tryPoolSocket(nil, "shared", "user")
	require.True(t, used)
	require.Equal(t, "agent-deck", cfg.Command)
	require.Equal(t, []string{"mcp-proxy", paths["shared"]}, cfg.Args)
}

// A live socket for a name this config did NOT opt in is never attached: it
// is another config's process, with that config's env.
func TestCLIPoolRefusesNonAllowlistedSocket(t *testing.T) {
	cliPoolTwoHomes(t, "[mcp_pool]\nenabled = true\npool_mcps = [\"shared\"]\n", "shared", "other")
	_, used := tryPoolSocket(nil, "other", "user")
	require.False(t, used, "a name outside pool_mcps must fall back to stdio")
}

func TestCLIPoolHonoursExcludeUnderPoolAll(t *testing.T) {
	cliPoolTwoHomes(t, "[mcp_pool]\nenabled = true\npool_all = true\nexclude_mcps = [\"other\"]\n", "shared", "other")
	_, used := tryPoolSocket(nil, "other", "user")
	require.False(t, used, "an excluded name must fall back to stdio")
	_, used = tryPoolSocket(nil, "shared", "user")
	require.True(t, used)
}

func TestCLIPoolDisabledNeverAttaches(t *testing.T) {
	cliPoolTwoHomes(t, "[mcp_pool]\nenabled = false\npool_mcps = [\"shared\"]\n", "shared")
	_, used := tryPoolSocket(nil, "shared", "user")
	require.False(t, used)
}
