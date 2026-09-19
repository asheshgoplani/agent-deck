package ingest

import (
	"errors"
	"fmt"
)

// The backfill and the sweep are batch work. Rate limiting bounds their
// average cost; the gate is what keeps them from competing with the agent
// the user is watching: it refuses to start while any managed session is
// busy or the machine is already loaded.

// ErrGated is returned (wrapped with the reason) when the gate refuses.
var ErrGated = errors.New("recall: load gate refused")

// Gate decides whether batch work may start now.
type Gate struct {
	// Busy reports whether a managed session is mid-turn, with a reason
	// for the user ("session auth-fix is running"). nil means no sessions
	// are consulted.
	Busy func() (busy bool, why string)
	// MaxLoadAvg refuses when the one-minute load average exceeds it
	// (0 disables the check).
	MaxLoadAvg float64
	// LoadAvg overrides the platform reader in tests.
	LoadAvg func() float64
}

// Check returns nil when work may proceed, or an ErrGated with the reason.
func (g *Gate) Check() error {
	if g == nil {
		return nil
	}
	if g.Busy != nil {
		if busy, why := g.Busy(); busy {
			return fmt.Errorf("%w: %s", ErrGated, why)
		}
	}
	if g.MaxLoadAvg > 0 {
		read := g.LoadAvg
		if read == nil {
			read = LoadAvg1
		}
		if la := read(); la > g.MaxLoadAvg {
			return fmt.Errorf("%w: load average %.1f exceeds %.1f", ErrGated, la, g.MaxLoadAvg)
		}
	}
	return nil
}

// BusyStatuses are the instance statuses that mean "an agent is working";
// a session in any other state (idle, waiting, error, stopped) is not
// disturbed by a background read of its transcript.
var BusyStatuses = map[string]bool{"running": true, "starting": true}
