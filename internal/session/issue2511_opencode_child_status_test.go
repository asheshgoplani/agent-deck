package session

// Parent status while sub-agents run on OpenCode 2.x (issue #2511). A
// sub-agent turn runs in a child session whose parentID points at the session
// the deck instance is bound to; the shared stream frames it with the child's
// sessionID only, so the watcher maps it back through the parent chain.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sesChild      = "ses_child1aaaaaaaaaaaaaaaaaaaaaa"
	sesGrandchild = "ses_grand1aaaaaaaaaaaaaaaaaaaaaa"
	sesOtherChild = "ses_ochild1aaaaaaaaaaaaaaaaaaaaa"
)

// parentLookup is a fake session.get: it answers from parents and counts
// calls per session id.
type parentLookup struct {
	mu      sync.Mutex
	parents map[string]string
	calls   map[string]int
}

func newParentLookup(parents map[string]string) *parentLookup {
	return &parentLookup{parents: parents, calls: map[string]int{}}
}

func (p *parentLookup) parentOf(sessionID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[sessionID]++
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

// assertStaysStatus fails if the instance's status leaves want within a short window.
func assertStaysStatus(t *testing.T, w *OpenCodeSSEWatcher, instanceID, want, why string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if s := w.GetStatus(instanceID); s == nil || s.Status != want {
		t.Fatalf("%s: want %q, got %+v", why, want, s)
	}
}

// TestOpenCodeSSEWatcher_SharedServiceChildKeepsParentRunning: the bound
// session's own turn ends while its sub-agent (and the sub-agent's own
// sub-agent) still runs; the instance stays running until the last one ends,
// then goes to waiting. Another deck's sub-agent never moves it.
func TestOpenCodeSSEWatcher_SharedServiceChildKeepsParentRunning(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
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
	send(sharedServiceEvent("session.execution.started", sesChild))
	send(sharedServiceEvent("session.execution.succeeded", sesBound))
	assertStaysStatus(t, w, "inst-bound", "running", "a running sub-agent keeps the parent running")

	send(sharedServiceEvent("session.execution.started", sesGrandchild))
	send(sharedServiceEvent("session.execution.succeeded", sesChild))
	assertStaysStatus(t, w, "inst-bound", "running", "a running nested sub-agent keeps the parent running")

	send(sharedServiceEvent("session.execution.succeeded", sesGrandchild))
	waitForStatus(t, w, "inst-bound", "waiting")

	send(sharedServiceEvent("session.execution.started", sesOtherChild))
	send(sharedServiceEvent("session.execution.started", sesOther))
	assertStaysStatus(t, w, "inst-bound", "waiting", "another session's sub-agent must not move this instance")

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

// TestOpenCodeSSEWatcher_SharedServiceEventParentIDSkipsLookup: when the event
// itself names the parent, no session.get is needed.
func TestOpenCodeSSEWatcher_SharedServiceEventParentIDSkipsLookup(t *testing.T) {
	srv, send := sharedServiceServer(t, `{"data":{}}`)
	lookup := newParentLookup(map[string]string{})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.Sync([]SSETarget{sharedTargetWithChildren("inst-bound", sesBound, srv.URL, lookup)})
	waitConnected()

	send(fmt.Sprintf(`{"id":"evt_p","type":"session.execution.started","data":{"sessionID":%q,"parentID":%q}}`, sesChild, sesBound))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.succeeded", sesChild))
	waitForStatus(t, w, "inst-bound", "waiting")
	if n := lookup.callCount(sesChild); n != 0 {
		t.Fatalf("event carried the parent, session.get ran %d times", n)
	}
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

	send(sharedServiceEvent("session.execution.started", sesChild))
	send(sharedServiceEvent("session.execution.started", sesChild))
	send(sharedServiceEvent("session.execution.started", sesBound))
	waitForStatus(t, w, "inst-bound", "running")
	send(sharedServiceEvent("session.execution.succeeded", sesBound))
	waitForStatus(t, w, "inst-bound", "waiting")
	if n := lookup.callCount(sesChild); n != 1 {
		t.Fatalf("a failed lookup is not retried on the same stream, ran %d times", n)
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

	parent, err := inst.openCodeSessionParent(sesChild)
	if err != nil || parent != sesBound {
		t.Fatalf("child parent = %q, %v; want %q", parent, err, sesBound)
	}
	parent, err = inst.openCodeSessionParent(sesBound)
	if err != nil || parent != "" {
		t.Fatalf("root parent = %q, %v; want empty", parent, err)
	}
	if _, err := inst.openCodeSessionParent(sesOther); err == nil {
		t.Fatal("a failing session.get must be an error")
	}
	if _, err := inst.openCodeSessionParent("ses_bad;rm -rf"); err == nil {
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
