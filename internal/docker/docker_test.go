package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sessionID    string
		sessionTitle string
		want         string
	}{
		{
			name:         "id only when title empty",
			sessionID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			sessionTitle: "",
			want:         "agent-deck-a1b2c3d4",
		},
		{
			name:         "title included",
			sessionID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			sessionTitle: "my-refactor",
			want:         "agent-deck-my-refactor-a1b2c3d4",
		},
		{
			name:         "title with spaces converted to hyphens",
			sessionID:    "a1b2c3d4-e5f6",
			sessionTitle: "auth module",
			want:         "agent-deck-auth-module-a1b2c3d4",
		},
		{
			name:         "title with special chars stripped",
			sessionID:    "a1b2c3d4-e5f6",
			sessionTitle: "fix: bug #42!",
			want:         "agent-deck-fix-bug-42-a1b2c3d4",
		},
		{
			name:         "short id preserved",
			sessionID:    "abc",
			sessionTitle: "test",
			want:         "agent-deck-test-abc",
		},
		{
			name:         "long title truncated",
			sessionID:    "12345678",
			sessionTitle: "this-is-a-very-long-session-title-that-exceeds-the-limit",
			want:         "agent-deck-this-is-a-very-long-session-ti-12345678",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateName(tc.sessionID, tc.sessionTitle)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestNewContainer_DefaultImage(t *testing.T) {
	t.Parallel()

	c := NewContainer("test-container", "")
	require.Equal(t, "test-container", c.Name())
	require.Equal(t, defaultImage, c.image)
}

func TestNewContainer_CustomImage(t *testing.T) {
	t.Parallel()

	c := NewContainer("test-container", "my-image:latest")
	require.Equal(t, "my-image:latest", c.image)
}

func TestFromName(t *testing.T) {
	t.Parallel()

	c := FromName("existing-container")
	require.Equal(t, "existing-container", c.Name())
}

func TestNewContainerConfig_ProjectMount(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/home/user/project")

	// Should have project mount.
	require.Len(t, cfg.volumes, 1)
	require.Equal(t, "/home/user/project", cfg.volumes[0].hostPath)
	require.Equal(t, containerWorkDir, cfg.volumes[0].containerPath)
	require.False(t, cfg.volumes[0].readOnly)
}

func TestNewContainerConfig_ResourceLimits(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project", WithCPULimit("2.0"), WithMemoryLimit("4g"))
	require.Equal(t, "2.0", cfg.cpuLimit)
	require.Equal(t, "4g", cfg.memoryLimit)
}

func TestNewContainerConfig_Environment(t *testing.T) {
	t.Parallel()

	extra := map[string]string{"MY_VAR": "my_value"}
	cfg := NewContainerConfig("/project", WithEnvironment(extra))

	// Always has IS_SANDBOX.
	require.Equal(t, "1", cfg.environment["IS_SANDBOX"])
	// Extra env passed through.
	require.Equal(t, "my_value", cfg.environment["MY_VAR"])
}

func TestWithEnvironment_NilMap(t *testing.T) {
	t.Parallel()

	// WithEnvironment should not panic when applied to a zero-value config.
	cfg := &ContainerConfig{}
	opt := WithEnvironment(map[string]string{"KEY": "val"})
	opt(cfg)
	require.Equal(t, "val", cfg.environment["KEY"])
}

func TestExecPrefix(t *testing.T) {
	t.Parallel()

	c := NewContainer("my-container", "")
	prefix := c.ExecPrefix()
	require.Equal(t, []string{"docker", "exec", "-it", "my-container"}, prefix)
}

func TestDefaultImage(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultImage, DefaultImage())
}

func TestAgentConfigMounts_AllTools(t *testing.T) {
	t.Parallel()

	mounts := AgentConfigMounts()
	require.GreaterOrEqual(t, len(mounts), 4)
	for _, m := range mounts {
		t.Run(m.hostRel, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, m.hostRel)
			require.NotEmpty(t, m.containerSuffix)
			// All tools must skip sandbox to prevent recursive copies.
			require.Contains(t, m.skipEntries, "sandbox")
		})
	}
}

func TestIsManagedContainer(t *testing.T) {
	t.Parallel()

	require.True(t, IsManagedContainer("agent-deck-a1b2c3d4"))
	require.False(t, IsManagedContainer("my-production-container"))
	require.False(t, IsManagedContainer("agent-deck-"))
}

