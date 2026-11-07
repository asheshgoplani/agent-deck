package send

import "golang.org/x/sys/unix"

func peerPIDOfFd(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
