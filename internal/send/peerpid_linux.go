package send

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// peerCredOfFd returns the pid and uid of the process that listens on the
// socket fd is connected to (the credentials the kernel recorded at listen).
func peerCredOfFd(fd int) (pid, uid int, err error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, fmt.Errorf("SO_PEERCRED: %w", err)
	}
	return int(cred.Pid), int(cred.Uid), nil
}
