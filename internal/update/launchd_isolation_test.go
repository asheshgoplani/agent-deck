package update

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// underDir reports whether path is dir or lies inside it.
func underDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(os.PathSeparator))
}

// Every default RebootstrapOptions.fill resolves (the pending marker under
// the cache dir, ~/Library/LaunchAgents) must land in the isolated HOME
// this package's TestMain installed, never under the HOME the binary
// started with or the passwd home. 2026-09-19 review of #2312: the launchd
// tests deleted a pending marker under the real ~/.cache/agent-deck, the
// 2026-06-04 data-loss class.
func TestLaunchdDefaults_NeverResolveUnderRealHome(t *testing.T) {
	isolated := os.Getenv("HOME")
	require.NotEmpty(t, isolated)
	require.NotEqual(t, homeBeforeIsolation, isolated, "TestMain must move HOME off the real one")
	require.Empty(t, os.Getenv(launchdServiceEnv), "tests must not inherit a launchd service label")

	var opts RebootstrapOptions
	require.NoError(t, opts.fill())
	cacheDir, err := getCacheDir()
	require.NoError(t, err)

	forbidden := []string{homeBeforeIsolation}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		forbidden = append(forbidden, u.HomeDir)
	}
	for name, path := range map[string]string{
		"PendingPath":     opts.PendingPath,
		"LaunchAgentsDir": opts.LaunchAgentsDir,
		"cache dir":       cacheDir,
	} {
		require.NotEmpty(t, path, "%s must resolve under the isolated HOME, not stay empty", name)
		require.True(t, underDir(path, isolated), "%s = %q is outside the isolated HOME %q", name, path, isolated)
		for _, home := range forbidden {
			if home == isolated {
				continue
			}
			require.False(t, underDir(path, home), "%s = %q resolves under the real home %q", name, path, home)
		}
	}
	require.Equal(t, "", opts.ServiceLabel, "ServiceLabel must not come from the host environment")
}
