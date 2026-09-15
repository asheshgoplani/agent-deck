//go:build darwin

package health

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// procTaskInfo matches sys/proc_info.h's fixed-width proc_taskinfo ABI.
type procTaskInfo struct {
	VirtualSize, ResidentSize, TotalUser, TotalSystem, ThreadsUser, ThreadsSystem uint64
	Counters                                                                      [12]int32
}

func residentBytes() *uint64 {
	var info procTaskInfo
	const procInfoCallPIDInfo = 2
	const procPIDTaskInfo = 4
	n, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(os.Getpid()), procPIDTaskInfo, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 || n != unsafe.Sizeof(info) {
		return nil
	}
	return &info.ResidentSize
}
