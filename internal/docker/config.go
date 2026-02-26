package docker

import (
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// containerHome is the home directory inside the container.
	// Assumes the default sandbox image runs as root. If a non-root image is
	// used, config mount paths (gitconfig, SSH, agent configs) will need updating.
	containerHome = "/root"

	// containerNamePrefix is the expected prefix for managed containers.
	containerNamePrefix = "agent-deck-"

	// containerWorkDir is the workspace inside the container.
	containerWorkDir = "/workspace"

	// defaultImage is the sandbox image when none is specified.
	// Uses :latest because this is a locally-built image (docker build -t agent-deck-sandbox sandbox/).
	// Users who want reproducibility can pin a custom image via config (sandbox_image = "myimage:v1.2").
	defaultImage = "agent-deck-sandbox:latest"
)

// agentConfigMounts defines all tool config directories and how they are handled.
// Adding a new tool requires only a new entry here — no code changes needed.
var agentConfigMounts = []AgentConfigMount{
	{
		hostRel:         ".claude",
		containerSuffix: ".claude",
		skipEntries:     []string{"sandbox", "projects", ".home-seeds"},
		copyDirs:        []string{"plugins", "skills"},
		homeSeedFiles:   map[string]string{".claude.json": `{"hasCompletedOnboarding":true}`},
		preserveFiles:   []string{".credentials.json", "statsig_user_id"},
		keychainCredential: &keychainEntry{
			service:  "Claude Code-credentials",
			filename: ".credentials.json",
		},
	},
	{
		hostRel:         ".local/share/opencode",
		containerSuffix: ".local/share/opencode",
		skipEntries:     []string{"sandbox"},
	},
	{
		hostRel:         ".codex",
		containerSuffix: ".codex",
		skipEntries:     []string{"sandbox"},
	},
	{
		hostRel:         ".gemini",
		containerSuffix: ".gemini",
		skipEntries:     []string{"sandbox"},
	},
}

// Mount path blocklists — prevent accidental exposure of sensitive host and container paths.
// The agent inside the container has full shell access by design; these blocklists protect
// against misconfiguration (e.g. mounting /etc or the Docker socket), not against the agent itself.

// blockedContainerPaths are exact container paths that must not be overwritten by user mounts.
var blockedContainerPaths = []string{
	"/",
	"/root",
	"/root/.ssh",
}

// blockedContainerPrefixes are container path prefixes that must not be overwritten.
// Covers system, binary, and library directories to prevent subverting tool execution.
var blockedContainerPrefixes = []string{
	"/bin",
	"/etc",
	"/lib",
	"/lib64",
	"/proc",
	"/sbin",
	"/sys",
	"/usr",
}

// blockedHostPaths are host paths that must never be mounted into sandbox containers.
var blockedHostPaths = []string{
	"/var/run/docker.sock",
	"/run/docker.sock",
}

// blockedHostPrefixes are host path prefixes that must never be mounted.
// Blocking these prevents accidental exposure of system config and kernel interfaces.
// Includes /private/etc because macOS resolves /etc → /private/etc via symlinks.
// /var is intentionally excluded — the Docker socket (/var/run/docker.sock) is blocked
// by exact match in blockedHostPaths, and /var/folders is the macOS temp directory.
var blockedHostPrefixes = []string{
	"/etc",
	"/private/etc",
	"/proc",
	"/sys",
}

// keychainEntry describes a macOS Keychain credential to extract.
type keychainEntry struct {
	// service is the Keychain service name (e.g. "Claude Code-credentials").
	service string
	// filename is the target filename inside the sandbox (e.g. ".credentials.json").
	filename string
}

// VolumeMount represents a single bind mount.
type VolumeMount struct {
	hostPath      string
	containerPath string
	readOnly      bool
}

