package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/1853.
//
// `session set <id> title <new>` had no collision check on either side of its
// SetField call, so it could rename a session onto a title another session
// already holds at the same location — the exact state `add` and `launch`
// refuse. Title plus location is what the CLI treats as a session's identity, so
// the result is two sessions neither of which is addressable by title:
// ResolveSession reports ErrCodeAmbiguous for both (pinned in
// TestResolveSession_TitleHeldTwiceIsAmbiguous).
//
//	agent-deck add -t alpha /tmp/proj
//	agent-deck add -t beta  /tmp/proj
//	agent-deck session set <beta-id> title alpha    # used to succeed
//	agent-deck add -t alpha /tmp/proj               # refused, exit 1

// TestTitleConflictAt_RejectsTitleHeldAtSameLocation is the reported case.
func TestTitleConflictAt_RejectsTitleHeldAtSameLocation(t *testing.T) {
	alpha := &session.Instance{ID: "alpha-id", Title: "alpha", ProjectPath: "/tmp/proj"}
	beta := &session.Instance{ID: "beta-id", Title: "beta", ProjectPath: "/tmp/proj"}
	instances := []*session.Instance{alpha, beta}

	conflict := titleConflictAt(instances, beta, "alpha")
	if conflict == nil {
		t.Fatal("renaming beta onto alpha at the same location was allowed; add and launch both refuse it")
	}
	if conflict.ID != "alpha-id" {
		t.Fatalf("conflict names %s, want alpha-id — the message must name the existing session so the user can act on it", conflict.ID)
	}
}

// TestTitleConflictAt_AllowsSameTitleAtDifferentLocation keeps the useful case:
// one title per location, not one title globally.
func TestTitleConflictAt_AllowsSameTitleAtDifferentLocation(t *testing.T) {
	alpha := &session.Instance{ID: "alpha-id", Title: "alpha", ProjectPath: "/tmp/proj-a"}
	beta := &session.Instance{ID: "beta-id", Title: "beta", ProjectPath: "/tmp/proj-b"}
	instances := []*session.Instance{alpha, beta}

	if conflict := titleConflictAt(instances, beta, "alpha"); conflict != nil {
		t.Fatalf("renaming to a title held at a DIFFERENT location was refused (conflict=%s)", conflict.ID)
	}
}

// TestCheckTitleConflict_AllowsRenameToOwnTitle: a no-op rename must not report
// the session as its own conflict.
func TestCheckTitleConflict_AllowsRenameToOwnTitle(t *testing.T) {
	alpha := &session.Instance{ID: "alpha-id", Title: "alpha", ProjectPath: "/tmp/proj"}
	instances := []*session.Instance{alpha}

	if msg, _ := checkTitleConflict(instances, alpha, "alpha"); msg != "" {
		t.Fatalf("a session was reported as a conflict with itself: %s", msg)
	}
}

// TestTitleConflictAt_RemoteSessionsOnDifferentHostsDoNotConflict: the same
// location rule the rest of the epic uses — a remote session's ProjectPath is a
// placeholder, so two remote sessions sharing one must not block each other.
func TestTitleConflictAt_RemoteSessionsOnDifferentHostsDoNotConflict(t *testing.T) {
	a := sshInstance("a-id", "alpha", "alice@host-a", "/srv/app-a")
	b := sshInstance("b-id", "beta", "bob@host-b", "/opt/app-b")
	instances := []*session.Instance{a, b}

	if conflict := titleConflictAt(instances, b, "alpha"); conflict != nil {
		t.Fatalf("two remote sessions on different hosts blocked each other's title (conflict=%s)", conflict.ID)
	}
}

// TestTitleConflictAt_SameRemoteLocationConflicts is the remote half of the
// refusal.
func TestTitleConflictAt_SameRemoteLocationConflicts(t *testing.T) {
	a := &session.Instance{
		ID: "a-id", Title: "alpha", ProjectPath: "/home/dev/cwd-one",
		SSHHost: "alice@host-a", SSHRemotePath: "/srv/app",
	}
	b := &session.Instance{
		ID: "b-id", Title: "beta", ProjectPath: "/home/dev/cwd-two",
		SSHHost: "alice@host-a", SSHRemotePath: "/srv/app",
	}
	instances := []*session.Instance{a, b}

	conflict := titleConflictAt(instances, b, "alpha")
	if conflict == nil {
		t.Fatal("two sessions at the same host and remote path were allowed to share a title")
	}
	if conflict.ID != "a-id" {
		t.Fatalf("conflict names %s, want a-id", conflict.ID)
	}
}

// TestCheckTitleConflict_UsesAlreadyExistsCode pins the CLI contract shared by
// all three commands that can refuse this (review finding F8): `add`, `rename`
// and `session set <id> title` must answer with one code, or a --json consumer
// has to special-case whichever is different. `rename` used to answer
// INVALID_OPERATION.
func TestCheckTitleConflict_UsesAlreadyExistsCode(t *testing.T) {
	alpha := &session.Instance{ID: "alpha-id", Title: "alpha", ProjectPath: "/tmp/proj"}
	beta := &session.Instance{ID: "beta-id", Title: "beta", ProjectPath: "/tmp/proj"}
	instances := []*session.Instance{alpha, beta}

	msg, code := checkTitleConflict(instances, beta, "alpha")
	if code != ErrCodeAlreadyExists {
		t.Errorf("code = %q, want %q", code, ErrCodeAlreadyExists)
	}
	for _, want := range []string{"alpha", "alpha-id", "/tmp/proj"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not name %q: %s", want, msg)
		}
	}

	// The `add` path must answer with the same code for the same condition.
	d := decideAddTitle(instances, "alpha", localLocation("/tmp/proj"), true)
	_, addCode := d.DuplicateError()
	if addCode != code {
		t.Errorf("add answers %q where rename/session-set answer %q for the identical collision", addCode, code)
	}
}

// TestCheckTitleConflict_RemoteMessageNamesTheRemoteLocation: the refusal has to
// tell the user WHERE the collision is, and for a remote session that is the
// host and remote dir.
func TestCheckTitleConflict_RemoteMessageNamesTheRemoteLocation(t *testing.T) {
	a := sshInstance("a-id", "alpha", "alice@host-a", "/srv/app")
	b := sshInstance("b-id", "beta", "alice@host-a", "/srv/app")

	msg, _ := checkTitleConflict([]*session.Instance{a, b}, b, "alpha")
	if !strings.Contains(msg, "alice@host-a:/srv/app") {
		t.Errorf("refusal does not name the remote location: %s", msg)
	}
	if strings.Contains(msg, controllerCWD) {
		t.Errorf("refusal names the local placeholder %s: %s", controllerCWD, msg)
	}
}
