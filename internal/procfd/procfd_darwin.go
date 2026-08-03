//go:build darwin

package procfd

import (
	"errors"
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
// field. The layout test pins these Go assumptions; OpenVnodePaths behavior
// tests verify them against the running libproc/kernel.
const vnodeFDInfoWithPathSize = 1200

type vnodeFDInfoWithPath struct {
	Prefix [vnodeFDInfoWithPathSize - maxPathLen]byte
	Path   [maxPathLen]byte
}

var (
	procPidinfoFn   = procPidinfo
	procPidfdinfoFn = procPidfdinfo
)

func openVnodePaths(pid int) ([]string, error) {
	const fdInfoSize = int(unsafe.Sizeof(procFDInfo{}))

	// Sizing call: with a nil buffer proc_pidinfo returns the byte size of the
	// current fd table.
	n, err := procPidinfoFn(pid, procPIDListFDs, 0, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("procfd: sizing fd list for pid %d: %w", pid, err)
	}
	if n%fdInfoSize != 0 {
		return nil, fmt.Errorf("procfd: fd list size for pid %d is not record-aligned: %d bytes", pid, n)
	}
	// Headroom for fds opened between the sizing call and the fill call.
	fds := make([]procFDInfo, n/fdInfoSize+32)
	bufSize := len(fds) * fdInfoSize
	n, err = procPidinfoFn(pid, procPIDListFDs, 0, unsafe.Pointer(&fds[0]), bufSize)
	if err != nil {
		return nil, fmt.Errorf("procfd: listing fds for pid %d: %w", pid, err)
	}
	if n >= bufSize {
		return nil, fmt.Errorf("procfd: fd list for pid %d may be truncated", pid)
	}
	if n%fdInfoSize != 0 {
		return nil, fmt.Errorf("procfd: fd list for pid %d ends with a partial record: %d bytes", pid, n)
	}
	fds = fds[:n/fdInfoSize]

	var (
		paths    []string
		probeErr error
	)
	for _, fd := range fds {
		if fd.FDType != proxFDTypeVnode {
			continue
		}
		var info vnodeFDInfoWithPath
		size, err := procPidfdinfoFn(pid, int(fd.FD), procPIDFDVnodePathInfo, unsafe.Pointer(&info), int(unsafe.Sizeof(info)))
		if err != nil {
			probeErr = errors.Join(probeErr, fmt.Errorf("procfd: reading vnode fd %d for pid %d: %w", fd.FD, pid, err))
			continue
		}
		if size < int(unsafe.Sizeof(info)) {
			probeErr = errors.Join(probeErr, fmt.Errorf("procfd: short vnode fd %d result for pid %d: got %d bytes, want %d", fd.FD, pid, size, unsafe.Sizeof(info)))
			continue
		}
		path := cString(info.Path[:])
		if path == "" {
			probeErr = errors.Join(probeErr, fmt.Errorf("procfd: empty vnode path for fd %d in pid %d", fd.FD, pid))
			continue
		}
		paths = append(paths, path)
	}
	return paths, probeErr
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
