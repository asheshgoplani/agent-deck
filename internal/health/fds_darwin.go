//go:build darwin

package health

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// procFDInfo matches sys/proc_info.h's struct proc_fdinfo.
type procFDInfo struct {
	FD     int32
	FDType uint32
}

// openFDs counts this process's descriptors with proc_pidinfo(PROC_PIDLISTFDS):
// one syscall, no exec of lsof (#1552), no cgo, no filesystem events.
// os.ReadDir("/dev/fd") cannot be used here: it fails with EBADF while
// lstat-ing an entry, which left open_fds null in every macOS sample (#2427).
func openFDs() (*int, bool) {
	const procInfoCallPIDInfo = 2
	const procPIDListFDs = 1
	const entrySize = unsafe.Sizeof(procFDInfo{})
	pid := uintptr(os.Getpid())
	// A nil buffer asks for the descriptor table's size plus headroom.
	size, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, pid, procPIDListFDs, 0, 0, 0)
	if errno != 0 || size < entrySize {
		return nil, true
	}
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]procFDInfo, size/entrySize)
		n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, pid, procPIDListFDs, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))*entrySize)
		if errno != 0 {
			return nil, true
		}
		if count := int(n / entrySize); count < len(buf) {
			return &count, true
		}
		// A full buffer may be truncated; retry with more room.
		size *= 2
	}
	return nil, true
}
