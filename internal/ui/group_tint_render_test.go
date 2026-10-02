package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	tintYellow = "#ffaa00"
	tintBlue   = "#00aaff"
	// 24-bit foreground escape body of tintYellow / tintBlue.
	tintYellowEsc = "38;2;255;170;0"
	tintBlueEsc   = "38;2;0;170;255"
)

func newTintHome(instances []*session.Instance) *Home {
	forceTrueColorProfile()
	return &Home{width: 140, groupTree: session.NewGroupTree(instances)}
}

func renderGroupRow(h *Home, path string, selected bool) string {
	var b strings.Builder
	g := h.groupTree.Groups[path]
	level := strings.Count(path, "/")
	h.renderGroupItem(&b, session.Item{Type: session.ItemTypeGroup, Group: g, Path: path, Level: level},
		selected, 0, h.buildGroupRenderStats(map[string]sessionRenderState{}), 60)
	return b.String()
}

func TestBuildGroupRenderStats_CountsTintedSessions(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow},
		{ID: "b", Title: "b", GroupPath: "demo", Color: tintYellow},
		{ID: "c", Title: "c", GroupPath: "demo"},
	})
	got := h.buildGroupRenderStats(map[string]sessionRenderState{})["demo"]
	if got.tinted != 2 {
		t.Errorf("tinted = %d, want 2", got.tinted)
	}
	if got.tint != tintYellow {
		t.Errorf("tint = %q, want %q", got.tint, tintYellow)
	}
}

func TestBuildGroupRenderStats_NoColorNoCount(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo"},
		{ID: "b", Title: "b", GroupPath: "demo"},
	})
	got := h.buildGroupRenderStats(map[string]sessionRenderState{})["demo"]
	if got.tinted != 0 || got.tint != "" {
		t.Errorf("(tinted, tint) = (%d, %q), want (0, \"\")", got.tinted, got.tint)
	}
}

func TestBuildGroupRenderStats_TintAggregatesToNestedAncestors(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "root/mid/leaf", Color: tintYellow},
	})
	stats := h.buildGroupRenderStats(map[string]sessionRenderState{})
	for _, path := range []string{"root/mid/leaf", "root/mid", "root"} {
		if stats[path].tinted != 1 || stats[path].tint != tintYellow {
			t.Errorf("%s: (tinted, tint) = (%d, %q), want (1, %q)", path, stats[path].tinted, stats[path].tint, tintYellow)
		}
	}
}

func TestBuildGroupRenderStats_TintIgnoresOtherArchivePartition(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "active", GroupPath: "demo"},
		{ID: "z", Title: "arch", GroupPath: "demo", Color: tintYellow, ArchivedAt: archivedAt()},
	})
	if got := h.buildGroupRenderStats(map[string]sessionRenderState{})["demo"]; got.tinted != 0 {
		t.Errorf("active view: tinted = %d, want 0 — an archived row is not rendered", got.tinted)
	}
	h.statusFilter = FilterModeArchived
	if got := h.buildGroupRenderStats(map[string]sessionRenderState{})["demo"]; got.tinted != 1 {
		t.Errorf("archived view: tinted = %d, want 1", got.tinted)
	}
}

func TestBuildGroupRenderStats_MixedColoursPickSmallestString(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "root/one", Color: tintYellow},
		{ID: "b", Title: "b", GroupPath: "root/two", Color: tintBlue},
		{ID: "c", Title: "c", GroupPath: "root/three", Color: tintYellow},
	})
	// The ancestor walk runs over a map, so repeat to catch order dependence.
	for i := 0; i < 50; i++ {
		got := h.buildGroupRenderStats(map[string]sessionRenderState{})["root"]
		if got.tint != tintBlue {
			t.Fatalf("run %d: tint = %q, want %q (smallest string)", i, got.tint, tintBlue)
		}
		if got.tinted != 3 {
			t.Fatalf("run %d: tinted = %d, want 3", i, got.tinted)
		}
	}
}

func TestRenderGroupItem_CollapsedTintedGroupNameUsesTint(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow},
	})
	h.groupTree.Groups["demo"].Expanded = false
	row := renderGroupRow(h, "demo", false)
	want := GroupNameStyle.Foreground(lipgloss.Color(tintYellow)).Render("demo")
	if !strings.Contains(row, want) {
		t.Errorf("row %q does not hold the tinted, bold group name %q", row, want)
	}
	if !strings.Contains(want, tintYellowEsc) {
		t.Fatalf("test setup: %q lacks the tint escape %q", want, tintYellowEsc)
	}
}

