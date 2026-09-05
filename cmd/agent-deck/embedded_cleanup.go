package main

import "sync"

// embeddedTerminalCleanup holds the release step for the embedded terminal's
// stdin router and stdout wrapper. Bubble Tea restores its own terminal state
// on every exit, but it knows nothing about the hardware-cursor shape and
// pointer shape the wrapper wrote, so those must be undone by us on every
// path out of the process: the deferred return from the TUI, the signal
// handler's os.Exit, and the p.Run error exit. Deferred calls do not run past
// os.Exit, hence a shared, idempotent hook instead of two defers.
var embeddedTerminalCleanup struct {
	mu sync.Mutex
	fn func()
}

func setEmbeddedTerminalCleanup(fn func()) {
	embeddedTerminalCleanup.mu.Lock()
	embeddedTerminalCleanup.fn = fn
	embeddedTerminalCleanup.mu.Unlock()
}

// runEmbeddedTerminalCleanup runs the registered release at most once. Safe to
// call when nothing was registered (classic layout).
func runEmbeddedTerminalCleanup() {
	embeddedTerminalCleanup.mu.Lock()
	defer embeddedTerminalCleanup.mu.Unlock()
	fn := embeddedTerminalCleanup.fn
	embeddedTerminalCleanup.fn = nil
	if fn != nil {
		// A concurrent signal exit must wait for the same terminal restore,
		// rather than observing nil and exiting while restoration is active.
		fn()
	}
}
