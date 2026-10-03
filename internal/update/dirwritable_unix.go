//go:build !windows

package update

import "syscall"

// dirWritable reports whether this process may create and rename entries
// in dir (access(2) with W_OK).
func dirWritable(dir string) bool {
	return syscall.Access(dir, 0x2) == nil
}
