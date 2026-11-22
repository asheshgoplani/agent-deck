package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/menudata"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func homeWithGroupedSessions(t *testing.T, instances ...*session.Instance) *Home {
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

// DELETE /api/groups/{path} answered 200 "deleted" for a path that names no
// group. The mutator now reports it, and the handler answers 404.
func TestWebMutatorDeleteGroup_MissingGroupIsNotFound(t *testing.T) {
	home := homeWithGroups(t, "work")
	m := &WebMutator{h: home}

	// "Work" is not "work": group routes match the stored path exactly.
	for _, path := range []string{"totally-nope", "Work"} {
		if err := m.DeleteGroup(path); !errors.Is(err, menudata.ErrGroupNotFound) {
			t.Errorf("DeleteGroup(%q) = %v, want ErrGroupNotFound", path, err)
		}
	}
	if _, ok := home.groupTree.Groups["work"]; !ok {
		t.Error("a refused delete removed the group work")
	}
}

// The web delete follows the CLI rule: sessions of a deleted subgroup move to
// the parent group, not to My Sessions.
func TestWebMutatorDeleteGroup_MovesSessionsToTheParentGroup(t *testing.T) {
	inst := session.NewInstanceWithGroup("s3", "/tmp/p1", "work/sub")
	home := homeWithGroupedSessions(t, inst)
	m := &WebMutator{h: home}

	if err := m.DeleteGroup("work/sub"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if inst.GroupPath != "work" {
		t.Errorf("session is in %q after deleting work/sub, want the parent group work", inst.GroupPath)
	}
}

// The TUI delete (d, then y) follows the same rule, and its dialog names the
// group the sessions really move to (it used to say 'default' group).
func TestHomeDeleteGroup_DialogNamesTheDestinationAndSessionsMoveThere(t *testing.T) {
	inSub := session.NewInstanceWithGroup("s3", "/tmp/p1", "work/sub")
	inRoot := session.NewInstanceWithGroup("s4", "/tmp/p1", "solo")
	home := homeWithGroupedSessions(t, inSub, inRoot)

	deleteGroup := func(path, wantDialog string) {
		t.Helper()
		found := false
		for i, item := range home.flatItems {
			if item.Type == session.ItemTypeGroup && item.Path == path {
				home.cursor, found = i, true
			}
		}
		if !found {
			t.Fatalf("group %q not in the list", path)
		}
		home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
		if !home.confirmDialog.IsVisible() {
			t.Fatalf("delete dialog not shown for %q", path)
		}
		// The dialog wraps long lines: compare its words, not its layout.
		view := home.confirmDialog.View()
		words := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(view), "│", " ")), " ")
		if !strings.Contains(words, wantDialog) || strings.Contains(words, "'default' group") {
			t.Errorf("delete dialog for %q does not say %q:\n%s", path, wantDialog, view)
		}
		home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	}

	deleteGroup("work/sub", "MOVED to the 'work' group")
	if inSub.GroupPath != "work" {
		t.Errorf("session is in %q after deleting work/sub in the TUI, want the parent group work", inSub.GroupPath)
	}
	deleteGroup("solo", "MOVED to the 'My Sessions' group")
	if inRoot.GroupPath != session.DefaultGroupPath {
		t.Errorf("session is in %q after deleting root group solo, want %q", inRoot.GroupPath, session.DefaultGroupPath)
	}
}

// A session action on an id that resolves to nothing reports the one
// not-found sentinel the handlers map to 404.
func TestWebMutatorSessionActions_UnknownIDIsErrSessionNotFound(t *testing.T) {
	m := &WebMutator{h: homeWithGroups(t)}
	_, forkErr := m.ForkSession("nope")
	for name, err := range map[string]error{
		"start":   m.StartSession("nope"),
		"stop":    m.StopSession("nope"),
		"close":   m.CloseSession("nope"),
		"restart": m.RestartSession("nope"),
		"delete":  m.DeleteSession("nope"),
		"archive": m.ArchiveSession("nope"),
		"fork":    forkErr,
	} {
		if !errors.Is(err, menudata.ErrSessionNotFound) || err.Error() != "session not found: nope" {
			t.Errorf("%s on an unknown id: %v, want ErrSessionNotFound \"session not found: nope\"", name, err)
		}
	}
}

// Archiving an archived session is an error on the web, as on the CLI.
func TestWebMutatorArchiveSession_AlreadyArchivedIsRefused(t *testing.T) {
	inst := session.NewInstance("xi", "/tmp/p1")
	stamp := time.Now().Add(-time.Hour).UTC()
	inst.SetArchivedAtThreadSafe(stamp)
	m := &WebMutator{h: homeWithGroupedSessions(t, inst)}

	err := m.ArchiveSession(inst.ID)
	if err == nil || !strings.Contains(err.Error(), "already archived") {
		t.Errorf("archiving an archived session: err = %v, want an already-archived refusal", err)
	}
	if !inst.ArchivedAt.Equal(stamp) {
		t.Errorf("archive time changed from %v to %v", stamp, inst.ArchivedAt)
	}
}