func TestExecPrefixWithEnv(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	env := map[string]string{
		"B_VAR": "b",
		"A_VAR": "a",
	}
	prefix := c.ExecPrefixWithEnv(env)
	// Keys sorted: A_VAR before B_VAR. Values plain (no shell quoting).
	require.Equal(t, []string{
		"docker", "exec", "-it",
		"-e", "A_VAR=a",
		"-e", "B_VAR=b",
		"sandbox",
	}, prefix)
}

func TestExecPrefixWithEnv_Empty(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	prefix := c.ExecPrefixWithEnv(nil)
	require.Equal(t, []string{"docker", "exec", "-it", "sandbox"}, prefix)
}

func TestExecPrefixWithEnv_SpecialChars(t *testing.T) {
	t.Parallel()

	c := NewContainer("sandbox", "")
	env := map[string]string{
		"VAR": `value with "quotes" and $dollar`,
	}
	prefix := c.ExecPrefixWithEnv(env)
	// Values are plain — shell quoting happens once at the wrapIgnoreSuspend boundary.
	require.Equal(t, []string{
		"docker", "exec", "-it",
		"-e", `VAR=value with "quotes" and $dollar`,
		"sandbox",
	}, prefix)
}

func TestNewContainerConfig_GitConfig(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithGitConfig("/home/user/.gitconfig"),
	)

	// Project mount + gitconfig mount.
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/home/user/.gitconfig", cfg.volumes[1].hostPath)
	require.Equal(t, containerHome+"/.gitconfig", cfg.volumes[1].containerPath)
	require.True(t, cfg.volumes[1].readOnly)
}

func TestNewContainerConfig_GitConfig_Empty(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithGitConfig(""),
	)

	// Only the project mount when path is empty.
	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_SSH(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithSSH("/home/user/.ssh"),
	)

	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/home/user/.ssh", cfg.volumes[1].hostPath)
	require.Equal(t, containerHome+"/.ssh", cfg.volumes[1].containerPath)
	require.True(t, cfg.volumes[1].readOnly)
}

func TestNewContainerConfig_VolumeIgnores(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithVolumeIgnores([]string{"node_modules", ".venv"}),
	)

	require.Equal(t, []string{"/workspace/node_modules", "/workspace/.venv"}, cfg.anonymousVolumes)
}

func TestNewContainerConfig_VolumeIgnores_RejectsTraversal(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithVolumeIgnores([]string{"../../etc", "safe", "sub/dir", ".."}),
	)

	// Only "safe" survives — traversal, separators, and ".." are rejected.
	require.Equal(t, []string{"/workspace/safe"}, cfg.anonymousVolumes)
}

func TestNewContainerConfig_ExtraVolumes_BlocksDockerSocket(t *testing.T) {
	t.Parallel()

	safeDir := t.TempDir()
	resolvedSafe, err := filepath.EvalSymlinks(safeDir)
	require.NoError(t, err)

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			"/var/run/docker.sock": "/var/run/docker.sock",
			safeDir:                "/container/data",
		}),
	)

	// Docker socket is blocked; only safeDir is mounted.
	require.Len(t, cfg.volumes, 2) // Project mount + safeDir.
	require.Equal(t, resolvedSafe, cfg.volumes[1].hostPath)
}

func TestNewContainerConfig_ExtraVolumes_BlocksSystemPaths(t *testing.T) {
	t.Parallel()

	systemPaths := []string{"/etc/passwd", "/proc/self", "/sys/kernel"}
	for _, hostPath := range systemPaths {
		cfg := NewContainerConfig("/project",
			WithExtraVolumes(map[string]string{
				hostPath: "/data/target",
			}),
		)
		require.Len(t, cfg.volumes, 1, "expected %s to be blocked", hostPath)
	}

	secretDirs := []string{"/home/user/.gnupg", "/home/user/.aws"}
	for _, hostPath := range secretDirs {
		cfg := NewContainerConfig("/project",
			WithExtraVolumes(map[string]string{
				hostPath: "/root/target",
			}),
		)
		// Either fails EvalSymlinks (non-existent on macOS) or blocked by base name.
		require.Len(t, cfg.volumes, 1, "expected %s to be blocked", hostPath)
	}
}

func TestNewContainerConfig_ExtraVolumes_BlocksContainerPaths(t *testing.T) {
	t.Parallel()

	// Use real directories so EvalSymlinks succeeds on the host side.
	hostDir := t.TempDir()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			hostDir: "/",
		}),
	)

	// Container path "/" is blocked — only project mount should remain.
	require.Len(t, cfg.volumes, 1)

	cfg2 := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			hostDir: "/root",
		}),
	)

	// Container path "/root" is blocked — only project mount should remain.
	require.Len(t, cfg2.volumes, 1)

	cfg3 := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			hostDir: "/safe/path",
		}),
	)

	// "/safe/path" is allowed.
	require.Len(t, cfg3.volumes, 2)
	require.Equal(t, "/safe/path", cfg3.volumes[1].containerPath)
}

