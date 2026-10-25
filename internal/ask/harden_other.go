//go:build !linux

package ask

import "golang.org/x/sys/unix"

// Harden sets RLIMIT_CORE=0 (no PR_SET_DUMPABLE outside Linux).
func Harden() error {
	return unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0})
}
