package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncAgentConfig_CopiesFiles(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "config.json"), []byte(`{"key":"val"}`), 0o644))

	// Subdirectory should NOT be copied (not in copyDirs).
	require.NoError(t, os.Mkdir(filepath.Join(hostDir, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "subdir", "nested.txt"), []byte("nested"), 0o644))

	mount := AgentConfigMount{hostRel: ".claude"}
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	// File should be copied.
	data, err := os.ReadFile(filepath.Join(sandboxDir, "config.json"))
	require.NoError(t, err)
	require.Equal(t, `{"key":"val"}`, string(data))

	// Subdirectory should NOT be copied.
	_, err = os.Stat(filepath.Join(sandboxDir, "subdir"))
	require.True(t, os.IsNotExist(err))
}

func TestSyncAgentConfig_SkipsEntries(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "keep.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "skip-me.txt"), []byte("skip"), 0o644))

	mount := AgentConfigMount{
		hostRel:     ".claude",
		skipEntries: []string{"skip-me.txt"},
	}
	_, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	sandboxDir := SandboxDir(homeDir, ".claude")
	_, err = os.ReadFile(filepath.Join(sandboxDir, "keep.txt"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(sandboxDir, "skip-me.txt"))
	require.True(t, os.IsNotExist(err))
}

func TestSyncAgentConfig_SeedFilesWriteOnce(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))

	mount := AgentConfigMount{
		hostRel: ".claude",
		seedFiles: map[string]string{
			"seed.json": `{"seeded":true}`,
		},
	}

	// First sync writes the seed file.
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(sandboxDir, "seed.json"))
	require.NoError(t, err)
	require.Equal(t, `{"seeded":true}`, string(data))

	// Simulate container modifying the seed file.
	require.NoError(t, os.WriteFile(filepath.Join(sandboxDir, "seed.json"), []byte("container-modified"), 0o644))

	// Second sync should NOT overwrite the existing seed file.
	_, err = SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err = os.ReadFile(filepath.Join(sandboxDir, "seed.json"))
	require.NoError(t, err)
	require.Equal(t, "container-modified", string(data))
}

func TestSyncAgentConfig_HostOverwritesNonSeedFiles(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "creds.json"), []byte("v1"), 0o644))

	mount := AgentConfigMount{hostRel: ".claude"}

	// First sync.
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(sandboxDir, "creds.json"))
	require.NoError(t, err)
	require.Equal(t, "v1", string(data))

	// Update host file.
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "creds.json"), []byte("v2"), 0o644))

	// Second sync should pick up the change.
	_, err = SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err = os.ReadFile(filepath.Join(sandboxDir, "creds.json"))
	require.NoError(t, err)
	require.Equal(t, "v2", string(data))
}

func TestSyncAgentConfig_CopyDirsRecursive(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	pluginsDir := filepath.Join(hostDir, "plugins")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginsDir, "myplugin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginsDir, "myplugin", "init.js"), []byte("plugin"), 0o644))

	// Also create a directory NOT in copyDirs.
	require.NoError(t, os.MkdirAll(filepath.Join(hostDir, "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "cache", "c.txt"), []byte("c"), 0o644))

	mount := AgentConfigMount{
		hostRel:  ".claude",
		copyDirs: []string{"plugins"},
	}
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(sandboxDir, "plugins", "myplugin", "init.js"))
	require.NoError(t, err)
	require.Equal(t, "plugin", string(data))

	// Unlisted directory should not be copied.
	_, err = os.Stat(filepath.Join(sandboxDir, "cache"))
	require.True(t, os.IsNotExist(err))
}

func TestSyncAgentConfig_FollowsSymlinks(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))

	// Create a real directory INSIDE the host dir and symlink to it.
	realDir := filepath.Join(hostDir, "real-skills")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "skill.yaml"), []byte("skill"), 0o644))
	require.NoError(t, os.Symlink(realDir, filepath.Join(hostDir, "skills")))

	// Also create a symlinked file within the host dir.
	realFile := filepath.Join(hostDir, "real-config.json")
	require.NoError(t, os.WriteFile(realFile, []byte("symlinked"), 0o644))
	require.NoError(t, os.Symlink(realFile, filepath.Join(hostDir, "config.json")))

	mount := AgentConfigMount{
		hostRel:     ".claude",
		copyDirs:    []string{"skills"},
		skipEntries: []string{"sandbox", "real-skills"},
	}
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	// Symlinked directory within host dir should be copied recursively.
	data, err := os.ReadFile(filepath.Join(sandboxDir, "skills", "skill.yaml"))
	require.NoError(t, err)
	require.Equal(t, "skill", string(data))

	// Symlinked file within host dir should be copied as a file.
	data, err = os.ReadFile(filepath.Join(sandboxDir, "config.json"))
	require.NoError(t, err)
	require.Equal(t, "symlinked", string(data))
}

