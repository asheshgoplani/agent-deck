//go:build unix

package agentpaths

import (
	"fmt"
	"os"
	"syscall"
)

func checkPrivateDir(dir string) (tightened bool, err error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is a symlink", dir)
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("%s: owner unknown", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return false, fmt.Errorf("%s is owned by uid %d, not %d", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return false, fmt.Errorf("%s: tighten mode: %w", dir, err)
		}
		return true, nil
	}
	return false, nil
}
