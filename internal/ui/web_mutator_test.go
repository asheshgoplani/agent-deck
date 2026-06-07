package ui

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// These tests exercise the REAL *WebMutator.EditSession / .MoveSessionToGroup
// against a Home backed by an isolated SQLite storage profile (temp HOME so we
// never touch the user's real ~/.agent-deck state.db). The handler tests in
// internal/web use a fakeMutator, so the AutoName-clear and the
// SetGeminiYoloMode setter path (which is tool-gated to "gemini") are only
// guaranteed by exercising the production code here.

func wmStrPtr(s string) *string { return &s }
func wmBoolPtr(b bool) *bool    { return &b }

// newWebMutatorTestHarness builds a *WebMutator over a Home whose instances /
// instanceByID / groupTree are seeded with the supplied instances under an
// isolated storage profile. The profile is unique per test so SaveWithGroups
// writes to a throwaway state.db. Returns the mutator and the seeded tree.
func newWebMutatorTestHarness(t *testing.T, profile string, insts ...*session.Instance) (*WebMutator, *session.GroupTree) {
	t.Helper()

	// Isolate storage: NewStorageWithProfile walks HOME + .agent-deck.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	tree := session.NewGroupTree(insts)

	byID := make(map[string]*session.Instance, len(insts))
	for _, in := range insts {
		byID[in.ID] = in
	}

	h := &Home{
		profile:      profile,
		instances:    insts,
		instanceByID: byID,
		groupTree:    tree,
	}
	return NewWebMutator(h), tree
}

func TestWebMutator_EditSession_TitleClearsAutoName(t *testing.T) {
	inst := session.NewInstanceWithTool("auto-handle", "/tmp/wm-rename", "claude")
	inst.AutoName = true

	m, _ := newWebMutatorTestHarness(t, "_wm_rename", inst)

	if err := m.EditSession(inst.ID, web.SessionPatch{Title: wmStrPtr("new name")}); err != nil {
		t.Fatalf("EditSession: %v", err)
	}
	if inst.Title != "new name" {
		t.Errorf("Title = %q, want %q", inst.Title, "new name")
	}
	if inst.AutoName {
		t.Errorf("AutoName = true, want false (a user-chosen title replaces the auto handle)")
	}
}

func TestWebMutator_EditSession_AppliesScalarFields(t *testing.T) {
	inst := session.NewInstanceWithTool("orig", "/tmp/wm-scalar", "claude")
	inst.Notes = "old notes"
	inst.Color = "#000000"
	inst.GeminiModel = "old-model"

	m, _ := newWebMutatorTestHarness(t, "_wm_scalar", inst)

	// Patch Notes, Color, GeminiModel only — leave Title/Channels/etc nil.
	if err := m.EditSession(inst.ID, web.SessionPatch{
		Notes:       wmStrPtr("new notes"),
		Color:       wmStrPtr("#ff0000"),
		GeminiModel: wmStrPtr("gemini-2.5-pro"),
	}); err != nil {
		t.Fatalf("EditSession: %v", err)
	}

	if inst.Notes != "new notes" {
		t.Errorf("Notes = %q, want %q", inst.Notes, "new notes")
	}
	if inst.Color != "#ff0000" {
		t.Errorf("Color = %q, want %q", inst.Color, "#ff0000")
	}
	if inst.GeminiModel != "gemini-2.5-pro" {
		t.Errorf("GeminiModel = %q, want %q", inst.GeminiModel, "gemini-2.5-pro")
	}
	// Fields left nil in the patch must be UNCHANGED.
	if inst.Title != "orig" {
		t.Errorf("Title changed unexpectedly: %q, want %q (patch field was nil)", inst.Title, "orig")
	}
}