// ContainerConfig holds settings for container creation.
// All fields are unexported to enforce construction via NewContainerConfig and the
// options pattern (WithGitConfig, WithSSH, etc.), preventing partially initialized configs.
type ContainerConfig struct {
	// workingDir inside the container.
	workingDir string

	// containerHome is the home directory inside the container (default: /root).
	// Override with WithContainerHome for non-root images.
	containerHome string

	// volumes are bind mounts (host path → container path).
	volumes []VolumeMount

	// anonymousVolumes are container-only paths (e.g. /workspace/node_modules).
	anonymousVolumes []string

	// environment variables to set in the container.
	environment map[string]string

	// cpuLimit is the CPU quota (e.g. "2.0").
	cpuLimit string

	// memoryLimit is the memory cap (e.g. "4g").
	memoryLimit string
}

// ContainerConfigOption customizes a ContainerConfig during NewContainerConfig.
type ContainerConfigOption func(*ContainerConfig)

// AgentConfigMount declares how a tool's config directory is synced into sandbox containers.
type AgentConfigMount struct {
	// hostRel is the path relative to home (e.g. ".claude").
	hostRel string
	// containerSuffix is the path relative to container home (e.g. ".claude").
	containerSuffix string
	// skipEntries are top-level entry names to skip when copying (not recursive).
	// Only exact matches on direct children of the host dir are excluded.
	skipEntries []string
	// copyDirs are directories to recursively copy into the sandbox.
	copyDirs []string
	// seedFiles are files written only if absent (write-once) to preserve container state (name → content).
	seedFiles map[string]string
	// homeSeedFiles are files to seed at the container home level, outside the config dir (name → content).
	homeSeedFiles map[string]string
	// preserveFiles are filenames that should not be overwritten if they already exist in the sandbox.
	// Unlike seedFiles (which write initial content), preserveFiles protect container-generated state
	// from being overwritten by host-to-sandbox copies (e.g. credentials, history).
	preserveFiles []string
	// keychainCredential is a macOS Keychain credential to extract (nil on Linux).
	keychainCredential *keychainEntry
}

// WithAgentConfigs adds bind mounts from sandbox sync results.
func WithAgentConfigs(bindMounts []VolumeMount, homeMounts []VolumeMount) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		cfg.volumes = append(cfg.volumes, bindMounts...)
		cfg.volumes = append(cfg.volumes, homeMounts...)
	}
}

// DefaultImage returns the default sandbox image name.
func DefaultImage() string {
	return defaultImage
}

// IsManagedContainer returns true if the name matches the agent-deck naming convention.
func IsManagedContainer(name string) bool {
	return len(name) > len(containerNamePrefix) && strings.HasPrefix(name, containerNamePrefix)
}

// AgentConfigMounts returns a shallow clone of all tool config mount definitions.
// Callers receive their own slice and cannot mutate the package-level configuration.
func AgentConfigMounts() []AgentConfigMount {
	return slices.Clone(agentConfigMounts)
}

// WithContainerHome overrides the default container home directory (/root).
// Use this for non-root images where the home directory differs.
func WithContainerHome(home string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if home != "" {
			cfg.containerHome = home
		}
	}
}

// WithGitConfig mounts the host gitconfig file read-only inside the container.
func WithGitConfig(path string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if path == "" {
			return
		}
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      path,
			containerPath: cfg.containerHome + "/.gitconfig",
			readOnly:      true,
		})
	}
}

// WithSSH mounts the host ~/.ssh directory read-only inside the container.
func WithSSH(path string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if path == "" {
			return
		}
		cfg.volumes = append(cfg.volumes, VolumeMount{
			hostPath:      path,
			containerPath: cfg.containerHome + "/.ssh",
			readOnly:      true,
		})
	}
}

// WithVolumeIgnores converts directory names into anonymous volumes at /workspace/<dir>.
// This prevents large host directories (e.g. node_modules) from syncing into the container.
// Entries containing path separators or ".." are rejected to prevent path traversal.
func WithVolumeIgnores(dirs []string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		for _, dir := range dirs {
			if dir == "" || strings.Contains(dir, "..") || strings.ContainsAny(dir, "/\\") {
				continue
			}
			cfg.anonymousVolumes = append(cfg.anonymousVolumes, filepath.Join(containerWorkDir, dir))
		}
	}
}

