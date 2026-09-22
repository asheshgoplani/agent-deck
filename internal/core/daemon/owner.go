package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// Paths are the files one profile's daemon owns inside its runtime dir.
type Paths struct {
	Dir    string
	Socket string
	// Lock holds the owner's advisory flock and its pid.
	Lock string
}

// PathsIn returns the daemon paths inside dir.
func PathsIn(dir string) Paths {
	return Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock")}
}

// PathsFor returns the daemon paths in the profile's runtime dir.
func PathsFor(profile string) (Paths, error) {
	dir, err := agentpaths.ProfileRuntimeDir(profile)
	if err != nil {
		return Paths{}, err
	}
	return PathsIn(dir), nil
}

// AlreadyRunningError is returned by Acquire when another live process holds
// the owner lock.
type AlreadyRunningError struct{ PID int }

func (e *AlreadyRunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("daemon already running (pid %d)", e.PID)
	}
	return "daemon already running"
}

// Owner is the single owner of a profile's daemon socket.
type Owner struct {
	lock *os.File
	ln   *net.UnixListener
}

// Acquire takes the profile's owner lock and listens on its socket.
//
// The lock is an exclusive flock, so it dies with its process: a daemon
// killed with SIGKILL leaves a socket file behind but no lock holder, and the
// next Acquire removes that stale socket and takes over. While a live owner
// holds the lock, Acquire fails with *AlreadyRunningError and touches
// nothing. A non-socket file at the socket path is never removed.
func Acquire(p Paths) (*Owner, error) {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	if err := os.Chmod(p.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict runtime dir: %w", err)
	}
	lock, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &AlreadyRunningError{PID: readPID(p)}
		}
		return nil, fmt.Errorf("lock daemon: %w", err)
	}
	fail := func(err error) (*Owner, error) {
		_ = lock.Close()
		return nil, err
	}

	if st, err := os.Lstat(p.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fail(fmt.Errorf("%s exists and is not a socket; refusing to replace it", p.Socket))
		}
		// We hold the lock, so no live daemon owns this socket.
		if err := os.Remove(p.Socket); err != nil {
			return fail(fmt.Errorf("remove stale socket: %w", err))
		}
	} else if !os.IsNotExist(err) {
		return fail(fmt.Errorf("stat socket: %w", err))
	}

	if err := lock.Truncate(0); err != nil {
		return fail(fmt.Errorf("write daemon lock: %w", err))
	}
	if _, err := lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return fail(fmt.Errorf("write daemon lock: %w", err))
	}

	// Same pattern as internal/mcppool/socket_proxy.go: listen, then
	// restrict the socket to its owner. The 0700 dir already keeps other
	// users out during the gap.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p.Socket, Net: "unix"})
	if err != nil {
		return fail(fmt.Errorf("listen: %w", err))
	}
	if err := os.Chmod(p.Socket, 0o600); err != nil {
		_ = ln.Close()
		return fail(fmt.Errorf("restrict socket: %w", err))
	}
	return &Owner{lock: lock, ln: ln}, nil
}

// Listener returns the owner's socket listener.
func (o *Owner) Listener() net.Listener { return o.ln }

// Close stops listening (which unlinks the socket) and releases the lock.
func (o *Owner) Close() error {
	err := o.ln.Close()
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	if cerr := o.lock.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// readPID returns the pid recorded in the lock file, or 0.
func readPID(p Paths) int {
	b, err := os.ReadFile(p.Lock)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// State is what Probe found.
type State string

const (
	// StateRunning: a daemon answered on the socket.
	StateRunning State = "running"
	// StateStale: a socket file exists but nothing answers (the owner died).
	StateStale State = "stale"
	// StateAbsent: no socket file.
	StateAbsent State = "absent"
)

// ProbeResult is the outcome of Probe.
type ProbeResult struct {
	State State
	// PID is the last recorded owner pid (stale) or the daemon's pid.
	PID    int
	Status Status
}

// Probe reports whether a daemon is serving p, without changing anything.
func Probe(p Paths) ProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := Dial(ctx, p.Socket); err == nil {
		defer c.Close()
		if st, err := c.Status(); err == nil {
			return ProbeResult{State: StateRunning, PID: st.PID, Status: st}
		}
	}
	if _, err := os.Lstat(p.Socket); err == nil {
		return ProbeResult{State: StateStale, PID: readPID(p)}
	}
	return ProbeResult{State: StateAbsent}
}
