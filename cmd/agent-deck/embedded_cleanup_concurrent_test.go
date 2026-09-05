package main

import (
	"testing"
	"time"
)

func TestEmbeddedCleanupConcurrentExitWaitsForRestoration(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	calls := 0
	setEmbeddedTerminalCleanup(func() {
		calls++
		close(entered)
		<-release
	})
	go func() { runEmbeddedTerminalCleanup(); close(firstDone) }()
	<-entered
	go func() { runEmbeddedTerminalCleanup(); close(secondDone) }()
	select {
	case <-secondDone:
		close(release)
		<-firstDone
		t.Fatal("concurrent exit returned before terminal restoration finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, done := range []chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cleanup did not finish after restoration")
		}
	}
	if calls != 1 {
		t.Fatalf("restoration ran %d times", calls)
	}
}
