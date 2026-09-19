package procowner

import "errors"

// Alive reports whether pid names a process that can still run code.
//
// kill(pid, 0) alone is the wrong question for a lock or marker holder: a
// zombie (exited, not yet waited for by its parent) still answers it, so a
// sweep child that died under a re-exec'd parent kept its remote-sweep
// marker and update.lock "held" for the full stale window. This reads the
// process state as well (ps on macOS, /proc on Linux) and counts a zombie
// as dead. When the state cannot be read the signal's answer stands, and
// EPERM (someone else's process) still means it exists.
func Alive(pid int) bool {
	return aliveWith(NewProber(), signalZero, pid)
}

// aliveWith is Alive over an injectable prober and kill(pid, 0).
func aliveWith(prober Prober, signal func(pid int) error, pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := signal(pid); err != nil && !isPermissionDenied(err) {
		return false
	}
	info, err := prober.Inspect(pid)
	switch {
	case err == nil:
		return !info.IsZombie()
	case errors.Is(err, ErrNoProcess):
		return false
	default:
		// Unreadable or unsupported: the process exists as far as the
		// kernel is concerned, and nothing here proves otherwise.
		return true
	}
}
