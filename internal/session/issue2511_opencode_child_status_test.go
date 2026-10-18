package session

// Parent status while sub-agents run on OpenCode 2.x (issue #2511). A
// sub-agent turn runs in a child session whose parentID points at the session
// the deck instance is bound to; the shared stream frames it with the child's
// sessionID only, so the watcher maps it back through the parent chain.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	sesChild      = "ses_child1aaaaaaaaaaaaaaaaaaaaaa"
	sesGrandchild = "ses_grand1aaaaaaaaaaaaaaaaaaaaaa"
	sesOtherChild = "ses_ochild1aaaaaaaaaaaaaaaaaaaaa"
)

// parentLookup is a fake session.get: it answers from parents and counts
// calls per session id. failFirst fails that many calls before answering;
// delay holds each call, so concurrent streams overlap.
type parentLookup struct {
	mu        sync.Mutex
	parents   map[string]string
	calls     map[string]int
	failFirst int
	delay     time.Duration
}

func newParentLookup(parents map[string]string) *parentLookup {
	return &parentLookup{parents: parents, calls: map[string]int{}}
}

func (p *parentLookup) parentOf(_ context.Context, sessionID string) (string, error) {
	time.Sleep(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[sessionID]++
	if p.failFirst > 0 {
		p.failFirst--
		return "", errors.New("service busy")
	}
	parent, ok := p.parents[sessionID]
	if !ok {
		return "", errors.New("session not found")
	}
	return parent, nil
}

func (p *parentLookup) callCount(sessionID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[sessionID]
}

func sharedTargetWithChildren(instanceID, sessionID, url string, lookup *parentLookup) SSETarget {
	t := sharedTarget(instanceID, sessionID, url)
	t.parentOf = lookup.parentOf
	return t
}

// sendAndSettle sends events on a one-stream watcher and waits until the
// stream has handled all of them (each ends in one status write or touch).
func sendAndSettle(t *testing.T, w *OpenCodeSSEWatcher, send func(string), events ...string) {
	t.Helper()
	want := w.writes.Load() + int64(len(events))
	for _, ev := range events {
		send(ev)
	}
	deadline := time.Now().Add(5 * time.Second)
	for w.writes.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("stream handled %d of %d events", len(events)-int(want-w.writes.Load()), len(events))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertStatus(t *testing.T, w *OpenCodeSSEWatcher, instanceID, want, why string) {
	t.Helper()
	if s := w.GetStatus(instanceID); s == nil || s.Status != want {
		t.Fatalf("%s: want %q, got %+v", why, want, s)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceChildKeepsParentRunning: the bound
// session's own turn ends while its sub-agent (and the sub-agent's own
// sub-agent) still runs; the instance stays running until the last one ends,
// then goes to waiting. Another deck's sub-agent never moves it.
func TestOpenCodeSSEWatcher_SharedServiceChildKeepsParentRunning(t *testing.T) {
	// session.active lists the live sub-agent when the bound turn ends.
	var snapshot atomic.Value
	snapshot.Store(`{"data":{}}`)
	srv, send := sharedServiceServerFunc(t, func() string { return snapshot.Load().(string) })
	lookup := newParentLookup(map[string]string{
		sesChild:      sesBound,
		sesGrandchild: sesChild,
		sesOtherChild: sesOther,
		sesOther:      "",
	})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")
	snapshot.Store(`{"data":{"` + sesChild + `":{"type":"running"}}}`)
	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesChild),
		sharedServiceEvent("session.execution.succeeded", sesBound))
	assertStatus(t, w, "inst-bound", "running", "a running sub-agent keeps the parent running")

	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesGrandchild),
		sharedServiceEvent("session.execution.succeeded", sesChild))
	assertStatus(t, w, "inst-bound", "running", "a running nested sub-agent keeps the parent running")

	sendAndSettle(t, w, send, sharedServiceEvent("session.execution.succeeded", sesGrandchild))
	assertStatus(t, w, "inst-bound", "waiting", "the last sub-agent ending leaves the parent waiting")

	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesOtherChild),
		sharedServiceEvent("session.execution.started", sesOther))
	assertStatus(t, w, "inst-bound", "waiting", "another session's sub-agent must not move this instance")

	// A child turn that starts after the parent went idle wakes the parent.
	send(sharedServiceEvent("session.execution.started", sesChild))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.failed", sesChild))
	waitForStatus(t, w, "inst-bound", "waiting")

	// Parent links never change, so each session is asked about at most once.
	for _, id := range []string{sesChild, sesGrandchild, sesOtherChild, sesOther} {
		if n := lookup.callCount(id); n > 1 {
			t.Errorf("session.get for %s ran %d times, want at most 1", id, n)
		}
	}
	if n := lookup.callCount(sesBound); n != 0 {
		t.Errorf("the bound session itself must never be looked up, ran %d times", n)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceSnapshotSeedsFromChild: connecting while
// only a sub-agent is busy seeds running.
func TestOpenCodeSSEWatcher_SharedServiceSnapshotSeedsFromChild(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{"`+sesChild+`":{"type":"running"},"`+sesOtherChild+`":{"type":"running"}}}`)
	lookup := newParentLookup(map[string]string{sesChild: sesBound, sesOtherChild: sesOther, sesOther: ""})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{
		sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup),
		sharedTargetWithChildren("inst-idle", sesGrandchild+"x", srv.URL, lookup),
	})
	waitForStatus(t, w, "inst-bound", "running")
	waitForStatus(t, w, "inst-idle", "waiting")

	send(sharedServiceEvent("session.execution.succeeded", sesChild))
	waitForStatus(t, w, "inst-bound", "waiting")
}

// TestOpenCodeSSEWatcher_SharedServiceStreamsShareLookup: several decks on
// the one service see the same new sub-agent at once; session.get runs once.
func TestOpenCodeSSEWatcher_SharedServiceStreamsShareLookup(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	lookup := newParentLookup(map[string]string{sesChild: sesBound})
	lookup.delay = 200 * time.Millisecond
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{
		sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup),
		sharedTargetWithChildren("inst-other", sesOther, srv.URL, lookup),
		sharedTargetWithChildren("inst-third", sesOtherChild, srv.URL, lookup),
	})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesChild))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.started", sesOther))
	waitForStatus(t, w, "inst-other", "running")
	if n := lookup.callCount(sesChild); n != 1 {
		t.Fatalf("three streams asking about one session must share one session.get, ran %d", n)
	}
	if s := w.GetStatus("inst-third"); s != nil && s.Status == "running" {
		t.Fatalf("an unrelated deck must not count the sub-agent, got %+v", s)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceRetriesFailedLookup: a lookup that
// failed is asked again after the retry window instead of excluding a real
// sub-agent for the life of the stream.
func TestOpenCodeSSEWatcher_SharedServiceRetriesFailedLookup(t *testing.T) {
	prev := openCodeParentRetry
	openCodeParentRetry = 0
	t.Cleanup(func() { openCodeParentRetry = prev })
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	lookup := newParentLookup(map[string]string{sesChild: sesBound})
	lookup.failFirst = 1
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	sendAndSettle(t, w, send, sharedServiceEvent("session.execution.started", sesChild))
	if s := w.GetStatus("inst-bound"); s != nil && s.Status == "running" {
		t.Fatalf("a failed lookup must not count the session, got %+v", s)
	}
	send(sharedServiceEvent("session.execution.started", sesChild))
	waitForStatus(t, w, "inst-bound", "running")
}

// TestOpenCodeSSEWatcher_SharedServiceLookupFailureIgnoresSession: a session
// whose parent cannot be read counts as unrelated (the pre-fix behaviour),
// and the stream keeps serving the bound session.
func TestOpenCodeSSEWatcher_SharedServiceLookupFailureIgnoresSession(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	lookup := newParentLookup(map[string]string{})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesChild),
		sharedServiceEvent("session.execution.started", sesChild))
	if s := w.GetStatus("inst-bound"); s != nil && s.Status == "running" {
		t.Fatalf("a session whose parent is unknown must not count, got %+v", s)
	}
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.succeeded", sesBound))
	waitForStatus(t, w, "inst-bound", "waiting")
	if n := lookup.callCount(sesChild); n != 1 {
		t.Fatalf("a failed lookup is not repeated inside the retry window, ran %d times", n)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceParentCycleTerminates: a corrupt parent
// chain must not hang the stream.
func TestOpenCodeSSEWatcher_SharedServiceParentCycleTerminates(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	lookup := newParentLookup(map[string]string{sesChild: sesGrandchild, sesGrandchild: sesChild})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesChild))
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.succeeded", sesBound))
	waitForStatus(t, w, "inst-bound", "waiting")
}

// TestOpenCodeSSETarget_V2LooksUpChildren: the 2.x target carries the parent
// lookup; 1.x keeps counting every session on its private server as before.
func TestOpenCodeSSETarget_V2LooksUpChildren(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	inst := &Instance{ID: "inst-1", Tool: "opencode", OpenCodeSessionID: sesBound}
	target, ok := inst.OpenCodeSSETarget()
	if !ok || target.parentOf == nil {
		t.Fatalf("2.x target must look up sub-agent parents, got ok=%v %+v", ok, target)
	}
}

// TestOpenCodeSessionParent: the lookup runs `opencode api session.get` for
// the session and reads data.parentID; a root session has none.
func TestOpenCodeSessionParent(t *testing.T) {
	useInstalledOpenCodeProbe(t)
	calls := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = session.get ]; then\n" +
		"  case \"$4\" in\n" +
		"    sessionID=" + sesChild + ") printf '%s' '{\"data\":{\"id\":\"" + sesChild + "\",\"parentID\":\"" + sesBound + "\",\"title\":\"sub\"}}' ;;\n" +
		"    sessionID=" + sesBound + ") printf '%s' '{\"data\":{\"id\":\"" + sesBound + "\",\"title\":\"root\"}}' ;;\n" +
		"    *) echo 'not found' >&2; exit 1 ;;\n" +
		"  esac\n  exit 0\nfi\necho 'opencode v2.0.20'\n"
	useOpenCodeStub(t, script)
	inst := &Instance{ID: "inst-1", Tool: "opencode", ProjectPath: t.TempDir()}

	parent, err := inst.openCodeSessionParent(context.Background(), sesChild)
	if err != nil || parent != sesBound {
		t.Fatalf("child parent = %q, %v; want %q", parent, err, sesBound)
	}
	parent, err = inst.openCodeSessionParent(context.Background(), sesBound)
	if err != nil || parent != "" {
		t.Fatalf("root parent = %q, %v; want empty", parent, err)
	}
	if _, err := inst.openCodeSessionParent(context.Background(), sesOther); err == nil {
		t.Fatal("a failing session.get must be an error")
	}
	if _, err := inst.openCodeSessionParent(context.Background(), "ses_bad;rm -rf"); err == nil {
		t.Fatal("an id that is not shell-safe must be refused before running the CLI")
	}
	data, _ := os.ReadFile(calls)
	if strings.Contains(string(data), "rm -rf") {
		t.Fatalf("refused id reached the CLI:\n%s", data)
	}
	if !strings.Contains(string(data), "api session.get --param sessionID="+sesChild) {
		t.Fatalf("unexpected CLI calls:\n%s", data)
	}
}

// TestOpenCodeSessionTree_WaiterRetriesAfterLeaderCancelled: a stream waiting
// on another stream's lookup asks again when that stream was cancelled
// mid-call, instead of taking its cancellation as the answer.
func TestOpenCodeSessionTree_WaiterRetriesAfterLeaderCancelled(t *testing.T) {
	w := NewOpenCodeSSEWatcher(nil)
	started := make(chan struct{})
	var calls atomic.Int32
	parentOf := func(ctx context.Context, sessionID string) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return sesBound, nil
	}
	src := openCodeEventSource{sessionID: sesBound, parentOf: parentOf}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan bool)
	go func() { leaderDone <- w.newSessionTree(leaderCtx, src).counts(leaderCtx, sesChild) }()
	<-started

	waiterDone := make(chan bool)
	go func() {
		waiterDone <- w.newSessionTree(context.Background(), src).counts(context.Background(), sesChild)
	}()
	// The waiter must have joined the in-flight call before the leader is cancelled.
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.mu.Lock()
		joined := w.parents[sesChild] != nil && w.parents[sesChild].waiters == 1
		w.mu.Unlock()
		if joined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter never joined the in-flight lookup")
		}
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	if <-leaderDone {
		t.Fatal("a cancelled leader must not count the session")
	}
	select {
	case got := <-waiterDone:
		if !got {
			t.Fatal("the waiter took the cancelled leader's error as its answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never finished")
	}
}

// TestOpenCodeSSEWatcher_SharedServiceDropsStaleChild: a counted sub-agent
// that never sends its turn ending (aborted, deleted, died) is dropped once
// session.active stops listing it, both when the bound turn ends and on the
// periodic check, so the parent does not stay running.
func TestOpenCodeSSEWatcher_SharedServiceDropsStaleChild(t *testing.T) {
	var snapshot atomic.Value
	snapshot.Store(`{"data":{}}`)
	srv, send := sharedServiceServerFunc(t, func() string { return snapshot.Load().(string) })
	lookup := newParentLookup(map[string]string{sesChild: sesBound, sesGrandchild: sesBound})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	// The bound turn ends while a live sub-agent runs: it stays counted.
	snapshot.Store(`{"data":{"` + sesChild + `":{"type":"running"}}}`)
	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesBound),
		sharedServiceEvent("session.execution.started", sesChild),
		sharedServiceEvent("session.execution.succeeded", sesBound))
	assertStatus(t, w, "inst-bound", "running", "a sub-agent session.active still lists keeps the parent running")

	// It dies without an ending; the next bound turn end drops it.
	snapshot.Store(`{"data":{}}`)
	sendAndSettle(t, w, send,
		sharedServiceEvent("session.execution.started", sesBound),
		sharedServiceEvent("session.execution.succeeded", sesBound))
	// The drop is written right after the turn end's own status write.
	waitForStatus(t, w, "inst-bound", "waiting")
}

// TestOpenCodeSSEWatcher_SharedServicePeriodicCheckDropsStaleChild: while the
// parent is idle, the periodic check drops a sub-agent that died mid-turn.
func TestOpenCodeSSEWatcher_SharedServicePeriodicCheckDropsStaleChild(t *testing.T) {
	prev := openCodeReconcileInterval
	openCodeReconcileInterval = 0
	t.Cleanup(func() { openCodeReconcileInterval = prev })
	var snapshot atomic.Value
	snapshot.Store(`{"data":{}}`)
	srv, send := sharedServiceServerFunc(t, func() string { return snapshot.Load().(string) })
	lookup := newParentLookup(map[string]string{sesChild: sesBound})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	snapshot.Store(`{"data":{"` + sesChild + `":{"type":"running"}}}`)
	send(sharedServiceEvent("session.execution.started", sesChild))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.started", sesOther)) // unrelated traffic, child still listed
	sendAndSettle(t, w, send, sharedServiceEvent("session.execution.started", sesOther))
	assertStatus(t, w, "inst-bound", "running", "a sub-agent session.active still lists stays counted")

	snapshot.Store(`{"data":{}}`) // it died without an ending
	send(sharedServiceEvent("session.execution.started", sesOther))
	waitForStatus(t, w, "inst-bound", "waiting")
}

// TestOpenCodeSSEWatcher_SharedServiceStuckLookupDoesNotStallStream: a
// session.get that never returns is cut off by the walk budget, so the bound
// session's own transitions still arrive.
func TestOpenCodeSSEWatcher_SharedServiceStuckLookupDoesNotStallStream(t *testing.T) {
	prev := openCodeParentWalkBudget
	openCodeParentWalkBudget = 100 * time.Millisecond
	t.Cleanup(func() { openCodeParentWalkBudget = prev })
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	target := sharedTarget("inst-bound", sesBound, srv.URL)
	target.parentOf = func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{target})
	waitConnected()

	send(sharedServiceEvent("session.execution.started", sesChild))
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")
}

// TestOpenCodeSessionTree_ParentCacheIsBounded: the shared cache drops
// finished lookups once it reaches its cap.
func TestOpenCodeSessionTree_ParentCacheIsBounded(t *testing.T) {
	prev := openCodeParentCacheMax
	openCodeParentCacheMax = 4
	t.Cleanup(func() { openCodeParentCacheMax = prev })
	w := NewOpenCodeSSEWatcher(nil)
	src := openCodeEventSource{sessionID: sesBound, parentOf: func(context.Context, string) (string, error) { return "", nil }}
	tree := w.newSessionTree(context.Background(), src)
	for n := 0; n < 50; n++ {
		tree.counts(context.Background(), fmt.Sprintf("ses_unrelated%03d", n))
	}
	w.mu.Lock()
	size := len(w.parents)
	w.mu.Unlock()
	if size > openCodeParentCacheMax {
		t.Fatalf("parent cache holds %d entries, cap %d", size, openCodeParentCacheMax)
	}
}
