//go:build !darwin && !linux

package send

import "errors"

// No portable way to read the listener's pid here, so the peer check fails
// closed and sends stay on tmux.
func peerPIDOfFd(int) (int, error) {
	return 0, errors.New("peer pid unsupported on this platform")
}
