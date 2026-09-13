package docker

import (
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SandboxDir returns the shared sandbox directory path for a tool's config.
func SandboxDir(homeDir string, hostRel string) string {
	return filepath.Join(homeDir, hostRel, "sandbox")
}

// keychainReader reads a secret from the platform credential store.
// Overridable in tests so no real Keychain is touched.
var keychainReader = readKeychainSecret

// extractKeychainCredential seeds destPath from the Keychain credential for
// service, but only when no sandbox credential exists yet.
//
// Single-owner rule (#2153): an OAuth refresh token is single-use and rotates
// on every refresh, so every copy of it starts a competing refresh chain and
// whichever side refreshes first invalidates the other. The Keychain entry is
// owned by the host's Claude; the sandbox file is owned by the containers and
// is normally created by a /login inside the sandbox. This opt-in path copies
// the Keychain entry exactly once, to seed a sandbox that has no credential of
// its own, and that single copy still forks the host chain once. From then on
// the sandbox file is canonical: it is never overwritten by a later sync and
// never removed on session teardown. The write is O_EXCL so a start that races
// another one cannot replace the file the winner now owns. An absent Keychain
// entry is not an error.
func extractKeychainCredential(service string, destPath string) error {
	if _, err := os.Stat(destPath); err == nil {
		return nil // Sandbox already owns a refresh chain; do not fork it.
	}
	secret, err := keychainReader(service)
	if err != nil {
		return err
	}
	if secret == "" {
		return nil // No Keychain entry (e.g. API-key auth); nothing to seed.
	}
	slog.Warn("Seeding sandbox credential from the macOS Keychain; this forks the host OAuth refresh chain once (#2153). Prefer /login inside the sandbox.", "path", destPath)
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil // Another sync seeded it first; that copy is canonical.
		}
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	_, writeErr := f.WriteString(secret)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(destPath) // Never leave a truncated credential behind.
		if writeErr != nil {
			return fmt.Errorf("writing %s: %w", destPath, writeErr)
		}
		return fmt.Errorf("closing %s: %w", destPath, closeErr)
	}
	return nil
}

// SyncOption tunes a host-to-sandbox sync.
type SyncOption func(*syncOptions)

type syncOptions struct {
	seedFromKeychain bool
}

// WithKeychainSeed opts in to a one-time copy of the macOS Keychain credential
// into a sandbox that has none. Off by default: every copy of an OAuth refresh
// token forks the host's refresh chain (#2153).
func WithKeychainSeed() SyncOption {
	return func(o *syncOptions) { o.seedFromKeychain = true }
}

