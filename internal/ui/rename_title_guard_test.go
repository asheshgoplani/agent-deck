package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// homeWithSessions is a Home holding the given sessions, no storage.
func homeWithSessions(t *testing.T, instances ...*session.Instance) *Home {
	t.Helper()
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil
	home.instancesMu.Lock()
	home.instances = instances
	for _, inst := range instances {
		home.instanceByID[inst.ID] = inst
	}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	home.rebuildFlatItems()
	return home
}

// The TUI rename (r) accepted a title another session already holds at the
// same path; `agent-deck rename` refuses it. Two sessions with one title at
// one path can no longer be addressed by title. The TUI refuses it too, with
// the CLI's message.
func TestHomeRenameSession_RefusesTitleTakenAtTheSamePath(t *testing.T) {
	taken := session.NewInstance("s1", "/tmp/p1")
	target := session.NewInstance("s2", "/tmp/p1")
	elsewhere := session.NewInstance("s3", "/tmp/p2")
	home := homeWithSessions(t, taken, target, elsewhere)

	rename := func(inst *session.Instance, title string) *Home {
		t.Helper()
		for i, item := range home.flatItems {
			if item.Type == session.ItemTypeSession && item.Session.ID == inst.ID {
				home.cursor = i
			}
		}
		home.setError(nil)
		home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
		if home.groupDialog.Mode() != GroupDialogRenameSession || home.groupDialog.GetSessionID() != inst.ID {
			t.Fatalf("rename dialog not open on %s", inst.Title)
		}
		home.groupDialog.nameInput.SetValue(title)
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return model.(*Home)
	}

	h := rename(target, "s1")
	if target.Title != "s2" {
		t.Errorf("rename onto a title taken at the same path was applied: title = %q, want s2", target.Title)
	}
	if h.err == nil || !strings.Contains(h.err.Error(), `session with title "s1" already exists at`) {
		t.Errorf("rename refusal not shown: err = %v", h.err)
	}

	// The same title at another path is free, as on the CLI.
	h = rename(elsewhere, "s1")
	if elsewhere.Title != "s1" || h.err != nil {
		t.Errorf("rename to a title held at another path: title = %q, err = %v; want s1, nil", elsewhere.Title, h.err)
	}
}

// The web PATCH /api/sessions/{id} goes through WebMutator.UpdateSession: it
// refuses the same rename and writes no field of the request.
func TestWebMutatorUpdateSession_RefusesTitleTakenAtTheSamePath(t *testing.T) {
	taken := session.NewInstance("Alpha API", "/tmp/alpha")
	target := session.NewInstance("beta", "/tmp/alpha")
	home := homeWithSessions(t, taken, target)
	m := &WebMutator{h: home}

	_, _, err := m.UpdateSession(target.ID, map[string]string{
		session.FieldTitle: "Alpha API",
		session.FieldNotes: "must not be written",
	})
	if err == nil || !strings.Contains(err.Error(), `session with title "Alpha API" already exists at`) {
		t.Errorf("UpdateSession err = %v, want the CLI's already-exists refusal", err)
	}
	if target.Title != "beta" {
		t.Errorf("title = %q after a refused rename, want beta", target.Title)
	}
	if target.Notes != "" {
		t.Errorf("notes = %q: a refused rename wrote another field of the same request", target.Notes)
	}
}

// The edit dialog (Shift+P) is the TUI's other rename path: it refuses the
// same title, keeps the dialog open with the message, and writes no field.
func TestHomeEditSessionDialog_RefusesTitleTakenAtTheSamePath(t *testing.T) {
	taken := session.NewInstance("s1", "/tmp/p1")
	target := session.NewInstance("s2", "/tmp/p1")
	home := homeWithSessions(t, taken, target)

	home.editSessionDialog.SetSize(home.width, home.height)
	home.editSessionDialog.Show(target)
	set := map[string]string{session.FieldTitle: "s1", session.FieldNotes: "must not be written"}
	for i := range home.editSessionDialog.fields {
		f := &home.editSessionDialog.fields[i]
		if v, ok := set[f.key]; ok {
			f.input.SetValue(v)
			delete(set, f.key)
		}
	}
	if _, ok := set[session.FieldTitle]; ok {
		t.Fatal("edit dialog has no title field")
	}
	home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})

	if target.Title != "s2" {
		t.Errorf("edit dialog renamed onto a title taken at the same path: title = %q, want s2", target.Title)
	}
	if target.Notes != "" {
		t.Errorf("notes = %q: a refused rename wrote another field of the same edit", target.Notes)
	}
	if !home.editSessionDialog.IsVisible() || !strings.Contains(home.editSessionDialog.validationErr, `session with title "s1" already exists at`) {
		t.Errorf("edit dialog visible=%v err=%q, want it open with the already-exists refusal", home.editSessionDialog.IsVisible(), home.editSessionDialog.validationErr)
	}
}
