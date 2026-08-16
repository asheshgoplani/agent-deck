package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const scopeToolConfig = `
[tools.mytool]
command = "mytool"
resume_flag = "--resume"
`

// newScopedGenericInstance returns a saved custom-tool session holding a
// persisted conversation id bound to its current tool and location.
func newScopedGenericInstance(t *testing.T, id string) (*Storage, *Instance) {
	t.Helper()

	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), scopeToolConfig)

	storage := newTestStorage(t)
	inst := NewInstance("scope-target", "/tmp/proj")
	inst.Tool = "mytool"
	inst.GenericSessionID = id
	inst.GenericDetectedAt = time.Unix(1_700_000_000, 0).UTC()
	scope := inst.currentGenericSessionScope()
	inst.GenericSessionTool = scope.Tool
	inst.GenericSessionCommand = scope.Command
	inst.GenericSessionLocation = scope.Location

	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	return storage, inst
}

// TestGenericSessionScope_RoundTripsAndResumes is the baseline: a binding made
// here is still resumable after a full save/reload, which is the entire point
// of persisting it.
func TestGenericSessionScope_RoundTripsAndResumes(t *testing.T) {
	storage, inst := newScopedGenericInstance(t, "sid-scope-ok")

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var got *Instance
	for _, l := range loaded {
		if l.ID == inst.ID {
			got = l
		}
	}
	if got == nil {
		t.Fatal("instance missing after reload")
	}
	if got.GenericSessionTool != "mytool" || got.GenericSessionLocation == "" {
		t.Fatalf("scope did not survive the round trip: tool=%q location=%q",
			got.GenericSessionTool, got.GenericSessionLocation)
	}
	if id := got.GetGenericSessionID(); id != "sid-scope-ok" {
		t.Errorf("GetGenericSessionID() = %q, want the persisted id: an unchanged session must "+
			"still resume its own conversation after a reboot", id)
	}
}

// TestGenericSessionScope_RefusesAfterToolChange is the contract. A [tools.*]
// entry is operator-defined and a session's tool can be repointed; the id
// captured from one CLI must not be replayed into another, which would resume
// an unrelated conversation or fail in a way that looks like data loss.
func TestGenericSessionScope_RefusesAfterToolChange(t *testing.T) {
	_, inst := newScopedGenericInstance(t, "sid-belongs-to-mytool")

	inst.Tool = "othertool"

	if id := inst.GetGenericSessionID(); id != "" {
		t.Errorf("GetGenericSessionID() = %q after the tool changed, want \"\": the id belongs to "+
			"the tool that captured it", id)
	}
	// The binding is kept, not destroyed: pointing the session back at its own
	// tool has to make it resumable again.
	if inst.GenericSessionID != "sid-belongs-to-mytool" {
		t.Errorf("stored id = %q, want it left intact for a later move back", inst.GenericSessionID)
	}
	inst.Tool = "mytool"
	if id := inst.GetGenericSessionID(); id != "sid-belongs-to-mytool" {
		t.Errorf("GetGenericSessionID() = %q after moving back, want the id to be eligible again", id)
	}
}

// TestGenericSessionScope_RefusesAfterCommandChange pins the command half of
// the scope at READ time, independently of the invalidation that SetField
// performs. The tool NAME is unchanged here; only the executable behind it
// moved, which is the case a tool-plus-location scope calls eligible. This is
// the guard for a row whose command changed without going through SetField —
// an older build, or a field written directly.
func TestGenericSessionScope_RefusesAfterCommandChange(t *testing.T) {
	_, inst := newScopedGenericInstance(t, "sid-owned-by-first-command")

	inst.Command = "mytool-next --flag"

	if id := inst.GetGenericSessionID(); id != "" {
		t.Errorf("GetGenericSessionID() = %q after the command changed, want \"\": the id belongs "+
			"to the executable that captured it, not to the tool name in front of it", id)
	}
	if inst.GenericSessionCommand != "" {
		t.Errorf("recorded command scope = %q, want the original binding left intact",
			inst.GenericSessionCommand)
	}
}

// TestGenericSessionScope_RefusesAfterLocationChange is the other half. A
// remote session's tool runs against the remote host's conversation store;
// comparing project paths cannot tell that apart from a local one, which is the
// mistake behind #1850-#1853.
func TestGenericSessionScope_RefusesAfterLocationChange(t *testing.T) {
	_, inst := newScopedGenericInstance(t, "sid-captured-locally")

	inst.SSHHost = "alice@remote"
	inst.SSHRemotePath = "/srv/app"

	if id := inst.GetGenericSessionID(); id != "" {
		t.Errorf("GetGenericSessionID() = %q for a session now running on %s, want \"\": the "+
			"conversation lives on whichever machine captured it", id, inst.SSHHost)
	}
}

