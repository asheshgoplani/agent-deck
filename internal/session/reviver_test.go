package session

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newReviverTestInstance(title string, status Status) *Instance {
	inst := NewInstance(title, "/tmp")
	inst.Status = status
	return inst
}

func TestReviver_ErroredSessionWithAliveServer_Respawns(t *testing.T) {
	inst := newReviverTestInstance("errored-alive-1", StatusError)

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(name, _ string) bool { return true },
		PipeAlive:    func(name string) bool { return false },
		ReviveAction: func(i *Instance) error { spyCalls++; return nil },
		Stagger:      0,
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 1 {
		t.Fatalf("expected ReviveAction called 1×, got %d", spyCalls)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}
	if outcomes[0].Class != ClassErrored {
		t.Errorf("expected ClassErrored, got %v", outcomes[0].Class)
	}
	if !outcomes[0].Revived {
		t.Errorf("expected Revived=true")
	}
	if outcomes[0].Err != nil {
		t.Errorf("unexpected err: %v", outcomes[0].Err)
	}
}

func TestReviver_DeadServer_NotRevived(t *testing.T) {
	inst := newReviverTestInstance("dead-srv-1", StatusError)

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(name, _ string) bool { return false },
		PipeAlive:    func(name string) bool { return false },
		ReviveAction: func(i *Instance) error { spyCalls++; return nil },
		Stagger:      0,
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 0 {
		t.Fatalf("dead-server must NOT be revived; spy called %d×", spyCalls)
	}
	if outcomes[0].Class != ClassDead {
		t.Errorf("expected ClassDead, got %v", outcomes[0].Class)
	}
	if outcomes[0].Revived {
		t.Errorf("expected Revived=false")
	}
}

func TestReviver_AliveSession_NotRevived(t *testing.T) {
	inst := newReviverTestInstance("alive-1", StatusRunning)

	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(name, _ string) bool { return true },
		PipeAlive:    func(name string) bool { return true },
		ReviveAction: func(i *Instance) error { spyCalls++; return nil },
		Stagger:      0,
	}

	outcomes := r.ReviveAll([]*Instance{inst})

	if spyCalls != 0 {
		t.Fatalf("alive session must NOT be revived; spy called %d×", spyCalls)
	}
	if outcomes[0].Class != ClassAlive {
		t.Errorf("expected ClassAlive, got %v", outcomes[0].Class)
	}
}

func TestReviver_DeletedInstance_NotRevived(t *testing.T) {
	// Tombstone semantics: a removed instance is absent from the storage-
	// returned slice. Passing an empty slice must produce no outcomes and
	// no revive calls — we never resurrect user-deleted work.
	spyCalls := 0
	r := &Reviver{
		TmuxExists:   func(name, _ string) bool { return true },
		PipeAlive:    func(name string) bool { return false },
		ReviveAction: func(i *Instance) error { spyCalls++; return nil },
		Stagger:      0,
	}

	outcomes := r.ReviveAll([]*Instance{})

	if spyCalls != 0 {
		t.Fatalf("tombstone: nothing to revive; spy called %d×", spyCalls)
	}
	if len(outcomes) != 0 {
		t.Errorf("expected zero outcomes, got %d", len(outcomes))
	}
}

func TestReviver_Stagger500ms_AcrossManyInstances(t *testing.T) {
	// 4 errored + 1 alive. The alive entry must not consume a stagger slot,
	// so total stagger cost is (4-1) gaps × stagger.
	errored := []*Instance{
		newReviverTestInstance("e1", StatusError),
		newReviverTestInstance("e2", StatusError),
		newReviverTestInstance("e3", StatusError),
		newReviverTestInstance("e4", StatusError),
	}
	alive := newReviverTestInstance("a1", StatusRunning)

	stagger := 20 * time.Millisecond
	var mu sync.Mutex
	calls := []string{}

	erroredSet := map[string]bool{"e1": true, "e2": true, "e3": true, "e4": true}

	r := &Reviver{
		TmuxExists: func(name, _ string) bool { return true },
		PipeAlive: func(name string) bool {
			// erroredSet members have dead pipes; alive has a live pipe
			return !erroredSet[name]
		},
		ReviveAction: func(i *Instance) error {
			mu.Lock()
			calls = append(calls, i.Title)
			mu.Unlock()
			return nil
		},
		Stagger: stagger,
	}

	start := time.Now()
	outcomes := r.ReviveAll([]*Instance{errored[0], alive, errored[1], errored[2], errored[3]})
	elapsed := time.Since(start)

	if len(calls) != 4 {
		t.Fatalf("expected 4 revive calls (4 errored), got %d", len(calls))
	}
	minExpected := 3 * stagger
	maxExpected := 5 * stagger
	if elapsed < minExpected {
		t.Errorf("stagger too short: elapsed=%v expected>=%v", elapsed, minExpected)
	}
	if elapsed > maxExpected {
		t.Errorf("stagger too long: elapsed=%v expected<=%v", elapsed, maxExpected)
	}

	aliveCount := 0
	revivedCount := 0
	for _, o := range outcomes {
		if o.Class == ClassAlive {
			aliveCount++
		}
		if o.Revived {
			revivedCount++
		}
	}
	if aliveCount != 1 {
		t.Errorf("expected 1 alive outcome, got %d", aliveCount)
	}
	if revivedCount != 4 {
		t.Errorf("expected 4 revived outcomes, got %d", revivedCount)
	}
}

func TestReviver_DefaultAction_NoopOnNilPipeManager(t *testing.T) {
	// Default reviver must not panic when the global PipeManager is nil
	// (control pipes disabled or not yet initialized).
	inst := newReviverTestInstance("nopipe-1", StatusError)

	r := NewReviver()
	// Force the classifier to see "errored + alive server" deterministically:
	r.TmuxExists = func(name, _ string) bool { return true }
	r.PipeAlive = func(name string) bool { return false }

	// Run action directly — must not panic, must return nil gracefully.
	err := r.ReviveAction(inst)
	if err != nil {
		t.Fatalf("default action returned error with nil PipeManager: %v", err)
	}
}

func TestReviver_ActionError_PropagatedInOutcome(t *testing.T) {
	inst := newReviverTestInstance("err-prop-1", StatusError)

	r := &Reviver{
		TmuxExists:   func(name, _ string) bool { return true },
		PipeAlive:    func(name string) bool { return false },
		ReviveAction: func(i *Instance) error { return fmt.Errorf("boom") },
		Stagger:      0,
	}

	outcomes := r.ReviveAll([]*Instance{inst})
	if outcomes[0].Revived {
		t.Errorf("action errored — Revived must be false")
	}
	if outcomes[0].Err == nil {
		t.Errorf("expected Err to propagate")
	}
}
