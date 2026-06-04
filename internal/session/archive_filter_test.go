package session

import (
	"testing"
	"time"
)

func archSessItem(id string, archived bool, path string) Item {
	inst := &Instance{ID: id, Status: StatusStopped, GroupPath: path}
	if archived {
		inst.ArchivedAt = time.Now()
	}
	return Item{Type: ItemTypeSession, Session: inst, Path: path}
}

func TestFilterArchived_ShowTrueIsIdentity(t *testing.T) {
	items := []Item{
		groupItem("a"),
		archSessItem("1", false, "a"),
		archSessItem("2", true, "a"),
	}
	got := FilterArchived(items, true)
	if len(got) != len(items) {
		t.Fatalf("showArchived=true must be identity; got %d want %d", len(got), len(items))
	}
}

func TestFilterArchived_HiddenDropsArchivedRows(t *testing.T) {
	items := []Item{
		groupItem("a"),
		archSessItem("1", false, "a"),
		archSessItem("2", true, "a"),
	}
	got := summarize(FilterArchived(items, false))
	want := []string{"G:a", "S:1"}
	if !eqSlice(got, want) {
		t.Fatalf("hidden view: got %v want %v", got, want)
	}
}

func TestFilterArchived_HiddenKeepsArchivedOnlyGroupHeader(t *testing.T) {
	items := []Item{
		groupItem("a"),
		archSessItem("1", true, "a"), // only session is archived
		groupItem("b"),
		archSessItem("2", false, "b"),
	}
	got := summarize(FilterArchived(items, false))
	// Group "a" keeps its header (its archived row hides) and renders like a
	// genuinely-empty group — consistent with how a group behaves after its
	// last session is *removed*. Only the archived session row drops.
	want := []string{"G:a", "G:b", "S:2"}
	if !eqSlice(got, want) {
		t.Fatalf("hidden view: got %v want %v", got, want)
	}
}

// TestFilterArchived_HiddenKeepsArchivedOnlySubgroupHeader is the reported bug:
// a nested subgroup whose only session is archived must keep its header (nested
// under its parent) when archived view is off, not vanish from the tree.
func TestFilterArchived_HiddenKeepsArchivedOnlySubgroupHeader(t *testing.T) {
	items := []Item{
		groupItem("root"),
		archSessItem("p", false, "root"),
		groupItem("root/sub"),
		archSessItem("c", true, "root/sub"), // subgroup's only session is archived
	}
	got := summarize(FilterArchived(items, false))
	// Subgroup header stays; only its archived row drops.
	want := []string{"G:root", "S:p", "G:root/sub"}
	if !eqSlice(got, want) {
		t.Fatalf("hidden view: got %v want %v", got, want)
	}
}

func TestFilterArchived_HiddenKeepsGenuinelyEmptyGroup(t *testing.T) {
	items := []Item{
		groupItem("empty"), // no sessions at all
		groupItem("a"),
		archSessItem("1", false, "a"),
	}
	got := summarize(FilterArchived(items, false))
	want := []string{"G:empty", "G:a", "S:1"} // empty group stays visible
	if !eqSlice(got, want) {
		t.Fatalf("hidden view: got %v want %v", got, want)
	}
}
