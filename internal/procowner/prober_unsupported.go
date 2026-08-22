//go:build !linux && !darwin

package procowner

import "fmt"

// ProviderUnsupported names the no-op provider used where no start-identity
// source exists.
const ProviderUnsupported = "unsupported"

// UnsupportedProber is the safe cross-platform fallback.
//
// Every call fails with ErrUnsupported, which means: no receipt is ever written
// (Spawn ownership cannot be established), so no receipt is ever verified, and
// nothing is ever signalled. The restart path keeps its pre-#1873 behaviour on
// such a platform rather than blocking every restart on an ownership question
// the host cannot answer. This is a deliberate scope limit, stated rather than
// silently degraded: without a start identity the PID+start-time contract has
// no identity half, and a PID alone must never authorise a signal.
type UnsupportedProber struct{}

// NewProber returns the platform's start-identity provider.
func NewProber() Prober { return UnsupportedProber{} }

// Name implements Prober.
func (UnsupportedProber) Name() string { return ProviderUnsupported }

// BootID implements Prober.
func (UnsupportedProber) BootID() (string, error) {
	return "", fmt.Errorf("%w: boot id", ErrUnsupported)
}

// Inspect implements Prober.
func (UnsupportedProber) Inspect(pid int) (ProcInfo, error) {
	return ProcInfo{}, fmt.Errorf("%w: inspect pid %d", ErrUnsupported, pid)
}

// Descendants implements Prober.
func (UnsupportedProber) Descendants(root ProcInfo) ([]ProcInfo, error) {
	return nil, fmt.Errorf("%w: descendants of pid %d", ErrUnsupported, root.PID)
}
