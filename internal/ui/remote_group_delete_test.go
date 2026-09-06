// d on a remote group header deletes that group on the remote (after the
// same confirmation local groups get). The Level-0 "remotes/<host>" header is
// a local UI bucket and must not offer a delete.

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRemoteGroupDelete_DOnRemoteGroupHeaderOpensRemoteConfirm(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "box", Path: "remotes/box", Level: 0},
		{Type: session.ItemTypeRemoteGroup, RemoteName: "box", Path: "remotes/box/work/api", Level: 2},
	}
	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 1

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	if !h.confirmDialog.IsVisible() || h.confirmDialog.confirmType != ConfirmDeleteRemoteGroup {
		t.Fatalf("d on a remote group header must open the remote group delete confirm; visible=%v type=%v", h.confirmDialog.IsVisible(), h.confirmDialog.confirmType)
	}
	if h.confirmDialog.GetRemoteName() != "box" || h.confirmDialog.GetTargetID() != "work/api" || h.confirmDialog.targetName != "api" {
		t.Fatalf("confirm target = remote %q path %q name %q, want box / work/api / api", h.confirmDialog.GetRemoteName(), h.confirmDialog.GetTargetID(), h.confirmDialog.targetName)
	}
}

func TestRemoteGroupDelete_DOnHostHeaderDoesNothing(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeRemoteGroup, RemoteName: "box", Path: "remotes/box", Level: 0},
	}
	home := newTestHomeWithItems(100, 30, items)
	home.cursor = 0

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	h := model.(*Home)

	if h.confirmDialog.IsVisible() {
		t.Fatalf("d on the remotes/<host> header must not open any confirm dialog")
	}
}

func TestRemoteGroupDelete_ResultDropsGroupAndSubgroupsFromCache(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteGroups = map[string][]string{"box": {"work", "work/api", "work/api/v2", "workshop", "other"}}

	model, cmd := home.Update(remoteGroupDeleteResultMsg{remoteName: "box", groupPath: "work/api"})
	h := model.(*Home)

	got := h.remoteGroups["box"]
	want := []string{"work", "workshop", "other"}
	if len(got) != len(want) {
		t.Fatalf("cached groups after delete = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cached groups after delete = %v, want %v", got, want)
		}
	}
	if cmd == nil {
		t.Fatalf("a successful remote group delete must schedule a fleet refresh")
	}
}
