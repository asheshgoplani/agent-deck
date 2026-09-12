//go:build windows

package update

import "errors"

// execSelf has no in-place equivalent on Windows (no execve); the caller
// prints the manual instruction instead.
func execSelf(exe string, args, env []string) error {
	return errors.New("in-place restart is not supported on Windows")
}
