//go:build !unix

package agentpaths

import (
	"fmt"
	"os"
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
	return false, nil
}
