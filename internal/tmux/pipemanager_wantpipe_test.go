package tmux

import (
	"context"
	"testing"
	"time"
)

// waitFor polls cond up to d, returning true once it holds.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestPipeManager_WantPipeGatesConnect(t *testing.T) {
	skipIfNoTmuxBinary(t)
	allowed := createTestSessionStrict(t, "allowed")
	denied := createTestSessionStrict(t, "denied")

	pm := NewPipeManager(context.Background(), nil)
	defer pm.Close()
	pm.SetWantPipe(func(name string) bool { return name == allowed })

	if err := pm.Connect(allowed, ""); err != nil {
		t.Fatalf("connect allowed: %v", err)
	}
	// Connect on a denied session must be a silent no-op (no pipe created).
	if err := pm.Connect(denied, ""); err != nil {
		t.Fatalf("connect denied returned error (want nil no-op): %v", err)
	}

	if !waitFor(2*time.Second, func() bool { return pm.IsConnected(allowed) }) {
		t.Fatal("allowed session should be connected")
	}
	if pm.IsConnected(denied) {
		t.Fatal("denied session must not be connected")
	}

	got := pm.ConnectedSessions()
	if len(got) != 1 || got[0] != allowed {
		t.Fatalf("ConnectedSessions = %v, want [%s]", got, allowed)
	}
}

func TestPipeManager_NilWantPipeConnectsAll(t *testing.T) {
	skipIfNoTmuxBinary(t)
	s := createTestSessionStrict(t, "nilwant")
	pm := NewPipeManager(context.Background(), nil)
	defer pm.Close()
	// No SetWantPipe call: nil predicate = legacy "connect everything".
	if err := pm.Connect(s, ""); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !waitFor(2*time.Second, func() bool { return pm.IsConnected(s) }) {
		t.Fatal("with nil wantPipe, connect must work as before")
	}
}

func TestWantsReconnect(t *testing.T) {
	if !wantsReconnect(nil, "x") {
		t.Fatal("nil predicate must allow reconnect (legacy)")
	}
	allow := func(n string) bool { return n == "keep" }
	if !wantsReconnect(allow, "keep") {
		t.Fatal("wanted session should reconnect")
	}
	if wantsReconnect(allow, "drop") {
		t.Fatal("unwanted session must not reconnect")
	}
}
