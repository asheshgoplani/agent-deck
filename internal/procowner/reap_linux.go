//go:build linux

package procowner

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// pidfdProcess is a PinnedProcess backed by a Linux pidfd.
type pidfdProcess struct {
	fd int
}

// Pin implements PinnedSignaler with pidfd_open(2). The descriptor refers to the
// process that occupies pid at this instant and to nothing else, for as long as
// it is open: once that process exits, pidfd_send_signal fails with ESRCH even
// if the pid has been handed to someone new.
//
// A kernel before 5.3 has no pidfd_open and answers ENOSYS; that is reported
// as ErrUnsupported so Reap knows the host has no handle mechanism (and uses
// the verified raw signal, as on macOS). Every other failure is returned as is,
// and Reap then refuses to signal that member at all.
func (OSSignaler) Pin(pid int) (PinnedProcess, error) {
	if pid <= 1 {
		return nil, fmt.Errorf("%w: refusing to pin pid %d", ErrNoProcess, pid)
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) {
			return nil, fmt.Errorf("%w: pidfd_open: %v", ErrUnsupported, err)
		}
		return nil, err
	}
	return &pidfdProcess{fd: fd}, nil
}

// Signal implements PinnedProcess.
func (p *pidfdProcess) Signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(p.fd, unix.Signal(sig), nil, 0)
}

// Close implements PinnedProcess.
func (p *pidfdProcess) Close() error {
	if p.fd < 0 {
		return nil
	}
	err := unix.Close(p.fd)
	p.fd = -1
	return err
}