// WithWorktree mounts the full repository root and adjusts workingDir to the worktree subdirectory.
// repoRoot is the absolute host path to the git repository root.
// relativePath is the worktree path relative to repoRoot.
func WithWorktree(repoRoot string, relativePath string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if repoRoot == "" {
			return
		}
		// Rebuild volumes slice, replacing the project mount with the repo root.
		newVols := make([]VolumeMount, 0, len(cfg.volumes))
		for _, v := range cfg.volumes {
			if v.containerPath == containerWorkDir {
				newVols = append(newVols, VolumeMount{
					hostPath:      repoRoot,
					containerPath: containerWorkDir,
				})
			} else {
				newVols = append(newVols, v)
			}
		}
		cfg.volumes = newVols
		// Adjust working directory to the worktree subdirectory.
		if relativePath != "" {
			cfg.workingDir = filepath.Join(containerWorkDir, relativePath)
		}
	}
}

// WithExtraVolumes adds user-configured bind mounts (host → container path).
// Both paths must be absolute. Host paths are resolved through EvalSymlinks to
// prevent symlink-based blocklist bypass (e.g. /home/user/link → /var/run/docker.sock).
// Host paths in blockedHostPaths and blockedHostPrefixes are rejected.
// Container paths in blockedContainerPaths and blockedContainerPrefixes
// are rejected to prevent overwriting critical paths.
func WithExtraVolumes(volumes map[string]string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		for host, container := range volumes {
			if host == "" || container == "" {
				continue
			}
			cleanHost := filepath.Clean(host)
			if !filepath.IsAbs(cleanHost) {
				continue
			}
			// Resolve symlinks so blocklist checks apply to the real path.
			resolvedHost, err := filepath.EvalSymlinks(cleanHost)
			if err != nil {
				slog.Warn("Skipping extra volume (cannot resolve symlink)", "path", cleanHost, "error", err)
				continue
			}
			cleanContainer := filepath.Clean(container)
			if !filepath.IsAbs(cleanContainer) {
				continue
			}
			// Check both the original clean path and the resolved path against
			// blocklists. On macOS, EvalSymlinks resolves /etc → /private/etc and
			// /var → /private/var, so checking only the resolved path would miss
			// the blocklist. Checking both catches the bypass in either direction.
			if slices.Contains(blockedHostPaths, cleanHost) || slices.Contains(blockedHostPaths, resolvedHost) {
				continue
			}
			if isBlockedPrefix(cleanHost, blockedHostPrefixes) || isBlockedPrefix(resolvedHost, blockedHostPrefixes) {
				continue
			}
			// Block home-relative secret directories.
			base := filepath.Base(resolvedHost)
			if base == ".gnupg" || base == ".aws" || base == ".azure" || base == ".config" {
				continue
			}
			if slices.Contains(blockedContainerPaths, cleanContainer) {
				continue
			}
			if isBlockedPrefix(cleanContainer, blockedContainerPrefixes) {
				continue
			}
			cfg.volumes = append(cfg.volumes, VolumeMount{
				hostPath:      resolvedHost,
				containerPath: cleanContainer,
			})
		}
	}
}

// isBlockedPrefix returns true if path equals or is a child of any blocked prefix.
func isBlockedPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// WithCPULimit sets the CPU quota for the container (e.g. "2.0").
func WithCPULimit(limit string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if limit != "" {
			cfg.cpuLimit = limit
		}
	}
}

// WithMemoryLimit sets the memory cap for the container (e.g. "4g").
func WithMemoryLimit(limit string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if limit != "" {
			cfg.memoryLimit = limit
		}
	}
}

// WithEnvironment merges key=value environment variables into the container config.
// Used for both host-resolved variables (via collectDockerEnvVars) and static values.
func WithEnvironment(env map[string]string) ContainerConfigOption {
	return func(cfg *ContainerConfig) {
		if len(env) == 0 {
			return
		}
		if cfg.environment == nil {
			cfg.environment = make(map[string]string, len(env))
		}
		maps.Copy(cfg.environment, env)
	}
}

// ContainerPath returns the full container path for this mount.
// The home parameter is the container's home directory (e.g. containerHome).
func (m AgentConfigMount) ContainerPath(home string) string {
	return home + "/" + m.containerSuffix
}