func TestSyncAgentConfig_RejectsExternalSymlinks(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))

	// Create a symlink pointing outside the host dir.
	externalDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(externalDir, "secret.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(externalDir, "secret.txt"), filepath.Join(hostDir, "secret.txt")))

	// Also create a valid local file so we can verify sync still works.
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "local.txt"), []byte("local"), 0o644))

	mount := AgentConfigMount{
		hostRel:     ".claude",
		skipEntries: []string{"sandbox"},
	}
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	// External symlink should be rejected.
	_, err = os.ReadFile(filepath.Join(sandboxDir, "secret.txt"))
	require.Error(t, err)

	// Local file should still be copied.
	data, err := os.ReadFile(filepath.Join(sandboxDir, "local.txt"))
	require.NoError(t, err)
	require.Equal(t, "local", string(data))
}

func TestRefreshAgentConfigs_ReturnsMounts(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// Create a host config dir for claude.
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte("{}"), 0o644))

	bindMounts, homeMounts := RefreshAgentConfigs(homeDir, "")

	// Claude sandbox should have been created as a bind mount.
	require.NotEmpty(t, bindMounts)

	// Home seed file (.claude.json) should be in homeMounts, stored under .home-seeds/.
	require.NotEmpty(t, homeMounts)
	found := false
	for _, m := range homeMounts {
		if m.containerPath == "/root/.claude.json" {
			found = true
			require.Contains(t, m.hostPath, ".home-seeds")
			data, err := os.ReadFile(m.hostPath)
			require.NoError(t, err)
			require.Equal(t, `{"hasCompletedOnboarding":true}`, string(data))
		}
	}
	require.True(t, found)
}

func TestRefreshAgentConfigs_SkipsMissingHostDirs(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	// Don't create any host dirs.
	bindMounts, _ := RefreshAgentConfigs(homeDir, "")

	// All tools should be skipped.
	require.Empty(t, bindMounts)
}

func TestRefreshAgentConfigs_PreservesContainerState(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "host.txt"), []byte("host"), 0o644))

	RefreshAgentConfigs(homeDir, "")

	// Simulate a container writing a file to the sandbox dir.
	sandboxDir := SandboxDir(homeDir, ".claude")
	require.NoError(t, os.WriteFile(filepath.Join(sandboxDir, "container-only.txt"), []byte("runtime"), 0o644))

	// Refresh — container-written files should survive since sandbox is shared.
	RefreshAgentConfigs(homeDir, "")

	// Host file should be updated.
	data, err := os.ReadFile(filepath.Join(sandboxDir, "host.txt"))
	require.NoError(t, err)
	require.Equal(t, "host", string(data))

	// Container-written file should still exist.
	data, err = os.ReadFile(filepath.Join(sandboxDir, "container-only.txt"))
	require.NoError(t, err)
	require.Equal(t, "runtime", string(data))
}

func TestRefreshAgentConfigs_HomeSeedFilesAlwaysRewritten(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()

	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))

	// First sync writes the home seed file under .home-seeds/.
	_, homeMounts := RefreshAgentConfigs(homeDir, "")
	require.NotEmpty(t, homeMounts)

	sandboxDir := SandboxDir(homeDir, ".claude")
	seedPath := filepath.Join(sandboxDir, ".home-seeds", ".claude.json")

	// Simulate the container overwriting the seed file (dropping bootstrap flags).
	require.NoError(t, os.WriteFile(seedPath, []byte(`{"oauthAccount":"x"}`), 0o644))

	// Second sync should overwrite with the original seed content.
	_, homeMounts = RefreshAgentConfigs(homeDir, "")
	require.NotEmpty(t, homeMounts)

	data, err := os.ReadFile(seedPath)
	require.NoError(t, err)
	require.Equal(t, `{"hasCompletedOnboarding":true}`, string(data))
}

func TestSyncAgentConfig_PreserveFiles(t *testing.T) {
	t.Parallel()

	homeDir := t.TempDir()
	hostDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, ".credentials.json"), []byte("host-creds"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte("host-settings"), 0o644))

	mount := AgentConfigMount{
		hostRel:       ".claude",
		preserveFiles: []string{".credentials.json"},
	}

	// First sync copies everything.
	sandboxDir, err := SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(sandboxDir, ".credentials.json"))
	require.NoError(t, err)
	require.Equal(t, "host-creds", string(data))

	// Simulate container modifying the preserved file.
	require.NoError(t, os.WriteFile(filepath.Join(sandboxDir, ".credentials.json"), []byte("container-creds"), 0o644))

	// Update host file — non-preserved file should update.
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "settings.json"), []byte("host-settings-v2"), 0o644))

	// Second sync should NOT overwrite the preserved file.
	_, err = SyncAgentConfig(homeDir, mount)
	require.NoError(t, err)

	data, err = os.ReadFile(filepath.Join(sandboxDir, ".credentials.json"))
	require.NoError(t, err)
	require.Equal(t, "container-creds", string(data))

	// Non-preserved file should be updated.
	data, err = os.ReadFile(filepath.Join(sandboxDir, "settings.json"))
	require.NoError(t, err)
	require.Equal(t, "host-settings-v2", string(data))
}