func TestWebMutator_EditSession_GeminiYoloUsesSetter(t *testing.T) {
	// SetGeminiYoloMode is tool-gated: it only applies for Tool == "gemini".
	// The setter path is what production uses; a direct field write would
	// skip the tmux-env sync. Seeding a gemini instance proves the setter ran.
	inst := session.NewInstanceWithTool("gem", "/tmp/wm-yolo", "gemini")
	if inst.GeminiYoloMode != nil {
		t.Fatalf("precondition: GeminiYoloMode should start nil, got %v", *inst.GeminiYoloMode)
	}

	m, _ := newWebMutatorTestHarness(t, "_wm_yolo", inst)

	if err := m.EditSession(inst.ID, web.SessionPatch{GeminiYolo: wmBoolPtr(true)}); err != nil {
		t.Fatalf("EditSession: %v", err)
	}
	if inst.GeminiYoloMode == nil {
		t.Fatalf("GeminiYoloMode = nil, want non-nil (setter should have run for a gemini tool)")
	}
	if !*inst.GeminiYoloMode {
		t.Errorf("GeminiYoloMode = false, want true")
	}
}

func TestWebMutator_MoveSessionToGroup(t *testing.T) {
	// Seed an instance in group "A". Two distinct groups so the move is real.
	inst := session.NewInstanceWithGroupAndTool("mover", "/tmp/wm-move", "A", "claude")

	m, tree := newWebMutatorTestHarness(t, "_wm_move", inst)
	// Pre-create group "B" so it's a true cross-group move, not group creation.
	tree.CreateGroup("B")

	if err := m.MoveSessionToGroup(inst.ID, "B"); err != nil {
		t.Fatalf("MoveSessionToGroup: %v", err)
	}

	if inst.GroupPath != "B" {
		t.Errorf("inst.GroupPath = %q, want %q", inst.GroupPath, "B")
	}
	// The group tree must reflect the move: the instance is now in B's session
	// list and gone from A's.
	if grpB, ok := tree.Groups["B"]; !ok {
		t.Fatalf("group B missing from tree after move")
	} else {
		found := false
		for _, s := range grpB.Sessions {
			if s.ID == inst.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("instance %s not found in group B sessions after move", inst.ID)
		}
	}
	if grpA, ok := tree.Groups["A"]; ok {
		for _, s := range grpA.Sessions {
			if s.ID == inst.ID {
				t.Errorf("instance %s still present in group A after move", inst.ID)
			}
		}
	}
}

func TestWebMutator_EditSession_NotFound(t *testing.T) {
	m, _ := newWebMutatorTestHarness(t, "_wm_nf_edit") // no instances seeded

	if err := m.EditSession("does-not-exist", web.SessionPatch{Notes: wmStrPtr("x")}); err == nil {
		t.Fatalf("EditSession on missing id: expected error, got nil")
	}
}

func TestWebMutator_MoveSessionToGroup_NotFound(t *testing.T) {
	m, _ := newWebMutatorTestHarness(t, "_wm_nf_move") // no instances seeded

	if err := m.MoveSessionToGroup("does-not-exist", "B"); err == nil {
		t.Fatalf("MoveSessionToGroup on missing id: expected error, got nil")
	}
}

// TestWebMutator_ArchiveSession_StopsProcess pins the process-lifecycle half of
// archive parity. The interface contract (web.SessionMutator.ArchiveSession)
// says it "Mirrors the TUI 'A' hotkey / CLI session archive" — both of which
// stop the agent process before hiding the row. The parity suite only checks
// wire/data parity (the `archived` flag), so a missing Kill() slips through.
// On a tmux-less seeded instance, Kill() resolves to Status=StatusStopped, so
// status is the observable proxy for "the process was torn down".
func TestWebMutator_ArchiveSession_StopsProcess(t *testing.T) {
	inst := session.NewInstanceWithTool("archive-me", "/tmp/wm-archive", "claude")
	inst.Status = session.StatusRunning

	m, _ := newWebMutatorTestHarness(t, "_wm_archive", inst)

	if err := m.ArchiveSession(inst.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	if inst.ArchivedAt.IsZero() {
		t.Errorf("ArchivedAt not set; archive did not record the timestamp")
	}
	if got := inst.GetStatusThreadSafe(); got != session.StatusStopped {
		t.Errorf("status = %q after web archive, want %q (archive must stop the process to mirror the TUI 'A' hotkey / CLI `session archive`)", got, session.StatusStopped)
	}
}