// SyncAgentConfig syncs host tool config into a shared sandbox directory.
// Seed files use write-once semantics: they are written only if they don't
// already exist, preserving any state accumulated by the container.
// Top-level files from the host dir are always copied (overwriting previous copies).
func SyncAgentConfig(homeDir string, mount AgentConfigMount, opts ...SyncOption) (string, error) {
	var o syncOptions
	for _, opt := range opts {
		opt(&o)
	}
	hostDir := filepath.Join(homeDir, mount.hostRel)
	sandboxDir := SandboxDir(homeDir, mount.hostRel)

	if err := os.MkdirAll(sandboxDir, 0o700); err != nil {
		return "", fmt.Errorf("creating sandbox dir %s: %w", sandboxDir, err)
	}

	// Write seed files only if they don't already exist (write-once).
	// Sorted iteration for deterministic logging and debugging.
	for _, name := range slices.Sorted(maps.Keys(mount.seedFiles)) {
		content := mount.seedFiles[name]
		dest := filepath.Join(sandboxDir, name)
		if _, err := os.Stat(dest); err == nil {
			continue // Already exists — preserve container-accumulated state.
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			slog.Warn("Writing seed file", "file", name, "error", err)
		}
	}

	// Copy top-level files from host dir (skip directories and skipEntries).
	// Tolerate a missing host dir — seed files above still apply, and the
	// caller (RefreshAgentConfigs) already pre-checks, but be defensive.
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			return sandboxDir, nil
		}
		return "", fmt.Errorf("reading host dir %s: %w", hostDir, err)
	}

	skipSet := make(map[string]bool, len(mount.skipEntries))
	for _, s := range mount.skipEntries {
		skipSet[s] = true
	}

	copyDirSet := make(map[string]bool, len(mount.copyDirs))
	for _, d := range mount.copyDirs {
		copyDirSet[d] = true
	}

	preserveSet := make(map[string]bool, len(mount.preserveFiles))
	for _, f := range mount.preserveFiles {
		preserveSet[f] = true
	}

	for _, entry := range entries {
		name := entry.Name()
		if skipSet[name] {
			continue
		}

		src := filepath.Join(hostDir, name)

		// Resolve symlinks and verify the target is within the host dir tree.
		resolved, err := resolveAndValidateSymlink(src, hostDir)
		if err != nil {
			slog.Warn("Skipping entry", "path", src, "error", err)
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			slog.Warn("Skipping entry (cannot stat)", "path", src, "error", err)
			continue
		}

		if info.IsDir() {
			// Only copy directories that are in copyDirs.
			// Use resolved path to follow validated symlinks.
			if copyDirSet[name] {
				dest := filepath.Join(sandboxDir, name)
				if err := copyDirRecursive(resolved, dest); err != nil {
					slog.Warn("Copying directory", "src", resolved, "error", err)
				}
			}
			continue
		}

		// Skip preserved files that already exist in the sandbox.
		dest := filepath.Join(sandboxDir, name)
		if preserveSet[name] {
			if _, statErr := os.Stat(dest); statErr == nil {
				continue
			}
		}

		// Copy regular file using the resolved path to follow validated symlinks.
		if err := copyFile(resolved, dest); err != nil {
			slog.Warn("Copying file", "src", resolved, "error", err)
		}
	}

	// The sandbox owns its own credential (#2153). Seeding it from the macOS
	// Keychain is opt-in because the copy forks the host's OAuth refresh chain.
	if mount.keychainCredential != nil {
		dest := filepath.Join(sandboxDir, mount.keychainCredential.filename)
		if o.seedFromKeychain {
			if err := extractKeychainCredential(mount.keychainCredential.service, dest); err != nil {
				slog.Warn("Extracting keychain credential", "service", mount.keychainCredential.service, "error", err)
			}
		}
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			slog.Info("Sandbox has no credential file yet; run /login once inside the sandbox session, or pass CLAUDE_CODE_OAUTH_TOKEN via [docker] environment", "path", dest)
		}
	}

	return sandboxDir, nil
}

// RefreshAgentConfigs syncs all tool configs into shared sandbox directories.
// Called on every session start to pick up credential changes and new files.
// On container reuse (exists=true), the existing container keeps its bind mounts
// but gets refreshed sandbox directory contents via the shared host directory.
// cHome is the container's home directory (e.g. "/root" for root images).
// Returns bind mounts for tool config dirs and home-level seed file mounts.
func RefreshAgentConfigs(homeDir string, cHome string, opts ...SyncOption) (bindMounts []VolumeMount, homeMounts []VolumeMount) {
	if cHome == "" {
		cHome = containerHome
	}
	for _, mount := range agentConfigMounts {
		hostDir := filepath.Join(homeDir, mount.hostRel)

		if _, err := os.Stat(hostDir); err != nil {
			slog.Debug("Host config dir missing, skipping", "dir", hostDir)
			continue
		}

		sandboxDir, err := SyncAgentConfig(homeDir, mount, opts...)
		if err != nil {
			slog.Warn("Sandbox sync failed, skipping",
				"tool", mount.hostRel, "error", err)
			continue
		}

		// Bind mount the sandbox dir into the container.
		bindMounts = append(bindMounts, VolumeMount{
			hostPath:      sandboxDir,
			containerPath: mount.ContainerPath(cHome),
		})

		// Write home-level seed files into a dedicated subdirectory to avoid
		// filename collisions with host config files in the sandbox dir.
		// Always written on every sync (not write-once) because agents may
		// overwrite the file during a session, dropping required bootstrap flags.
		if len(mount.homeSeedFiles) > 0 {
			seedDir := filepath.Join(sandboxDir, ".home-seeds")
			if mkErr := os.MkdirAll(seedDir, 0o700); mkErr != nil {
				slog.Warn("Creating home seed dir", "dir", seedDir, "error", mkErr)
			} else {
				for _, name := range slices.Sorted(maps.Keys(mount.homeSeedFiles)) {
					content := mount.homeSeedFiles[name]
					seedPath := filepath.Join(seedDir, name)
					if writeErr := os.WriteFile(seedPath, []byte(content), 0o600); writeErr != nil {
						slog.Warn("Writing home seed file", "file", name, "error", writeErr)
						continue
					}
					homeMounts = append(homeMounts, VolumeMount{
						hostPath:      seedPath,
						containerPath: cHome + "/" + name,
					})
				}
			}
		}
	}

	return bindMounts, homeMounts
}

