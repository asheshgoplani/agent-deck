//go:build !darwin && !linux

package send

import "errors"

// No portable way to read the listener's credentials here, so the peer check
// fails closed and sends stay on tmux.
func peerCredOfFd(int) (pid, uid int, err error) {
	return 0, 0, errors.New("peer credentials unsupported on this platform")
}
