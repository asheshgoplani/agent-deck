package session

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A persisted conversation id belongs to one tool, running one command, at one
// location. Refusing to RESUME a mismatched id is only half a guard, because
// the id also lives in the pane's environment: the pane outlives the settings
// it was launched under, so after a scope change it still exports the old
// tool's id, still answers a live read, and rebinding it to the new scope turns
// a refusal into an acceptance one poll later.
//
// These pin the three ways a live session can move out from under its binding.

const invalidationToolConfig = `
[tools.toola]
command = "toola"
resume_flag = "--resume"
session_id_env = "TOOLA_SESSION_ID"

[tools.toolb]
command = "toolb"
resume_flag = "--resume"
session_id_env = "TOOLB_SESSION_ID"
`

func newBoundGenericInstance(t *testing.T, id string) *Instance {
	t.Helper()

	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), invalidationToolConfig)

	storage := newTestStorage(t)
	prev := statedb.GetGlobal()
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstance("binding-target", "/tmp/proj")
	inst.Tool = "toola"
	inst.Command = "toola"
	inst.GenericSessionID = id
	inst.GenericDetectedAt = time.Unix(1_700_000_000, 0).UTC()
	scope := inst.currentGenericSessionScope()
	inst.GenericSessionTool = scope.Tool
	inst.GenericSessionCommand = scope.Command
	inst.GenericSessionLocation = scope.Location

	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if got := inst.GetGenericSessionID(); got != id {
		t.Fatalf("fixture: GetGenericSessionID() = %q, want %q", got, id)
	}
	return inst
}

func TestGenericBinding_ToolChangeInvalidates(t *testing.T) {
	inst := newBoundGenericInstance(t, "sid-owned-by-toola")

	if _, _, err := SetField(inst, FieldTool, "toolb", nil); err != nil {
		t.Fatalf("SetField(tool): %v", err)
	}

	if inst.GenericSessionID != "" {
		t.Errorf("binding survived a tool change: id = %q. A restart would hand toola's "+
			"conversation to toolb", inst.GenericSessionID)
	}
	if got := inst.GetGenericSessionID(); got != "" {
		t.Errorf("GetGenericSessionID() = %q after a tool change, want \"\"", got)
	}
	if !inst.genericSessionIDCleared {
		t.Error("intentional-clear flag not set, so the next save lets sticky merge resurrect the binding")
	}
}

func TestGenericBinding_CommandChangeInvalidates(t *testing.T) {
	inst := newBoundGenericInstance(t, "sid-owned-by-original-command")

	// The tool NAME is unchanged; only the executable behind it moved. Scoping
	// on the tool alone would call this binding eligible.
	if _, _, err := SetField(inst, FieldCommand, "toola-next --flag", nil); err != nil {
		t.Fatalf("SetField(command): %v", err)
	}

	if inst.GenericSessionID != "" {
		t.Errorf("binding survived a command change: id = %q. A cold restart would supply the "+
			"old CLI's conversation id to the new executable", inst.GenericSessionID)
	}
	if got := inst.GetGenericSessionID(); got != "" {
		t.Errorf("GetGenericSessionID() = %q after a command change, want \"\"", got)
	}
}

func TestGenericBinding_LocationChangeInvalidates(t *testing.T) {
	inst := newBoundGenericInstance(t, "sid-captured-here")

	if _, _, err := SetField(inst, FieldPath, t.TempDir(), nil); err != nil {
		t.Fatalf("SetField(path): %v", err)
	}

	if inst.GenericSessionID != "" {
		t.Errorf("binding survived a location change: id = %q", inst.GenericSessionID)
	}
	if got := inst.GetGenericSessionID(); got != "" {
		t.Errorf("GetGenericSessionID() = %q after a location change, want \"\"", got)
	}
}

// TestGenericBinding_InvalidationErasesTheOldToolsVariable is the half that
// makes the invalidation stick. The variables to erase are the ones the tool
// that CAPTURED the id declared: after a tool change the current tool's name
// list no longer mentions them, and an unerased variable is exactly what the
// pane hands back on the next read.
func TestGenericBinding_InvalidationErasesTheOldToolsVariable(t *testing.T) {
	inst := newBoundGenericInstance(t, "sid-in-the-pane")

	names := inst.genericSessionEnvNamesFor("toola", "toolb")
	want := map[string]bool{
		"TOOLA_SESSION_ID":          false,
		"TOOLB_SESSION_ID":          false,
		genericCapturedSessionIDEnv: false,
	}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected variable %q in erase set %v", n, names)
			continue
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("erase set %v omits %q: an id left in that variable is re-adopted on the "+
				"next read, so the invalidation does not hold", names, n)
		}
	}
}

// TestGenericBinding_ScopedIDIsNotRelaunderedThroughThePane pins the loop the
// review found. GetGenericSessionID used to read the pane BEFORE checking
// scope, so a mismatched binding was re-adopted under the new scope on the very
// next call — and SyncSessionIDsToTmux published it back, closing the loop.
func TestGenericBinding_ScopedIDIsNotRelaunderedThroughThePane(t *testing.T) {
	inst := newBoundGenericInstance(t, "sid-must-not-be-laundered")

	// A scope change that has NOT gone through SetField — a row written by an
	// older build, or a field touched directly. The read side must still refuse,
	// because the pane is not a source of authority for a binding under dispute.
	inst.Tool = "toolb"

	if got := inst.GetGenericSessionID(); got != "" {
		t.Fatalf("GetGenericSessionID() = %q under a mismatched scope, want \"\"", got)
	}
	if inst.GenericSessionTool != "toola" {
		t.Errorf("recorded scope was rewritten to %q by a refused read: one more poll and the "+
			"refusal becomes an acceptance", inst.GenericSessionTool)
	}
	if inst.GenericSessionID != "sid-must-not-be-laundered" {
		t.Errorf("stored id changed to %q during a refused read", inst.GenericSessionID)
	}
}

// TestGenericCapture_SSHSessionDoesNotPublishToTheRemoteTmux pins the second
// P1. prepareCommand hands the capture fragment to wrapForSSH for an --ssh
// session, so a tmux call inside it runs on the REMOTE host, finds no server,
// and is swallowed by the `|| true` — durability in appearance only.
func TestGenericCapture_SSHSessionDoesNotPublishToTheRemoteTmux(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), captureOnlyToolConfig)

	local := NewInstance("capture-local", "/tmp/proj")
	local.Tool = "capturetool"
	localCmd := local.buildGenericCommand("capturetool")
	if !strings.Contains(localCmd, "set-environment "+genericCapturedSessionIDEnv) {
		t.Fatalf("local session lost its publish:\n%s", localCmd)
	}

	remote := NewInstance("capture-remote", "/tmp/proj")
	remote.Tool = "capturetool"
	remote.SSHHost = "alice@remote"
	remote.SSHRemotePath = "/srv/app"
	remoteCmd := remote.buildGenericCommand("capturetool")

	if strings.Contains(remoteCmd, "set-environment") {
		t.Errorf("an --ssh session emits a tmux publish that would run on the remote host, where "+
			"there is no agent-deck tmux server to hold it:\n%s", remoteCmd)
	}
	// The capture and resume themselves still work; only the publish is dropped.
	if !strings.Contains(remoteCmd, "--resume") {
		t.Errorf("remote capture command lost its resume:\n%s", remoteCmd)
	}
}