// resolveAndValidateSymlink resolves a symlink and verifies the target is within
// boundaryDir. Returns the resolved path or an error if the symlink is broken or
// escapes the boundary.
//
// Note: a TOCTOU window exists between resolution and the subsequent copy/stat.
// This is acceptable because the files being synced are user-owned config dirs
// (local-user threat model) — an attacker with write access to those dirs already
// has full control of the user's session.
func resolveAndValidateSymlink(entryPath string, boundaryDir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(entryPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", entryPath, err)
	}
	if !pathWithin(resolved, boundaryDir) {
		return "", fmt.Errorf("symlink %s resolves outside boundary %s to %s", entryPath, boundaryDir, resolved)
	}
	return resolved, nil
}

// copyDirRecursive copies a directory tree from src to dst, following symlinks.
// Broken symlinks are skipped with a warning. Symlinks that resolve outside the
// source tree are rejected to prevent credential exfiltration.
// Directories are created with 0700 to protect credential files in sandbox copies.
func copyDirRecursive(src string, dst string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return fmt.Errorf("resolving source %s: %w", src, err)
	}

	if _, err := os.Stat(absSrc); err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("creating dir %s: %w", dst, err)
	}

	entries, err := os.ReadDir(absSrc)
	if err != nil {
		return fmt.Errorf("reading dir %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(absSrc, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		// Resolve symlinks and verify the target is within the source tree.
		resolved, err := resolveAndValidateSymlink(srcPath, absSrc)
		if err != nil {
			slog.Warn("Skipping entry in recursive copy", "path", srcPath, "error", err)
			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			slog.Warn("Skipping entry in recursive copy", "path", srcPath, "error", err)
			continue
		}

		// Use the resolved path for recursion and copy to ensure symlinks
		// cannot shift the boundary on the next recursion level.
		if info.IsDir() {
			if err := copyDirRecursive(resolved, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(resolved, dstPath); err != nil {
				return fmt.Errorf("copying %s: %w", resolved, err)
			}
		}
	}

	return nil
}

// pathWithin returns true if child is equal to or a descendant of parent.
// Both paths are resolved through EvalSymlinks to handle macOS /var → /private/var.
func pathWithin(child string, parent string) bool {
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		resolvedParent = parent
	}
	resolvedChild, err := filepath.EvalSymlinks(child)
	if err != nil {
		resolvedChild = child
	}
	rel, err := filepath.Rel(resolvedParent, resolvedChild)
	if err != nil {
		return false
	}
	// Check for ".." as a complete path component, not just a prefix.
	// A bare HasPrefix("..") would reject valid directory names like "..foo".
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyFile copies a single file from src to dst atomically using temp+rename.
// This prevents partial writes to credential files if the process is interrupted.
func copyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source %s: %w", src, err)
	}

	// Write to a temp file in the same directory, then rename for atomicity.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", dst, err)
	}
	tmpName := tmp.Name()

	_, copyErr := io.Copy(tmp, in)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("copying to %s: %w", dst, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing temp file for %s: %w", dst, closeErr)
	}

	// Cap permissions at 0700 — sandbox copies may contain credentials, but
	// executable scripts (plugins, hooks) need their execute bit preserved.
	perm := min(info.Mode().Perm(), 0o700)
	if chmodErr := os.Chmod(tmpName, perm); chmodErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("setting permissions on %s: %w", dst, chmodErr)
	}

	if renameErr := os.Rename(tmpName, dst); renameErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming temp to %s: %w", dst, renameErr)
	}

	return nil
}
