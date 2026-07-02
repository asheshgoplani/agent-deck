//go:build darwin

package procfd

import (
	"fmt"
	"syscall"
	"unsafe" // for go:linkname and libproc buffer passing
)

// Constants from <sys/proc_info.h>. This is public, stable ABI (unchanged
// since macOS 10.5); lsof itself is built on the same interface.
const (
	procPIDListFDs         = 1 // PROC_PIDLISTFDS
	procPIDFDVnodePathInfo = 2 // PROC_PIDFDVNODEPATHINFO
	proxFDTypeVnode        = 1 // PROX_FDTYPE_VNODE
	maxPathLen             = 1024
)

// procFDInfo mirrors struct proc_fdinfo.
type procFDInfo struct {
	FD     int32
	FDType uint32
}

// vnode_fdinfowithpath ends with the only field we need: a MAXPATHLEN
// pathname. Keep the unused ABI prefix opaque instead of mirroring every C
// field. Tests pin both the total size and the pathname offset from the SDK.
const vnodeFDInfoWithPathSize = 1200

type vnodeFDInfoWithPath struct {
	Prefix [vnodeFDInfoWithPathSize - maxPathLen]byte
	Path   [maxPathLen]byte
}

func openVnodePaths(pid int) ([]string, error) {
	// Sizing call: with a nil buffer proc_pidinfo returns the byte size of the
	// current fd table.
	n, err := procPidinfo(pid, procPIDListFDs, 0, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("procfd: sizing fd list for pid %d: %w", pid, err)
	}

	const fdInfoSize = int(unsafe.Sizeof(procFDInfo{}))
	// Headroom for fds opened between the sizing call and the fill call.
	fds := make([]procFDInfo, n/fdInfoSize+32)
	n, err = procPidinfo(pid, procPIDListFDs, 0, unsafe.Pointer(&fds[0]), len(fds)*fdInfoSize)
	if err != nil {
		return nil, fmt.Errorf("procfd: listing fds for pid %d: %w", pid, err)
	}
	fds = fds[:n/fdInfoSize]

	var paths []string
	for _, fd := range fds {
		if fd.FDType != proxFDTypeVnode {
			continue
		}
		var info vnodeFDInfoWithPath
		size, err := procPidfdinfo(pid, int(fd.FD), procPIDFDVnodePathInfo, unsafe.Pointer(&info), int(unsafe.Sizeof(info)))
		if err != nil || size < int(unsafe.Sizeof(info)) {
			// The fd closed mid-scan or its path is unavailable; skip it.
			continue
		}
		if path := cString(info.Path[:]); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// int proc_pidinfo(int pid, int flavor, uint64_t arg, void *buffer, int buffersize)
// Returns the number of bytes written (or needed, for a nil buffer); <= 0 means error.
func procPidinfo(pid, flavor int, arg uint64, buf unsafe.Pointer, size int) (int, error) {
	r1, _, errno := syscall_syscall6(libc_proc_pidinfo_trampoline_addr,
		uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(buf), uintptr(size), 0)
	// #nosec G115 -- the libc function returns a C int (32-bit) in a 64-bit
	// register; truncating to int32 recovers the real return value.
	if n := int(int32(r1)); n > 0 {
		return n, nil
	}
	if errno != 0 {
		return 0, errno
	}
	return 0, syscall.EINVAL
}

// int proc_pidfdinfo(int pid, int fd, int flavor, void *buffer, int buffersize)
func procPidfdinfo(pid, fd, flavor int, buf unsafe.Pointer, size int) (int, error) {
	r1, _, errno := syscall_syscall6(libc_proc_pidfdinfo_trampoline_addr,
		uintptr(pid), uintptr(fd), uintptr(flavor), uintptr(buf), uintptr(size), 0)
	// #nosec G115 -- the libc function returns a C int (32-bit) in a 64-bit
	// register; truncating to int32 recovers the real return value.
	if n := int(int32(r1)); n > 0 {
		return n, nil
	}
	if errno != 0 {
		return 0, errno
	}
	return 0, syscall.EINVAL
}

// syscall_syscall6 is the runtime's darwin libc-call gate, pushed into package
// syscall by the runtime and pulled from there by golang.org/x/sys/unix and by
// this package alike. It calls fn (a libc function pointer) with the given
// arguments and returns errno on failure.
//
//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_pidfdinfo proc_pidfdinfo "/usr/lib/libSystem.B.dylib"

var libc_proc_pidinfo_trampoline_addr uintptr
var libc_proc_pidfdinfo_trampoline_addr uintptr
