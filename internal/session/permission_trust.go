package session

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// Ownership and writability checks for files agent-deck trusts as config
// (config.toml, dir-local .agent-deck/config.toml) and the one-time
// tightening of the state root (LOC-8, LOC-11).

// permTrustUID is the uid a trusted file must be owned by. A seam so tests
// can simulate a file owned by someone else without root.
var permTrustUID = os.Getuid

// isUserPrivateGroup reports whether gid is uid's user-private group: the
// user's primary group, named after the user (the Debian/Ubuntu
// USERGROUPS_ENAB layout behind umask 002). Group write on such a group
// gives nobody else access, so it is not treated as group-writable. A seam
// for tests.
//
// A failed lookup fails closed (the group counts as shared): release builds
// are CGO_ENABLED=0, so for an sssd/LDAP account os/user cannot read the
// passwd/group entry, and there the primary group is often a shared one
// (domain users). The skip is shown to the user (config show --effective,
// the TUI footer), and chmod g-w makes the file apply again.
var isUserPrivateGroup = func(gid uint32, uid int) bool {
	u, err := lookupUserByID(strconv.Itoa(uid))
	if err != nil || u.Gid != strconv.FormatUint(uint64(gid), 10) {
		return false
	}
	g, err := lookupGroupByID(u.Gid)
	return err == nil && g.Name == u.Username
}

// Seams for isUserPrivateGroup's lookups.
var (
	lookupUserByID  = user.LookupId
	lookupGroupByID = user.LookupGroupId
)

// foreignWritableReason returns "" when info is owned by the current user
// and nobody else can write it, otherwise a short reason naming why not.
func foreignWritableReason(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "owner unknown"
	}
	uid := permTrustUID()
	if int(st.Uid) != uid {
		return fmt.Sprintf("owned by uid %d, not %d", st.Uid, uid)
	}
	perm := info.Mode().Perm()
	if perm&0o002 != 0 {
		return fmt.Sprintf("world-writable (mode %#o)", perm)
	}
	if perm&0o020 != 0 && !isUserPrivateGroup(st.Gid, uid) {
		return fmt.Sprintf("group-writable (mode %#o)", perm)
	}
	return ""
}

var (
	permWarnMu     sync.Mutex
	permWarnWriter io.Writer
	permWarned     = map[string]bool{}
)

// SetPermissionWarningWriter sets where permission warnings are printed in
// addition to the debug log, and returns the previous writer. The CLI points
// it at stderr; the TUI leaves it nil so nothing is written over the screen.
func SetPermissionWarningWriter(w io.Writer) io.Writer {
	permWarnMu.Lock()
	defer permWarnMu.Unlock()
	prev := permWarnWriter
	permWarnWriter = w
	return prev
}

// warnPermissionOnce logs msg once per key per process, and prints it to the
// permission warning writer when one is set.
func warnPermissionOnce(key, event, path, reason, msg string) {
	permWarnMu.Lock()
	if permWarned[key] {
		permWarnMu.Unlock()
		return
	}
	permWarned[key] = true
	w := permWarnWriter
	permWarnMu.Unlock()

	sessionLog.Warn(event, slog.String("path", path), slog.String("reason", reason))
	if w != nil {
		_, _ = fmt.Fprintf(w, "WARNING: %s\n", msg)
	}
}

// warnUserConfigPerms warns (never refuses) when config.toml could have been
// written by someone other than the user.
func warnUserConfigPerms(path string, info os.FileInfo) {
	reason := foreignWritableReason(info)
	if reason == "" {
		return
	}
	warnPermissionOnce("config:"+path+":"+reason, "user_config_permissions_unsafe", path, reason,
		fmt.Sprintf("%s is %s; anyone who can write it controls the commands agent-deck runs. Fix with: chmod go-w %q", path, reason, path))
}

// dirLocalConfigUntrustedReason returns why the dir-local config at path
// must not be applied, or "" when it and its .agent-deck directory are owned
// by the user and not writable by anyone else.
func dirLocalConfigUntrustedReason(path string) string {
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(p)
		if err != nil {
			return err.Error()
		}
		if reason := foreignWritableReason(info); reason != "" {
			return p + " is " + reason
		}
	}
	return ""
}

var stateRootOnce = &sync.Once{}

// EnsureStateRootPrivate tightens the agent-deck state roots to 0700 once
// per process when they exist, are owned by the user and have group/other
// bits. Both the XDG data dir and the legacy ~/.agent-deck are covered, not
// only the one GetAgentDeckDir picks: in a diverged layout the profiles can
// live in the other one (see SelectStoreRoot). A root is never created here;
// the writers that create it use 0700.
func EnsureStateRootPrivate(w io.Writer) {
	stateRootOnce.Do(func() {
		for _, root := range stateRootCandidates() {
			tightenStateRoot(root, w)
		}
	})
}

// stateRootCandidates returns the distinct state roots to keep private:
// GetAgentDeckDir, the XDG data dir and the legacy dir. A seam for tests.
var stateRootCandidates = func() []string {
	var roots []string
	seen := map[string]bool{}
	for _, get := range []func() (string, error){GetAgentDeckDir, agentpaths.DataDir, agentpaths.LegacyDir} {
		root, err := get()
		if err != nil || root == "" {
			continue
		}
		key := filepath.Clean(root)
		if resolved, err := filepath.EvalSymlinks(key); err == nil {
			key = resolved
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		roots = append(roots, root)
	}
	return roots
}

// tightenStateRoot chmods root to 0700 when it is a directory owned by the
// current user with any group/other permission bit, and reports whether it
// did. A root owned by someone else is left as it is.
func tightenStateRoot(root string, w io.Writer) bool {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != permTrustUID() {
		return false
	}
	if err := os.Chmod(root, 0o700); err != nil {
		sessionLog.Warn("state_root_chmod_failed", slog.String("path", root), slog.String("error", err.Error()))
		return false
	}
	sessionLog.Warn("state_root_permissions_tightened", slog.String("path", root),
		slog.String("old_mode", fmt.Sprintf("%#o", perm)), slog.String("new_mode", "0700"))
	if w != nil {
		_, _ = fmt.Fprintf(w, "agent-deck: tightened permissions on %s from %#o to 0700 (agent-deck state is private to your user)\n", root, perm)
	}
	return true
}
