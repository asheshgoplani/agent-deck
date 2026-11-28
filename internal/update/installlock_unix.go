//go:build !windows

package update

import (
	"os"
	"syscall"
)

// flockExclusiveNB takes a non-blocking exclusive flock on f.
// ok=false with a nil error means another process holds it.
func flockExclusiveNB(f *os.File) (ok bool, err error) {
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch err {
		case nil:
			return true, nil
		case syscall.EINTR:
			continue
		case syscall.EWOULDBLOCK:
			return false, nil
		default:
			return false, err
		}
	}
}

func flockRelease(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
