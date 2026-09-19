//go:build windows

package procowner

import "os"

// signalZero has no kill(0) on Windows; FindProcess opens a handle, which
// fails when the pid is gone.
func signalZero(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = p.Release()
	return nil
}

func isPermissionDenied(err error) bool { return os.IsPermission(err) }
