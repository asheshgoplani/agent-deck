//go:build eval_smoke

package ui

// Behavioral eval for the 2026-09-17 tree-connector fix.
//
// Why this lives in internal/ui/ and not tests/eval/: Go's internal-package
// rule prevents tests/eval/... from importing internal/ui. The eval still
// runs under `-tags eval_smoke` in the eval-smoke CI tier. See
// tests/eval/README.md.
//
// This drives the real renderSessionList() output (what a user actually
// sees) rather than the underlying Item flags — a Go unit test can pass on
// the struct fields while the row still prints the wrong glyph if a future
// change touches renderSessionItem's glyph selection without touching the
// flags (groups.go:711 guard, and ParentIsLastInGroup/IsLastSubSession never
// being corrected for filtered or archived trailing rows).

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// rowFor returns the single rendered line containing the given session
// title, or fails the test if it is not found exactly once.
func rowFor(t *testing.T, rendered, title string) string {
	t.Helper()
	var match string
	found := 0
	for _, row := range strings.Split(rendered, "\n") {
		if strings.Contains(row, title) {
			match = row
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly 1 rendered row containing %q, found %d\nfull render:\n%s", title, found, rendered)
	}
	return match
}

// TestEval_TreeConnectors_RenderedRowsMatchExpectedGlyphs exercises the three
// scenarios from the spec's objective table in one screen: a group whose
// last top-level session has children, a subgroup with a sole top-level
// session that has children, and a parent with one live and several archived
// children.
func TestEval_TreeConnectors_RenderedRowsMatchExpectedGlyphs(t *testing.T) {
	h := seamBNewHome()
	h.initialLoading = false
	h.width, h.height = 120, 60

	// Scenario 1: group "alpha" — p1 (no children), p2 (last top-level, has
	// a child) → p2 must render └─, not ├─ (defect 1).
	p1 := session.NewInstanceWithTool("row1-p1", "/tmp/p1", "claude")
	p2 := session.NewInstanceWithTool("row1-p2", "/tmp/p2", "claude")
	c1 := session.NewInstanceWithTool("row1-c1", "/tmp/c1", "claude")
	for _, in := range []*session.Instance{p1, p2, c1} {
		in.GroupPath = "alpha"
		in.Status = session.StatusIdle
	}
	c1.SetParent(p2.ID)

	// Scenario 2: subgroup "trovy/anarlog" — sole top-level session with a
	// child → must render └─ (defect 1, also applies inside subgroups).
	q := session.NewInstanceWithTool("row2-q", "/tmp/q", "claude")
	c2 := session.NewInstanceWithTool("row2-c2", "/tmp/c2", "claude")
	for _, in := range []*session.Instance{q, c2} {
		in.GroupPath = "trovy/anarlog"
		in.Status = session.StatusIdle
	}
	c2.SetParent(q.ID)

	// Scenario 3: group "beta" — sole top-level parent r with 1 live child
	// (created first) and 2 archived children (created after) → the live
	// child must render └─ with no leading │ (defect 2: IsLastSubSession and
	// ParentIsLastInGroup are computed on the raw, archived-inclusive list
	// and never corrected).
	r := session.NewInstanceWithTool("row3-r", "/tmp/r", "claude")
	r.GroupPath = "beta"
	r.Status = session.StatusIdle
	live := session.NewInstanceWithTool("row3-live", "/tmp/live", "claude")
	live.GroupPath = "beta"
	live.Status = session.StatusIdle
	live.SetParent(r.ID)

	instances := []*session.Instance{p1, p2, c1, q, c2, r, live}
	for i := 0; i < 2; i++ {
		archived := session.NewInstance("row3-archived", "/tmp/archived")
		archived.GroupPath = "beta"
		archived.Status = session.StatusIdle
		archived.SetParent(r.ID)
		archived.ArchivedAt = time.Now()
		instances = append(instances, archived) // raw order: created after the live child
	}

	h.instancesMu.Lock()
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTree(instances)
	h.setHotkeys(resolveHotkeys(nil))
	h.rebuildFlatItems()

	rendered := h.renderSessionList(h.width, h.height)
	if rendered == "" {
		t.Fatal("renderSessionList returned empty — test harness misconfigured")
	}

	row := rowFor(t, rendered, "row1-p2")
	if !strings.Contains(row, treeLast) {
		t.Errorf("row1-p2 (last top-level, has a child) does not render %q:\n%s", treeLast, row)
	}
	if strings.Contains(row, treeBranch) {
		t.Errorf("row1-p2 renders %q instead of %q:\n%s", treeBranch, treeLast, row)
	}

	row = rowFor(t, rendered, "row2-q")
	if !strings.Contains(row, treeLast) {
		t.Errorf("row2-q (sole top-level in subgroup, has a child) does not render %q:\n%s", treeLast, row)
	}

	row = rowFor(t, rendered, "row3-live")
	if !strings.Contains(row, subLast) {
		t.Errorf("row3-live (sole visible child, 2 archived siblings) does not render %q:\n%s", subLast, row)
	}
	if strings.Contains(row, "│") {
		t.Errorf("row3-live renders a stray │ continuation even though its parent is last in its group:\n%s", row)
	}
}