func TestSandboxDir(t *testing.T) {
	t.Parallel()

	dir := SandboxDir("/home/user", ".claude")
	require.Equal(t, "/home/user/.claude/sandbox", dir)
}

func TestCopyDirRecursive(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dest")

	// Build a small directory tree.
	require.NoError(t, os.MkdirAll(filepath.Join(src, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "top.txt"), []byte("top"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a", "mid.txt"), []byte("mid"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a", "b", "deep.txt"), []byte("deep"), 0o644))

	require.NoError(t, copyDirRecursive(src, dst))

	data, err := os.ReadFile(filepath.Join(dst, "top.txt"))
	require.NoError(t, err)
	require.Equal(t, "top", string(data))

	data, err = os.ReadFile(filepath.Join(dst, "a", "mid.txt"))
	require.NoError(t, err)
	require.Equal(t, "mid", string(data))

	data, err = os.ReadFile(filepath.Join(dst, "a", "b", "deep.txt"))
	require.NoError(t, err)
	require.Equal(t, "deep", string(data))
}

func TestPathWithin(t *testing.T) {
	t.Parallel()

	// Create a real directory so EvalSymlinks works (needed on macOS /var → /private/var).
	parent := t.TempDir()

	t.Run("child inside parent", func(t *testing.T) {
		t.Parallel()
		child := filepath.Join(parent, "sub", "file.txt")
		require.NoError(t, os.MkdirAll(filepath.Dir(child), 0o755))
		require.NoError(t, os.WriteFile(child, []byte("x"), 0o644))
		require.True(t, pathWithin(child, parent))
	})

	t.Run("child equals parent", func(t *testing.T) {
		t.Parallel()
		require.True(t, pathWithin(parent, parent))
	})

	t.Run("child outside parent", func(t *testing.T) {
		t.Parallel()
		other := t.TempDir()
		require.False(t, pathWithin(other, parent))
	})

	t.Run("dotdot-prefixed directory name is valid", func(t *testing.T) {
		t.Parallel()
		// A directory named "..foo" is a legitimate name, not a traversal.
		dotdotDir := filepath.Join(parent, "..foo")
		require.NoError(t, os.MkdirAll(dotdotDir, 0o755))
		require.True(t, pathWithin(dotdotDir, parent))
	})
}

func TestResolveAndValidateSymlink(t *testing.T) {
	t.Parallel()

	t.Run("valid symlink within boundary", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "real.txt")
		require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))
		link := filepath.Join(dir, "link.txt")
		require.NoError(t, os.Symlink(target, link))

		resolved, err := resolveAndValidateSymlink(link, dir)
		require.NoError(t, err)
		// EvalSymlinks may resolve macOS /var → /private/var, so compare
		// the resolved target rather than the raw path.
		wantResolved, resolveErr := filepath.EvalSymlinks(target)
		require.NoError(t, resolveErr)
		require.Equal(t, wantResolved, resolved)
	})

	t.Run("symlink outside boundary", func(t *testing.T) {
		t.Parallel()
		boundary := t.TempDir()
		external := t.TempDir()
		target := filepath.Join(external, "secret.txt")
		require.NoError(t, os.WriteFile(target, []byte("secret"), 0o644))
		link := filepath.Join(boundary, "escape.txt")
		require.NoError(t, os.Symlink(target, link))

		_, err := resolveAndValidateSymlink(link, boundary)
		require.Error(t, err)
		require.Contains(t, err.Error(), "outside boundary")
	})

	t.Run("broken symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		link := filepath.Join(dir, "broken.txt")
		require.NoError(t, os.Symlink("/nonexistent/target", link))

		_, err := resolveAndValidateSymlink(link, dir)
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot resolve")
	})

	t.Run("regular file (no symlink)", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		file := filepath.Join(dir, "regular.txt")
		require.NoError(t, os.WriteFile(file, []byte("data"), 0o644))

		resolved, err := resolveAndValidateSymlink(file, dir)
		require.NoError(t, err)
		// On macOS, TempDir is under /var which symlinks to /private/var.
		// EvalSymlinks resolves this, so compare against the resolved path.
		expected, err := filepath.EvalSymlinks(file)
		require.NoError(t, err)
		require.Equal(t, expected, resolved)
	})
}
