package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/1850.
//
//  1. An exact duplicate registration was reported as success (exit 0, --json
//     ignored entirely), so a script could not tell "created" from "already
//     existed". `launch` already reported ALREADY_EXISTS and exited 1.
//  2. A same-location registration without -t renamed silently, leaving a
//     mystery "project (2)" with no trace of where it came from.
//  3. --ssh sessions collided with each other even when genuinely different,
//     and the rename message named the local placeholder directory.

// --- 1. Exact duplicate ------------------------------------------------------

// TestDecideAddTitle_ExactDuplicateIsRefused pins that an exact duplicate is a
// refusal carrying the existing session, not a silent success.
func TestDecideAddTitle_ExactDuplicateIsRefused(t *testing.T) {
	instances := []*session.Instance{
		{ID: "existing-1", Title: "dup", ProjectPath: "/path/to/project"},
	}

	d := decideAddTitle(instances, "dup", localLocation("/path/to/project"), true)

	if d.Duplicate == nil {
		t.Fatal("exact duplicate (same title, same location) was not refused")
	}
	if d.Duplicate.ID != "existing-1" {
		t.Fatalf("duplicate names session %s, want existing-1", d.Duplicate.ID)
	}
	if d.RenamedFrom != "" {
		t.Errorf("an explicit -t must never be auto-renamed; got RenamedFrom=%q", d.RenamedFrom)
	}
}

// TestDecideAddTitle_DuplicateUsesAlreadyExistsCode pins the CLI contract the
// issue asks for: the same ALREADY_EXISTS code `launch` uses, with a message
// naming the existing session so a caller can act on it.
func TestDecideAddTitle_DuplicateUsesAlreadyExistsCode(t *testing.T) {
	instances := []*session.Instance{
		{ID: "existing-1", Title: "dup", ProjectPath: "/path/to/project"},
	}
	d := decideAddTitle(instances, "dup", localLocation("/path/to/project"), true)

	msg, code := d.DuplicateError()
	if code != ErrCodeAlreadyExists {
		t.Errorf("duplicate error code = %q, want %q", code, ErrCodeAlreadyExists)
	}
	for _, want := range []string{"dup", "existing-1", "/path/to/project"} {
		if !strings.Contains(msg, want) {
			t.Errorf("duplicate message does not name %q: %s", want, msg)
		}
	}
}

// TestDecideAddTitle_DuplicateJSONPayload pins that --json gets a structured
// signal rather than being ignored on this path.
func TestDecideAddTitle_DuplicateJSONPayload(t *testing.T) {
	instances := []*session.Instance{
		{ID: "existing-1", Title: "dup", ProjectPath: "/path/to/project"},
	}
	d := decideAddTitle(instances, "dup", localLocation("/path/to/project"), true)

	out := NewCLIOutput(true, false)
	raw := captureStdout(t, func() {
		msg, code := d.DuplicateError()
		out.ErrorWithData(msg, code, d.DuplicateJSONFields())
	})

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("--json duplicate output is not JSON (%v): %s", err, raw)
	}
	if payload["success"] != false {
		t.Errorf("payload success = %v, want false", payload["success"])
	}
	if payload["code"] != ErrCodeAlreadyExists {
		t.Errorf("payload code = %v, want %s", payload["code"], ErrCodeAlreadyExists)
	}
	if payload["existing_id"] != "existing-1" {
		t.Errorf("payload existing_id = %v, want existing-1", payload["existing_id"])
	}
	if payload["location"] != "/path/to/project" {
		t.Errorf("payload location = %v, want /path/to/project", payload["location"])
	}
}

// TestDecideAddTitle_RemoteDuplicateJSONNamesTheRemote: a machine consumer of a
// remote duplicate needs the host and remote dir, not the placeholder.
func TestDecideAddTitle_RemoteDuplicateJSONNamesTheRemote(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "shared", "alice@host-a", "/srv/app-a")}
	d := decideAddTitle(instances, "shared", remoteLocation("alice@host-a", "/srv/app-a"), true)

	fields := d.DuplicateJSONFields()
	if fields["ssh_host"] != "alice@host-a" {
		t.Errorf("ssh_host = %v, want alice@host-a", fields["ssh_host"])
	}
	if fields["ssh_remote_path"] != "/srv/app-a" {
		t.Errorf("ssh_remote_path = %v, want /srv/app-a", fields["ssh_remote_path"])
	}
	if fields["location"] != "alice@host-a:/srv/app-a" {
		t.Errorf("location = %v, want alice@host-a:/srv/app-a", fields["location"])
	}
}

// --- 2. Visible auto-rename --------------------------------------------------

// TestDecideAddTitle_SameLocationWithoutTitleIsRenamedVisibly keeps two agents
// on one checkout working while leaving a trace.
func TestDecideAddTitle_SameLocationWithoutTitleIsRenamedVisibly(t *testing.T) {
	instances := []*session.Instance{
		{ID: "existing-1", Title: "project", ProjectPath: "/path/to/project"},
	}

	d := decideAddTitle(instances, "project", localLocation("/path/to/project"), false)

	if d.Duplicate != nil {
		t.Fatal("a second session at the same location without -t must still be created")
	}
	if d.Title != "project (2)" {
		t.Fatalf("title = %q, want %q", d.Title, "project (2)")
	}
	if d.RenamedFrom != "project" {
		t.Fatalf("RenamedFrom = %q, want %q — the rename must leave a trace", d.RenamedFrom, "project")
	}

	warning := d.RenameWarning()
	for _, want := range []string{"project", "project (2)", "/path/to/project"} {
		if !strings.Contains(warning, want) {
			t.Errorf("rename warning does not name %q: %s", want, warning)
		}
	}
}

