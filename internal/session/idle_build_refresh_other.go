//go:build !linux && !darwin

package session

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// fileBuildStat falls back to the modification time where the inode change
// time is not available.
func fileBuildStat(path string) (changed time.Time, id string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, "", err
	}
	return fi.ModTime(), fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano()), nil
}

func detachIdleRefreshChild(*exec.Cmd) {}
