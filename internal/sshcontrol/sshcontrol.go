// Package sshcontrol owns the directory agent-deck tells OpenSSH to keep its
// ControlMaster sockets in.
//
// The directory used to be the fixed, shared /tmp/agent-deck-ssh, created with
// MkdirAll and never checked. Another local user could pre-create it (or plant
// a symlink there) and then own, or squat on, every multiplexed connection the
// deck opened. The directory is now per user (/tmp/agent-deck-ssh-<uid>) and
// is verified on every use: it must be a real directory, not a symlink, owned
// by the current user, with no group or other permission bits. A directory that
// fails the check is never used; the process falls back to a fresh private
// directory created with os.MkdirTemp instead.
//
// The path stays directly under /tmp on purpose: unix socket paths are limited
// to 104 bytes on macOS, and a long $TMPDIR like /var/folders/... would eat
// most of that. The socket itself is named %C (a 40-character hash of the
// local host, remote host, port and user), not %r@%h:%p, so its length does
// not depend on the host name (#2837).
//
// Both internal/session and internal/terminal import this package so a remote
// attach opened from a new terminal window shares the TUI's multiplexed
// connection.
package sshcontrol

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// LegacyDir is the shared directory releases before the per-user directory
// used. It is only ever swept for our own stale sockets, never used for new
// masters.
const LegacyDir = "/tmp/agent-deck-ssh"

// socketSuffix is the per-connection socket name OpenSSH expands. %C is a
// fixed 40-character hash (OpenSSH 6.7+), so long user or host names cannot
// push the socket path past the unix socket limit (#2837). OpenSSH binds
// "<path>.<16 random chars>" first, so the worst case under /tmp is about
// 24 + 41 + 17 bytes, or about 11 more for the MkdirTemp fallback.
const socketSuffix = "/%C"

var log = logging.ForComponent("ssh")

// resolver resolves and verifies the control directory under parent for uid.
// The fallback directory, once created, is kept for the life of the process so
// every ssh invocation of this process shares one set of masters.
type resolver struct {
	parent string
	uid    int
	// owner reads a file's owning uid; a test seam for the foreign-owner
	// case, which an unprivileged test cannot create for real.
	owner func(os.FileInfo) (int, bool)

	mu       sync.Mutex
	fallback string
	warned   bool
}

var (
	defaultResolver = &resolver{parent: defaultParent(), uid: os.Getuid(), owner: ownerUID}
	legacyNotice    sync.Once
)

func defaultParent() string {
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

// dirName is agent-deck-ssh-<uid>, or plain agent-deck-ssh where the platform
// has no numeric uid (os.Getuid returns -1 on Windows).
func dirName(uid int) string {
	if uid < 0 {
		return "agent-deck-ssh"
	}
	return "agent-deck-ssh-" + strconv.Itoa(uid)
}

func (r *resolver) preferred() string {
	return filepath.Join(r.parent, dirName(r.uid))
}

// errUnsafe marks a preferred directory that exists but must not be used.
var errUnsafe = errors.New("unsafe ssh control directory")

// verify creates dir 0700 when missing and checks that it is a private real
// directory owned by uid, tightening the mode when it is ours but looser.
func verify(dir string, uid int, owner func(os.FileInfo) (int, bool)) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", errUnsafe, dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", errUnsafe, dir)
	}
	if got, ok := owner(info); ok && got != uid {
		return fmt.Errorf("%w: %s is owned by uid %d, not %d", errUnsafe, dir, got, uid)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("%w: %s has mode %v and cannot be tightened: %v", errUnsafe, dir, info.Mode().Perm(), err)
		}
	}
	return nil
}

// ensure returns the directory to use, creating it when needed.
func (r *resolver) ensure() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fallback != "" {
		// Re-create the private fallback if something removed it; MkdirTemp
		// made it ours, so the same check applies.
		if err := verify(r.fallback, r.uid, r.owner); err == nil {
			return r.fallback, nil
		}
		r.fallback = ""
	}
	dir := r.preferred()
	err := verify(dir, r.uid, r.owner)
	if err == nil {
		return dir, nil
	}
	if !errors.Is(err, errUnsafe) {
		// Could not create or stat it (parent missing or unwritable). Nobody
		// else can have it either, so hand back the path; ssh reports the
		// failure itself, exactly as before.
		return dir, err
	}
	fallback, mkErr := os.MkdirTemp(r.parent, dirName(r.uid)+"-")
	if mkErr != nil {
		return "", fmt.Errorf("%v; private fallback failed: %w", err, mkErr)
	}
	r.fallback = fallback
	if !r.warned {
		r.warned = true
		log.Warn("ssh_control_dir_unsafe_using_private_fallback",
			slog.String("unsafe_dir", dir),
			slog.String("reason", err.Error()),
			slog.String("fallback_dir", fallback))
	}
	return fallback, nil
}

// Ensure returns the verified ControlMaster socket directory for this user,
// creating it if needed. On error the returned path may be empty.
func Ensure() (string, error) {
	dir, err := defaultResolver.ensure()
	legacyNotice.Do(func() { noticeLegacy(dir) })
	return dir, err
}

// Dir is Ensure without the error, for the many best-effort call sites that
// used to ignore MkdirAll's error. It returns "" only when neither the
// per-user directory nor a private fallback could be made safe.
func Dir() string {
	dir, err := Ensure()
	if err != nil {
		log.Debug("ssh_control_dir_ensure_failed", slog.String("error", err.Error()))
	}
	return dir
}

// ControlPath returns the ssh ControlPath value (dir + "/%C"). When no
// safe directory could be had at all it returns "none": ssh then simply does
// not multiplex, which is strictly better than using an unsafe directory.
func ControlPath() string {
	dir := Dir()
	if dir == "" {
		return "none"
	}
	return dir + socketSuffix
}

// PreferredDir is the per-user directory path without creating or checking
// it, for tooling that only needs to inspect it.
func PreferredDir() string {
	return defaultResolver.preferred()
}

// OwnedByCurrentUser reports whether dir is a real directory (not a symlink)
// owned by the current user. Used to decide whether the legacy shared
// directory may be swept.
func OwnedByCurrentUser(dir string) bool {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	owner, ok := ownerUID(info)
	return !ok || owner == os.Getuid()
}

// noticeLegacy logs once per process that masters now live in the per-user
// directory, so an operator wondering why the first remote call after upgrade
// opened a new connection finds the reason.
func noticeLegacy(dir string) {
	if dir == "" || dir == LegacyDir {
		return
	}
	if _, err := os.Lstat(LegacyDir); err != nil {
		return
	}
	log.Info("ssh_control_dir_moved",
		slog.String("legacy_dir", LegacyDir),
		slog.String("dir", dir))
}
