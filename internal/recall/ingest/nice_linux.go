package ingest

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// niceCurrentGoroutine lowers the scheduling priority of the calling
// goroutine's OS thread by delta (a positive nice value, so it never
// outranks the interactive work the load gate exists to protect). It locks
// the goroutine to its thread first: Linux keys setpriority(PRIO_PROCESS, .)
// off the specific thread id, not the whole process, so this only slows the
// background backfill goroutine, never the daemon's other work sharing the
// same process. The lock is never released; per runtime.LockOSThread, a
// goroutine that returns without unlocking takes its (now de-prioritized)
// thread down with it, which is what a one-shot background pass wants.
func niceCurrentGoroutine(delta int) {
	runtime.LockOSThread()
	_ = unix.Setpriority(unix.PRIO_PROCESS, unix.Gettid(), delta)
}
