//go:build !linux && !darwin

package voicestate

import "syscall"

// statCtimeNs is unknown here; the inode still tells rewrites apart.
func statCtimeNs(*syscall.Stat_t) int64 { return 0 }
