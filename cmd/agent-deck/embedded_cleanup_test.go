package main

import "testing"

func TestEmbeddedTerminalCleanupRunsOnceAndTolerantOfNothingRegistered(t *testing.T) {
	// Classic layout registers nothing; every exit path still calls this.
	runEmbeddedTerminalCleanup()

	calls := 0
	setEmbeddedTerminalCleanup(func() { calls++ })
	// Signal handler and deferred return may both reach it; the terminal
	// must be released exactly once.
	runEmbeddedTerminalCleanup()
	runEmbeddedTerminalCleanup()
	if calls != 1 {
		t.Fatalf("cleanup ran %d times, want 1", calls)
	}
}
