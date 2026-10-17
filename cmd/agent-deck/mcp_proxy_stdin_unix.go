//go:build darwin || linux

package main

import "golang.org/x/sys/unix"

func mcpProxyStdinClosed() bool {
	// stdin is descriptor 0 for this CLI. HUP is reported even when buffered
	// data remains, unlike a read that may be blocked behind backpressure.
	fds := []unix.PollFd{{Fd: 0}}
	_, err := unix.Poll(fds, 0)
	return err == nil && fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0
}
