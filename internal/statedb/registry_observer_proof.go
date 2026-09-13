//go:build watcher_proof

package statedb

// HoldRegistryObserverForProof blocks probes without timers or DB scheduling.
// It exists only in the explicitly tagged behavioral proof fixture.
func HoldRegistryObserverForProof(o *RegistryObserver) func() {
	o.mu.Lock()
	return o.mu.Unlock
}