// TestDecideAddTitle_NoRenameWhenLocationIsFree guards the silent common case.
func TestDecideAddTitle_NoRenameWhenLocationIsFree(t *testing.T) {
	d := decideAddTitle(nil, "project", localLocation("/path/to/project"), false)

	if d.Title != "project" || d.RenamedFrom != "" || d.Duplicate != nil {
		t.Fatalf("a first registration must be silent: %+v", d)
	}
	if d.RenameWarning() != "" {
		t.Errorf("RenameWarning() must be empty when nothing was renamed, got %q", d.RenameWarning())
	}
}

// --- 3. --ssh sessions -------------------------------------------------------

// TestDecideAddTitle_RemoteSessionsOnDifferentHostsAreNotDuplicates is the exact
// scenario from the report: same title, different host, different remote
// directory, both registered from one local directory.
func TestDecideAddTitle_RemoteSessionsOnDifferentHostsAreNotDuplicates(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "shared", "alice@host-a", "/srv/app-a")}

	d := decideAddTitle(instances, "shared", remoteLocation("bob@host-b", "/opt/app-b"), true)

	if d.Duplicate != nil {
		t.Fatalf("bob@host-b:/opt/app-b refused as a duplicate of %s — different host, different remote dir", d.Duplicate.ID)
	}
	if d.RenamedFrom != "" {
		t.Errorf("no rename should fire either; got RenamedFrom=%q", d.RenamedFrom)
	}
}

// TestDecideAddTitle_SameHostDifferentRemotePathsAreNotDuplicates covers the
// other half of requirement 3.
func TestDecideAddTitle_SameHostDifferentRemotePathsAreNotDuplicates(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "shared", "alice@host-a", "/srv/app-a")}

	d := decideAddTitle(instances, "shared", remoteLocation("alice@host-a", "/srv/app-b"), true)
	if d.Duplicate != nil {
		t.Fatalf("same host but a different remote path was refused as a duplicate of %s", d.Duplicate.ID)
	}
}

// TestDecideAddTitle_LocalAndRemoteAtOnePathAreNeverDuplicates: a local session
// at the placeholder path and a remote session registered from it are in
// different places.
func TestDecideAddTitle_LocalAndRemoteAtOnePathAreNeverDuplicates(t *testing.T) {
	instances := []*session.Instance{
		{ID: "local-1", Title: "shared", ProjectPath: controllerCWD},
	}

	d := decideAddTitle(instances, "shared", remoteLocation("alice@host-a", controllerCWD), true)
	if d.Duplicate != nil {
		t.Fatalf("a remote session was refused as a duplicate of the LOCAL session %s at the same path string", d.Duplicate.ID)
	}
}

// TestDecideAddTitle_RemoteDuplicateNamesHostAndRemoteDir pins the message
// content: naming the local placeholder tells the user nothing about the
// collision.
func TestDecideAddTitle_RemoteDuplicateNamesHostAndRemoteDir(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "shared", "alice@host-a", "/srv/app-a")}

	d := decideAddTitle(instances, "shared", remoteLocation("alice@host-a", "/srv/app-a"), true)
	if d.Duplicate == nil {
		t.Fatal("same title at the same host and remote path must still be a duplicate")
	}

	msg, _ := d.DuplicateError()
	if !strings.Contains(msg, "alice@host-a:/srv/app-a") {
		t.Errorf("duplicate message does not name the remote location: %s", msg)
	}
	if strings.Contains(msg, controllerCWD) {
		t.Errorf("duplicate message names the local placeholder %s, which has nothing to do with the collision: %s", controllerCWD, msg)
	}
}

// TestDecideAddTitle_RemoteRenameWarningNamesRemoteLocation covers the same for
// the auto-rename path (required behaviour 2).
func TestDecideAddTitle_RemoteRenameWarningNamesRemoteLocation(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "app", "alice@host-a", "/srv/app-a")}

	d := decideAddTitle(instances, "app", remoteLocation("alice@host-a", "/srv/app-a"), false)
	if d.RenamedFrom != "app" || d.Title != "app (2)" {
		t.Fatalf("expected a visible rename at the same remote location, got %+v", d)
	}
	warning := d.RenameWarning()
	if !strings.Contains(warning, "alice@host-a:/srv/app-a") {
		t.Errorf("rename warning does not name the remote location: %s", warning)
	}
	if strings.Contains(warning, controllerCWD) {
		t.Errorf("rename warning names the local placeholder %s: %s", controllerCWD, warning)
	}
}

// TestDecideAddTitle_RemoteHomeSpellingsAreOneLocation ties the CLI decision to
// the canonical-path rule: `--remote-path ~` and no --remote-path at all name
// the same directory, so they must collide with each other.
func TestDecideAddTitle_RemoteHomeSpellingsAreOneLocation(t *testing.T) {
	instances := []*session.Instance{sshInstance("existing-1", "home", "alice@host-a", "")}

	d := decideAddTitle(instances, "home", remoteLocation("alice@host-a", "~"), true)
	if d.Duplicate == nil {
		t.Fatal(`--remote-path "~" was not recognised as the same location as no --remote-path, but both run in the remote home`)
	}

	// ...while a directory UNDER the remote home is its own place.
	d = decideAddTitle(instances, "home", remoteLocation("alice@host-a", "~/work"), true)
	if d.Duplicate != nil {
		t.Fatalf("~/work was folded onto the remote home itself (conflict with %s)", d.Duplicate.ID)
	}
}
