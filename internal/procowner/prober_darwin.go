//go:build darwin

package procowner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProviderDarwinPS is the provider name recorded in receipts written here.
const ProviderDarwinPS = "darwin_ps"

// psTimeout bounds every probe. An ownership check runs on the restart path, so
// a wedged `ps` must fail closed quickly rather than hang the restart.
const psTimeout = 5 * time.Second

// DarwinProber reads process start identity from BSD `ps`.
//
// macOS has no /proc, and the ruling forbids new dependencies, so the start
// identity is the `lstart` column: the process's start wall-clock time to
// one-second resolution. That is weaker than Linux's start ticks — two
// processes sharing a PID within the same second would be indistinguishable —
// but PID reuse requires the whole pid space to wrap, which does not happen
// inside a second on a healthy machine. Where it is ambiguous the verifier
// fails closed and signals nothing.
type DarwinProber struct{}

// NewProber returns the platform's start-identity provider.
func NewProber() Prober { return DarwinProber{} }

// Name implements Prober.
func (DarwinProber) Name() string { return ProviderDarwinPS }

// BootID implements Prober. kern.boottime changes on every boot, which is all a
// boot identifier has to do.
func (DarwinProber) BootID() (string, error) {
	out, err := runBounded("sysctl", "-n", "kern.boottime")
	if err != nil {
		return "", fmt.Errorf("%w: read kern.boottime: %v", ErrUnreadable, err)
	}
	return parseKernBootTime(out)
}

// Inspect implements Prober.
func (DarwinProber) Inspect(pid int) (ProcInfo, error) {
	if pid <= 0 {
		return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}
	out, err := runBounded("ps", "-p", strconv.Itoa(pid), "-o", psFieldOrder)
	if err != nil {
		// ps exits non-zero with no output when the pid is absent. Anything
		// else (a timeout, a missing binary) stays unreadable, not "gone".
		if strings.TrimSpace(out) == "" && isExitStatus(err) {
			return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
		}
		return ProcInfo{}, fmt.Errorf("%w: ps for pid %d: %v", ErrUnreadable, pid, err)
	}
	rows := parsePSTable(out)
	if len(rows) == 0 {
		return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}
	if rows[0].PID != pid {
		return ProcInfo{}, fmt.Errorf("%w: ps reported pid %d for %d", ErrUnreadable, rows[0].PID, pid)
	}
	if rows[0].IsZombie() {
		// See ProcInfo.IsZombie: exited, awaiting wait(), not alive.
		return ProcInfo{}, fmt.Errorf("%w: pid %d is a zombie", ErrNoProcess, pid)
	}
	return rows[0], nil
}

// Descendants implements Prober over a single `ps -A` snapshot.
func (DarwinProber) Descendants(root ProcInfo) ([]ProcInfo, error) {
	if root.PID <= 0 {
		return nil, fmt.Errorf("%w: pid %d", ErrNoProcess, root.PID)
	}
	out, err := runBounded("ps", "-Ao", psFieldOrder)
	if err != nil {
		return nil, fmt.Errorf("%w: ps -A: %v", ErrUnreadable, err)
	}
	return descendantsFromTable(root, parsePSTable(out)), nil
}

func runBounded(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func isExitStatus(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

// CompareStart implements StartComparer over the `lstart` wall-clock format.
func (DarwinProber) CompareStart(a, b string) (int, error) {
	at, err := parseLstart(a)
	if err != nil {
		return 0, err
	}
	bt, err := parseLstart(b)
	if err != nil {
		return 0, err
	}
	switch {
	case at.Before(bt):
		return -1, nil
	case at.After(bt):
		return 1, nil
	default:
		return 0, nil
	}
}
