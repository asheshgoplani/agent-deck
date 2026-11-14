//go:build unix

package sshcontrol

import (
	"os"
	"syscall"
)

// ownerUID returns the owning uid of info.
func ownerUID(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
