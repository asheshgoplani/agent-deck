package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3169: a session created from a session in another group (for example
// `agent-deck add -g workers` run from inside a conductor that lives in group
// "conductor") keeps its parent link, so its row is a sub-session. Flatten
// already emits it at top-level depth (#2995), but the TUI row renderer
// switched to the nested layout for every sub-session: with the cursor on it,
// the row moved two columns right and read as a child of the row above it.
//
// A row is drawn nested only when it sits one level deeper than its group's
// top-level rows, the same rule the web sidebar uses (depth > groupDepth+1).

// issue3169Home builds the issue's deck: A in "conductor"; B and C top-level
// in grp; D in grp with A as its parent. dOrder places D among B (0) and C (2).
func issue3169Home(t *testing.T, grp string, dOrder int) (*Home, map[string]*session.Instance) {
	t.Helper()
	session.SetGroupSortMode("creation")
	t.Cleanup(func() { session.SetGroupSortMode("creation") })

	a := session.NewInstanceWithTool("A", "/tmp/a", "claude")
	a.GroupPath = "conductor"
	b := session.NewInstanceWithTool("B", "/tmp/b", "claude")
	b.GroupPath = grp
	c := session.NewInstanceWithTool("C", "/tmp/c", "claude")
	c.GroupPath = grp
	d := session.NewInstanceWithTool("D", "/tmp/d", "claude")
	d.GroupPath = grp
	d.ParentSessionID = a.ID
	a.Order, b.Order, c.Order, d.Order = 0, 0, 2, dOrder

	instances := []*session.Instance{a, b, c, d}
	h := NewHome()
	h.width, h.height = 100, 30
	h.instancesMu.Lock()
	h.instances = instances
	for _, in := range instances {
		h.instanceByID[in.ID] = in
	}
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTree(instances)
	h.rebuildFlatItems()
	return h, map[string]*session.Instance{"A": a, "B": b, "C": c, "D": d}
}

// rowForIssue3169 returns the rendered list line of the session titled title.
func rowForIssue3169(t *testing.T, frame, title string) string {
	t.Helper()
	for _, l := range strings.Split(frame, "\n") {
		if strings.Contains(l, "○ "+title+" ") {
			return l
		}
	}
	t.Fatalf("no row for %q in frame:\n%s", title, frame)
	return ""
}

// connectorColumn is the cell column of the row's tree connector (├ or └).
func connectorColumn(line string) int {
	for i, r := range []rune(line) {
		if r == '├' || r == '└' {
			return i
		}
	}
	return -1
}

func TestCrossGroupChildRendersTopLevel_Issue3169(t *testing.T) {
	for _, grp := range []string{"workers", "deck/workers"} {
		for _, dOrder := range []int{1, 5} {
			t.Run(fmt.Sprintf("%s/dOrder=%d", grp, dOrder), func(t *testing.T) {
				h, s := issue3169Home(t, grp, dOrder)
				if s["D"].ParentSessionID != s["A"].ID {
					t.Fatalf("precondition: D must keep its parent link")
				}

				// The same cursor on B and on D must put ▶ and the connector in
				// the same columns: D is a top-level row of grp, like B.
				h.moveCursorToSession(s["B"].ID)
				bSel := rowForIssue3169(t, stripAnsi(h.renderSessionList(80, 20)), "B")
				h.moveCursorToSession(s["D"].ID)
				frame := stripAnsi(h.renderSessionList(80, 20))
				dSel := rowForIssue3169(t, frame, "D")
				cRow := rowForIssue3169(t, frame, "C")

				if got, want := connectorColumn(dSel), connectorColumn(bSel); got != want {
					t.Errorf("selected D connector at column %d, selected B at %d: D is drawn as a child\nB: %q\nD: %q", got, want, bSel, dSel)
				}
				if got, want := strings.Index(dSel, "▶"), strings.Index(bSel, "▶"); got != want {
					t.Errorf("selected D arrow at byte %d, selected B at %d\nB: %q\nD: %q", got, want, bSel, dSel)
				}
				if got, want := connectorColumn(dSel), connectorColumn(cRow); got != want {
					t.Errorf("selected D connector at column %d, unselected C at %d\nC: %q\nD: %q", got, want, cRow, dSel)
				}
				if s["D"].ParentSessionID != s["A"].ID {
					t.Errorf("rendering dropped D's parent link")
				}
			})
		}
	}
}

// TestCrossGroupChildGolden_Issue3169 pins the whole list for the issue's
// layout, cursor on D, at root and nested group depth.
// UPDATE_GOLDEN=1 rewrites testdata/issue3169/*.txt.
func TestCrossGroupChildGolden_Issue3169(t *testing.T) {
	for _, tc := range []struct {
		name, grp string
		dOrder    int
	}{
		{"root_d_second", "workers", 1},
		{"root_d_last", "workers", 5},
		{"nested_d_second", "deck/workers", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := issue3169Home(t, tc.grp, tc.dOrder)
			h.moveCursorToSession(s["D"].ID)
			var lines []string
			for _, l := range strings.Split(stripAnsi(h.renderSessionList(60, 12)), "\n") {
				if l = strings.TrimRight(l, " "); l != "" {
					lines = append(lines, l)
				}
			}
			got := strings.Join(lines, "\n") + "\n"
			path := filepath.Join("testdata", "issue3169", tc.name+".txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("%s differs\nwant:\n%s\ngot:\n%s", path, want, got)
			}
		})
	}
}
