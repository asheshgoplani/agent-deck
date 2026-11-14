package send

import "golang.org/x/sys/unix"

// peerCredOfFd returns the pid and effective uid of the process that listens
// on the socket fd is connected to. Both must be read: a failure of either
// fails the peer check closed.
func peerCredOfFd(fd int) (pid, uid int, err error) {
	pid, err = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		return 0, 0, err
	}
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return pid, int(cred.Uid), nil
}
