//go:build linux

package health

import "os"

// openFDs counts this process's descriptors from /proc/self/fd. The count
// includes the descriptor used to read the directory.
func openFDs() (*int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, true
	}
	n := len(entries)
	return &n, true
}
