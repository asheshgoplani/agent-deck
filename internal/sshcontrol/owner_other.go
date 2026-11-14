//go:build !unix

package sshcontrol

import "os"

// ownerUID is unavailable on platforms without POSIX ownership; the directory
// then relies on the platform's own temp-dir ACLs.
func ownerUID(os.FileInfo) (int, bool) { return 0, false }