func TestNewContainerConfig_IsSandboxNotOverridable(t *testing.T) {
	t.Parallel()

	extra := map[string]string{"IS_SANDBOX": "0"}
	cfg := NewContainerConfig("/project", WithEnvironment(extra))

	// IS_SANDBOX must remain "1" regardless of caller-supplied values.
	require.Equal(t, "1", cfg.environment["IS_SANDBOX"])
}

func TestCreateNilConfig(t *testing.T) {
	t.Parallel()

	c := NewContainer("test", "image:latest")
	_, err := c.Create(t.Context(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil config")
}

func TestNewContainerConfig_ExtraVolumes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	resolvedDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			dir: "/container/data",
		}),
	)

	// Project mount + extra volume. Host path is the resolved (real) path.
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, resolvedDir, cfg.volumes[1].hostPath)
	require.Equal(t, "/container/data", cfg.volumes[1].containerPath)
}

func TestNewContainerConfig_ExtraVolumes_SkipsEmpty(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			"":           "/container/path",
			"/host/path": "",
		}),
	)

	// Only the project mount — empty paths are skipped.
	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_ExtraVolumes_RejectsRelativePaths(t *testing.T) {
	t.Parallel()

	safeDir := t.TempDir()
	resolvedSafe, err := filepath.EvalSymlinks(safeDir)
	require.NoError(t, err)

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			"relative/path":    "/container/data",
			"../../etc/passwd": "/data/passwd",
			safeDir:            "relative/container",
		}),
	)

	// All rejected: relative host paths and relative container paths.
	require.Len(t, cfg.volumes, 1) // Only project mount.

	cfg2 := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			safeDir: "/safe/container",
		}),
	)

	// Absolute→absolute pair survives. Host path is the resolved (real) path.
	require.Len(t, cfg2.volumes, 2)
	require.Equal(t, resolvedSafe, cfg2.volumes[1].hostPath)
	require.Equal(t, "/safe/container", cfg2.volumes[1].containerPath)
}

func TestNewContainerConfig_ExtraVolumes_BlocksContainerPrefixes(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()

	// All container prefixes (/usr, /bin, /sbin, /lib) should be blocked.
	for _, containerPath := range []string{"/usr/local/bin", "/bin/custom", "/sbin/tool", "/lib/x86_64"} {
		cfg := NewContainerConfig("/project",
			WithExtraVolumes(map[string]string{
				hostDir: containerPath,
			}),
		)
		require.Len(t, cfg.volumes, 1, "expected %s to be blocked", containerPath)
	}

	// /workspace/data should be allowed.
	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			hostDir: "/workspace/data",
		}),
	)
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/workspace/data", cfg.volumes[1].containerPath)
}

func TestNewContainerConfig_ExtraVolumes_UsesResolvedPaths(t *testing.T) {
	t.Parallel()

	// Use a real directory so EvalSymlinks succeeds.
	dir := t.TempDir()

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			dir: "/container/./data",
		}),
	)

	// Resolved host path and cleaned container path are used in the mount.
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, "/container/data", cfg.volumes[1].containerPath)
}

func TestNewContainerConfig_ExtraVolumes_ResolvesSymlinks(t *testing.T) {
	t.Parallel()

	// Create a real directory and a symlink pointing to it.
	targetDir := t.TempDir()
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "link")
	require.NoError(t, os.Symlink(targetDir, link))

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			link: "/container/data",
		}),
	)

	// The resolved (real) path should be mounted, not the symlink.
	// On macOS, t.TempDir() returns /var/folders/... which EvalSymlinks resolves
	// to /private/var/folders/..., so compare against the resolved target.
	resolvedTarget, err := filepath.EvalSymlinks(targetDir)
	require.NoError(t, err)
	require.Len(t, cfg.volumes, 2)
	require.Equal(t, resolvedTarget, cfg.volumes[1].hostPath)
}

