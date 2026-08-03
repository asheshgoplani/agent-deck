// Package procfd lists the filesystem paths a process has open, in-process,
// without spawning external tools.
//
// On macOS it uses the public libproc API (proc_pidinfo / proc_pidfdinfo)
// through libSystem trampolines — the same CGO_ENABLED=0-compatible mechanism
// used by golang.org/x/sys/unix. This avoids spawning the general-purpose lsof
// tool per PID and asks the kernel for paths only for vnode descriptors
// (issue #1552).
//
// Other platforms return ErrUnsupported: Linux callers read /proc/<pid>/fd
// directly and never need this package.
package procfd

import "errors"

// ErrUnsupported is returned by OpenVnodePaths on platforms without a native
// implementation.
var ErrUnsupported = errors.New("procfd: not supported on this platform")

// OpenVnodePaths returns the filesystem paths of the vnode (file-backed) file
// descriptors currently open in pid. A non-nil error means the returned paths
// may be incomplete. Probing a dead PID or another user's process fails; current
// macOS reports EINVAL for both
// (not ESRCH/EPERM as one might expect).
func OpenVnodePaths(pid int) ([]string, error) {
	return openVnodePaths(pid)
}