func TestRenderGroupItem_ExpandedGroupKeepsDefaultName(t *testing.T) {
	tinted := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow},
	})
	plain := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo"},
	})
	if got, want := renderGroupRow(tinted, "demo", false), renderGroupRow(plain, "demo", false); got != want {
		t.Errorf("expanded tinted group row differs from the no-tint render:\n got %q\nwant %q", got, want)
	}
}

func TestRenderGroupItem_SelectedRowKeepsSelectedStyle(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow},
	})
	h.groupTree.Groups["demo"].Expanded = false
	row := renderGroupRow(h, "demo", true)
	if !strings.Contains(row, GroupNameSelStyle.Render("demo")) {
		t.Errorf("selected row %q lacks the selected name style", row)
	}
	if strings.Contains(row, tintYellowEsc) {
		t.Errorf("selected row %q carries the tint escape", row)
	}
}

func TestRenderGroupItem_CompactSidebarPaintsTint(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow},
	})
	h.embeddedLayout = true
	h.compactSidebar = true
	h.groupTree.Groups["demo"].Expanded = false
	row := renderGroupRow(h, "demo", false)
	if !strings.Contains(row, GroupNameStyle.Foreground(lipgloss.Color(tintYellow)).Render("demo")) {
		t.Errorf("compact row %q does not carry the tinted group name", row)
	}
}

func TestRenderGroupItem_NestedSubgroupOnlyCollapsedRowPaints(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "root/child", Color: tintYellow},
	})
	tintedRoot := GroupNameStyle.Foreground(lipgloss.Color(tintYellow)).Render("root")
	tintedChild := GroupNameStyle.Foreground(lipgloss.Color(tintYellow)).Render("child")

	h.groupTree.Groups["root/child"].Expanded = false
	if row := renderGroupRow(h, "root/child", false); !strings.Contains(row, tintedChild) {
		t.Errorf("collapsed child row %q not tinted", row)
	}
	if row := renderGroupRow(h, "root", false); strings.Contains(row, tintYellowEsc) {
		t.Errorf("expanded parent row %q is tinted", row)
	}

	h.groupTree.Groups["root"].Expanded = false
	if row := renderGroupRow(h, "root", false); !strings.Contains(row, tintedRoot) {
		t.Errorf("collapsed parent row %q not tinted", row)
	}
}

func TestRenderGroupItem_NoTintIsByteIdenticalToBaseline(t *testing.T) {
	h := newTintHome([]*session.Instance{
		{ID: "a", Title: "a", GroupPath: "demo", Status: session.StatusWaiting},
		{ID: "b", Title: "b", GroupPath: "demo"},
	})
	h.groupTree.Groups["demo"].Expanded = false
	got := renderGroupRow(h, "demo", false)

	// Baseline: the stats a build without the tint fields would have produced.
	var b strings.Builder
	h.renderGroupItem(&b, session.Item{Type: session.ItemTypeGroup, Group: h.groupTree.Groups["demo"], Path: "demo"},
		false, 0, map[string]groupRenderStats{"demo": {sessionCount: 2, waiting: 1}}, 60)
	if got != b.String() {
		t.Errorf("no-tint render changed:\n got %q\nwant %q", got, b.String())
	}
	if !strings.Contains(got, GroupNameStyle.Render("demo")) || !strings.Contains(got, "◐ 1") {
		t.Errorf("row %q lacks the default name or the ◐ 1 counter", got)
	}
}

func TestRenderGroupItem_ColorClearedReturnsToDefault(t *testing.T) {
	inst := &session.Instance{ID: "a", Title: "a", GroupPath: "demo", Color: tintYellow}
	h := newTintHome([]*session.Instance{inst})
	h.groupTree.Groups["demo"].Expanded = false
	if row := renderGroupRow(h, "demo", false); !strings.Contains(row, tintYellowEsc) {
		t.Fatalf("setup: row %q not tinted", row)
	}
	inst.Color = ""
	row := renderGroupRow(h, "demo", false)
	if strings.Contains(row, tintYellowEsc) || !strings.Contains(row, GroupNameStyle.Render("demo")) {
		t.Errorf("after clearing Color the row %q is not back to the default name", row)
	}
}
