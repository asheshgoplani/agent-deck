//go:build !windows

package git

import (
	"fmt"
	"os"
	"syscall"
)

// openScriptFileForHashing opens path for reading in a way that cannot hang
// on a FIFO with no writer (or a slow/blocking device): O_NONBLOCK makes the
// open() syscall itself return immediately regardless of whether a writer
// is present, instead of blocking indefinitely as a plain os.Open would.
//
// After opening, the check for "is this actually a regular file" is done
// via fstat on the resulting file descriptor — never a second path-based
// Stat/Lstat, which would just re-open the exact TOCTOU window this exists
// to close (the path could be re-resolved to something else between two
// separate syscalls; fstat on an already-open fd cannot be fooled that way,
// since the fd is bound to whatever inode open() actually returned).
// O_NONBLOCK has no effect on read blocking for a genuine regular file, so
// once this check passes, the returned *os.File reads normally.
//
// O_CLOEXEC is included in the same open() call (not set afterward via a
// separate fcntl, which would leave a window where a concurrently forked
// child of this process could still inherit the descriptor): os.Open always
// opens close-on-exec internally, and dropping to the raw syscall.Open here
// to get O_NONBLOCK must not silently lose that property.
func openScriptFileForHashing(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file (refusing to read a symlink/FIFO/device target, which could hang or misbehave)", path)
	}
	return f, nil
}
