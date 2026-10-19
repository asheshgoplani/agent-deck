//go:build linux || darwin

package session

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// fileBuildStat returns path's inode change time (when the file was created
// or last replaced, which installers cannot back-date the way they set mtime)
// and an identity that a chmod or xattr write leaves unchanged.
func fileBuildStat(path string) (changed time.Time, id string, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return time.Time{}, "", err
	}
	id = fmt.Sprintf("%d:%d:%d:%d", st.Dev, st.Ino, st.Size, st.Mtim.Nano())
	return time.Unix(st.Ctim.Unix()), id, nil
}

// detachIdleRefreshChild puts the restart in its own process group, so a
// daemon recycle mid-restart does not kill it between teardown and respawn.
func detachIdleRefreshChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
