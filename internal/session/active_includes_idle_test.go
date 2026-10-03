package session

// Issue #2452: [ui] active_includes_idle keeps idle sessions that still have a
// live tmux pane in the top section of the active-on-top view, so the list of
// sessions being juggled stops reshuffling every time one goes idle. Only
// sessions without a live pane (stopped, error, queued) sink. The option is
// opt-in; with it off the split must stay exactly as before.

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// mixedStatusItems: proj-a holds every status, proj-b only an idle session,
// proj-c only sessions without a live pane.
func mixedStatusItems() []Item {
	return []Item{
		groupItem("proj-a"),
		sessItem("run", StatusRunning, "proj-a"),
		sessItem("idle-a", StatusIdle, "proj-a"),
		sessItem("stopped-a", StatusStopped, "proj-a"),
		sessItem("err-a", StatusError, "proj-a"),
		groupItem("proj-b"),
		sessItem("idle-b", StatusIdle, "proj-b"),
		groupItem("proj-c"),
		sessItem("stopped-c", StatusStopped, "proj-c"),
		sessItem("queued-c", StatusQueued, "proj-c"),
	}
}

func dividerLabelOf(items []Item) string {
	for _, it := range items {
		if it.Type == ItemTypeDivider {
			return it.DividerLabel
		}
	}
	return ""
}

func TestActiveIncludesIdle_IdleWithPaneStaysTopStoppedSinks(t *testing.T) {
	got := PartitionByViewModeWith(mixedStatusItems(), GroupViewActiveTop, nil, ViewModeOptions{ActiveIncludesIdle: true})
	want := []string{
		"G:proj-a", "S:run", "S:idle-a",
		"G:proj-b", "S:idle-b",
		"---",
		"G:proj-a", "S:stopped-a", "S:err-a",
		"G:proj-c", "S:stopped-c", "S:queued-c",
	}
	if s := summarize(got); !eqSlice(s, want) {
		t.Fatalf("active_includes_idle split:\n got=%v\nwant=%v", s, want)
	}
	if l := dividerLabelOf(got); l != "stopped / done" {
		t.Fatalf("divider label = %q, want %q (idle sessions are no longer below it)", l, "stopped / done")
	}
}

// Default off: the historical working-vs-idle split, divider label included.
func TestActiveIncludesIdle_OffKeepsTodaysSplit(t *testing.T) {
	want := []string{
		"G:proj-a", "S:run",
		"---",
		"G:proj-a", "S:idle-a", "S:stopped-a", "S:err-a",
		"G:proj-b", "S:idle-b",
		"G:proj-c", "S:stopped-c", "S:queued-c",
	}
	for name, got := range map[string][]Item{
		"PartitionByViewMode":           PartitionByViewMode(mixedStatusItems(), GroupViewActiveTop, nil),
		"PartitionByViewModeWith(zero)": PartitionByViewModeWith(mixedStatusItems(), GroupViewActiveTop, nil, ViewModeOptions{}),
	} {
		if s := summarize(got); !eqSlice(s, want) {
			t.Fatalf("%s: default split changed:\n got=%v\nwant=%v", name, s, want)
		}
		if l := dividerLabelOf(got); l != "idle / done" {
			t.Fatalf("%s: divider label = %q, want %q", name, l, "idle / done")
		}
	}
}

// The option only redefines active-on-top; populated-on-top never splits by
// status, so it must be unaffected.
func TestActiveIncludesIdle_PopulatedTopUnaffected(t *testing.T) {
	items := append(mixedStatusItems(), groupItem("empty"))
	off := summarize(PartitionByViewMode(items, GroupViewPopulatedTop, nil))
	on := summarize(PartitionByViewModeWith(items, GroupViewPopulatedTop, nil, ViewModeOptions{ActiveIncludesIdle: true}))
	if !eqSlice(off, on) {
		t.Fatalf("populated-on-top changed by active_includes_idle:\n off=%v\n  on=%v", off, on)
	}
}

// A pin still beats the status split: pin-bottom sinks an idle session even
// with the option on.
func TestActiveIncludesIdle_PinBottomStillSinks(t *testing.T) {
	pinned := sessItem("idle-pinned", StatusIdle, "a")
	pinned.Session.Pin = PinBottom
	items := []Item{
		groupItem("a"),
		sessItem("idle", StatusIdle, "a"),
		pinned,
	}
	got := summarize(PartitionByViewModeWith(items, GroupViewActiveTop, nil, ViewModeOptions{ActiveIncludesIdle: true}))
	want := []string{"G:a", "S:idle", "---", "G:a", "S:idle-pinned"}
	if !eqSlice(got, want) {
		t.Fatalf("pin-bottom must override active_includes_idle:\n got=%v\nwant=%v", got, want)
	}
}

// A collapsed group holding only idle sessions has no session rows, so its
// placement comes from the tree-wide activity map. With the option on it must
// count as active (stay top); with it off it sinks as before.
func TestActiveIncludesIdle_CollapsedIdleGroupStaysTop(t *testing.T) {
	idle := &Instance{ID: "i", Title: "i", GroupPath: "idle-grp", Status: StatusIdle}
	stopped := &Instance{ID: "s", Title: "s", GroupPath: "stopped-grp", Status: StatusStopped}
	tree := NewGroupTree([]*Instance{idle, stopped})

	on := tree.GroupActivityMapWith(false, ViewModeOptions{ActiveIncludesIdle: true})
	if !on["idle-grp"].HasActive {
		t.Fatalf("idle-grp HasActive=false with active_includes_idle on, want true")
	}
	if on["stopped-grp"].HasActive {
		t.Fatalf("stopped-grp HasActive=true with active_includes_idle on, want false")
	}
	if off := tree.GroupActivityMap(false); off["idle-grp"].HasActive {
		t.Fatalf("idle-grp HasActive=true with the option off, want false (default unchanged)")
	}

	items := []Item{groupItem("idle-grp"), groupItem("stopped-grp")} // both collapsed
	got := summarize(PartitionByViewModeWith(items, GroupViewActiveTop, on, ViewModeOptions{ActiveIncludesIdle: true}))
	want := []string{"G:idle-grp", "---", "G:stopped-grp"}
	if !eqSlice(got, want) {
		t.Fatalf("collapsed idle group placement:\n got=%v\nwant=%v", got, want)
	}
}

func TestActiveIncludesIdle_ConfigKey(t *testing.T) {
	var unset UISettings
	if unset.GetActiveIncludesIdle() {
		t.Fatal("GetActiveIncludesIdle() on unset config = true, want false (opt-in)")
	}
	var cfg UserConfig
	if _, err := toml.Decode("[ui]\nactive_includes_idle = true\n", &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.UI.GetActiveIncludesIdle() {
		t.Fatal("active_includes_idle = true decoded as false")
	}
}
