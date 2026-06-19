package tmux

import (
	"context"
	"fmt"
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

func TestPipeManager_ReconcileKeepsOnlyLiveSet(t *testing.T) {
	skipIfNoTmuxBinary(t)
	const k = 4
	names := make([]string, k)
	for i := range names {
		names[i] = createTestSessionStrict(t, fmt.Sprintf("liveset%d", i))
	}

	pm := NewPipeManager(context.Background(), nil)
	defer pm.Close()

	// live set = only names[1]
	live := names[1]
	pm.SetWantPipe(func(n string) bool { return n == live })

	// Connect-all: the gate ensures only `live` actually attaches a pipe.
	for _, n := range names {
		_ = pm.Connect(n, "")
	}

	if !waitFor(2*time.Second, func() bool { return pm.IsConnected(live) }) {
		t.Fatalf("live session %s should be connected", live)
	}
	got := pm.ConnectedSessions()
	if len(got) != 1 || got[0] != live {
		t.Fatalf("only the live session should hold a pipe; got %v", got)
	}

	// Move the live set to names[2]: connect the new one, disconnect the old.
	live2 := names[2]
	pm.SetWantPipe(func(n string) bool { return n == live2 })
	_ = pm.Connect(live2, "")
	pm.Disconnect(live) // old one leaves the set

	if !waitFor(2*time.Second, func() bool {
		return pm.IsConnected(live2) && !pm.IsConnected(live)
	}) {
		t.Fatalf("after move: want only %s connected, got %v", live2, pm.ConnectedSessions())
	}

	// Critical: the disconnected pipe must NOT be auto-reconnected by watchPipe
	// (it is no longer wanted). watchPipe's first backoff is ~2s; give it 3s.
	if waitFor(3*time.Second, func() bool { return pm.IsConnected(live) }) {
		t.Fatal("disconnected session was resurrected by watchPipe; wantPipe gate failed")
	}
}
