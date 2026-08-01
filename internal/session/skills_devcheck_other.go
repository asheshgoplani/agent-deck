//go:build !unix

package session

import "os"

// sameFilesystem is a no-op on platforms without a portable device identifier.
func sameFilesystem(a, b os.FileInfo) bool { return true }
