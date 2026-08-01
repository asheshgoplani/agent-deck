//go:build unix

package session

import (
	"os"
	"syscall"
)

// sameFilesystem reports whether two entries live on the same device.
//
// os.Root deliberately traverses filesystem boundaries, so the
// no-symlinked-component rule alone does not stop a mount planted at a managed
// skills dir (or at .agent-deck) from redirecting removal and materialization
// onto foreign content. Comparing device numbers rejects the cross-device case
// (FUSE, a separate filesystem, an external volume). Same-device bind mounts
// are NOT detectable this way — that needs Linux statx mount IDs — and are out
// of scope: planting one inside the user's project requires privileges far
// beyond the hostile-repo threat model this guard is for.
func sameFilesystem(a, b os.FileInfo) bool {
	sa, aok := a.Sys().(*syscall.Stat_t)
	sb, bok := b.Sys().(*syscall.Stat_t)
	if !aok || !bok {
		return true
	}
	return sa.Dev == sb.Dev
}