// TestGenericSessionScope_AllowsUnscopedID pins the deliberate asymmetry. A
// DISAGREEING scope is a real conflict and is refused; a MISSING one only means
// the binding was written by something that did not state a scope, and refusing
// that would turn an internal inconsistency into a lost conversation.
func TestGenericSessionScope_AllowsUnscopedID(t *testing.T) {
	_, inst := newScopedGenericInstance(t, "sid-unscoped")
	inst.GenericSessionTool = ""
	inst.GenericSessionLocation = ""

	if id := inst.GetGenericSessionID(); id != "sid-unscoped" {
		t.Errorf("GetGenericSessionID() = %q for an id with no recorded scope, want it honored", id)
	}
}

// TestGenericSessionScope_FreshStartClearsScope keeps the two halves of the
// binding from drifting apart: a cleared id with a live scope left behind would
// let the next bind inherit a scope it never chose.
func TestGenericSessionScope_FreshStartClearsScope(t *testing.T) {
	_, inst := newScopedGenericInstance(t, "sid-to-clear")

	inst.clearSessionBindingForFreshStart()

	if inst.GenericSessionID != "" {
		t.Errorf("id = %q after a fresh start, want cleared", inst.GenericSessionID)
	}
	if inst.GenericSessionTool != "" || inst.GenericSessionCommand != "" || inst.GenericSessionLocation != "" {
		t.Errorf("scope survived the clear: tool=%q command=%q location=%q",
			inst.GenericSessionTool, inst.GenericSessionCommand, inst.GenericSessionLocation)
	}
	if !inst.genericSessionIDCleared {
		t.Error("intentional-clear flag not set, so a later save would let sticky merge resurrect the binding")
	}
}

// TestGenericSessionScope_ToolDataProtocol pins that the scope keys follow the
// same omission/explicit-empty rule as the id. Breaking that lets a writer that
// has not observed the binding either wipe the scope of a live one or leave a
// stale scope attached to a new id.
func TestGenericSessionScope_ToolDataProtocol(t *testing.T) {
	bound := WriteGenericSessionScopeToToolData(nil, "mytool", "mytool --flag", "local:/tmp/proj", false)
	tool, command, location := ReadGenericSessionScopeFromToolData(bound)
	if tool != "mytool" || command != "mytool --flag" || location != "local:/tmp/proj" {
		t.Fatalf("round trip = (%q, %q, %q), want (mytool, mytool --flag, local:/tmp/proj)", tool, command, location)
	}

	unaware := WriteGenericSessionScopeToToolData(bound, "", "", "", false)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(unaware, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["generic_session_tool"]; ok {
		t.Errorf("an unaware writer stated a scope: %s", unaware)
	}

	cleared := WriteGenericSessionScopeToToolData(bound, "", "", "", true)
	if err := json.Unmarshal(cleared, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["generic_session_tool"]) != `""` || string(m["generic_session_location"]) != `""` {
		t.Errorf("intentional clear must write explicit empties, got %s", cleared)
	}
}

// TestFingerprintSessionID pins the privacy property the review asked for: logs
// identify a conversation binding without carrying the id itself.
func TestFingerprintSessionID(t *testing.T) {
	const id = "019f683f-260d-7ae1-a84d-205234ea3184"

	fp := fingerprintSessionID(id)
	if fp == "" {
		t.Fatal("fingerprint of a real id is empty")
	}
	if strings.Contains(fp, id) {
		t.Errorf("fingerprint %q contains the conversation id", fp)
	}
	for _, part := range strings.Split(id, "-") {
		if strings.Contains(fp, part) {
			t.Errorf("fingerprint %q leaks the id fragment %q", fp, part)
		}
	}
	if fp != fingerprintSessionID(id) {
		t.Error("fingerprint is not stable, so two log lines about one binding would not correlate")
	}
	if fp == fingerprintSessionID(id+"x") {
		t.Error("two different ids share a fingerprint")
	}
	if fingerprintSessionID("") != "" {
		t.Error(`fingerprint of "" is non-empty, so "no id" would read as some particular id`)
	}
}