func TestNewContainerConfig_ExtraVolumes_SymlinkBypassBlocked(t *testing.T) {
	t.Parallel()

	// Create a symlink pointing to a blocked path (/etc is always blocked).
	dir := t.TempDir()
	link := filepath.Join(dir, "sneaky-link")
	require.NoError(t, os.Symlink("/etc", link))

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			link: "/container/etc",
		}),
	)

	// Only the project volume should exist — symlink to blocked path is rejected.
	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_ExtraVolumes_BrokenSymlinkRejected(t *testing.T) {
	t.Parallel()

	// Create a symlink pointing to a non-existent target.
	dir := t.TempDir()
	link := filepath.Join(dir, "broken-link")
	require.NoError(t, os.Symlink("/nonexistent/path", link))

	cfg := NewContainerConfig("/project",
		WithExtraVolumes(map[string]string{
			link: "/container/data",
		}),
	)

	// Only the project volume should exist — broken symlink is rejected.
	require.Len(t, cfg.volumes, 1)
}

func TestNewContainerConfig_EnvironmentValues(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithEnvironment(map[string]string{
			"CUSTOM_VAR": "custom_value",
		}),
	)

	require.Equal(t, "1", cfg.environment["IS_SANDBOX"])
	require.Equal(t, "custom_value", cfg.environment["CUSTOM_VAR"])
}

func TestNewContainerConfig_Worktree(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/worktree/feature-x",
		WithWorktree("/repo-root", "worktrees/feature-x"),
	)

	// Project mount replaced with repo root.
	require.Equal(t, "/repo-root", cfg.volumes[0].hostPath)
	require.Equal(t, containerWorkDir, cfg.volumes[0].containerPath)
	// Working dir adjusted.
	require.Equal(t, "/workspace/worktrees/feature-x", cfg.workingDir)
}

func TestNewContainerConfig_Worktree_NoRelativePath(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithWorktree("/repo-root", ""),
	)

	require.Equal(t, "/repo-root", cfg.volumes[0].hostPath)
	// Working dir unchanged when no relative path.
	require.Equal(t, containerWorkDir, cfg.workingDir)
}

func TestNewContainerConfig_ContainerHome(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project",
		WithContainerHome("/home/app"),
		WithGitConfig("/home/user/.gitconfig"),
		WithSSH("/home/user/.ssh"),
	)

	// gitconfig and SSH should use custom home, not /root.
	require.Len(t, cfg.volumes, 3) // project + gitconfig + ssh.
	require.Equal(t, "/home/app/.gitconfig", cfg.volumes[1].containerPath)
	require.Equal(t, "/home/app/.ssh", cfg.volumes[2].containerPath)
}

func TestNewContainerConfig_ContainerHome_Default(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("/project")
	require.Equal(t, containerHome, cfg.containerHome)
}

func TestNewContainerConfig_EmptyProjectPath(t *testing.T) {
	t.Parallel()

	cfg := NewContainerConfig("")

	// No project mount when path is empty.
	require.Empty(t, cfg.volumes)
}

func TestShellJoinArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "simple alphanumeric args",
			args: []string{"docker", "exec", "-it", "my-container", "bash"},
			want: "docker exec -it my-container bash",
		},
		{
			name: "env value with spaces and semicolons",
			args: []string{
				"docker", "exec", "-it",
				"-e", "TERM=xterm-256color",
				"-e", "API_KEY=sk-ant abc;rm -rf /",
				"my-container", "claude",
			},
			want: `docker exec -it -e TERM=xterm-256color -e 'API_KEY=sk-ant abc;rm -rf /' my-container claude`,
		},
		{
			name: "double quotes and dollar signs",
			args: []string{"-e", `VAR=value with "quotes" and $dollar`},
			want: `-e 'VAR=value with "quotes" and $dollar'`,
		},
		{
			name: "single quotes in value",
			args: []string{"-e", "VAR=it's a value"},
			want: `-e 'VAR=it'"'"'s a value'`,
		},
		{
			name: "empty argument",
			args: []string{"cmd", ""},
			want: "cmd ''",
		},
		{
			name: "backticks and subshell",
			args: []string{"-e", "VAR=$(whoami)"},
			want: `-e 'VAR=$(whoami)'`,
		},
		{
			name: "newlines and tabs",
			args: []string{"-e", "VAR=line1\nline2\ttab"},
			want: `-e 'VAR=line1` + "\n" + `line2` + "\t" + `tab'`,
		},
		{
			name: "path with slashes and dots",
			args: []string{"/usr/bin/docker", "--config=/root/.docker"},
			want: "/usr/bin/docker --config=/root/.docker",
		},
		{
			name: "pipe and redirect chars",
			args: []string{"-e", "CMD=echo hello | cat > /tmp/out"},
			want: `-e 'CMD=echo hello | cat > /tmp/out'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShellJoinArgs(tc.args)
			require.Equal(t, tc.want, got)
		})
	}
}
