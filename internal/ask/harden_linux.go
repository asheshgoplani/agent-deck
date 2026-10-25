package ask

import "golang.org/x/sys/unix"

// Harden keeps a process that holds a value out of core dumps and ptrace by
// other same-uid processes: PR_SET_DUMPABLE=0 and RLIMIT_CORE=0. `ask
// deliver` and `ask answer` call it first.
func Harden() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	return unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}
