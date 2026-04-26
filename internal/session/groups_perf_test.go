package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// Performance regression tests for the group lifecycle hot paths.
//
// Track B (TestPerf_*) — hard-gated walltime budgets, runs under -race in CI.
// Track A (Benchmark*) — advisory ns/op trending, runs without -race via
// `make bench`.
//
// Budgets are 5x the last observed local median (Linux, -race,
// multiplier=1.0). CI sets PERF_BUDGET_MULTIPLIER=2.0, so the effective
// CI gate is 10x local. See CLAUDE.md "Performance regression: mandatory
// test coverage".

// TestPerf_GroupCreate_100Flat creates 100 root-level groups and asserts the
// median walltime stays under budget. Catches accidental O(n²) regressions in
// rebuildGroupList (groups.go:670).
func TestPerf_GroupCreate_100Flat(t *testing.T) {
	testutil.SkipIfShort(t)
	// Last local median: 8.13ms.
	budget := testutil.Budget(t, 40*time.Millisecond)

	got := testutil.MedianOf(5, func() {
		tree := NewGroupTree(nil)
		for i := 0; i < 100; i++ {
			tree.CreateGroup(fmt.Sprintf("perf-group-%d", i))
		}
	})

	if got > budget {
		t.Fatalf("CreateGroup x100 median = %v, budget = %v (regression in groups.go:670 or rebuildGroupList)", got, budget)
	}
	t.Logf("CreateGroup x100 median = %v (budget = %v)", got, budget)
}

// TestPerf_GroupCreate_NestedDeep builds a 50-level chain via CreateSubgroup.
// Catches regressions in the parent-walk + sibling-count logic at groups.go:700.
func TestPerf_GroupCreate_NestedDeep(t *testing.T) {
	testutil.SkipIfShort(t)
	// Last local median: 6.91ms (run-to-run variance observed: 1.9–6.9ms).
	budget := testutil.Budget(t, 34*time.Millisecond)

	got := testutil.MedianOf(5, func() {
		tree := NewGroupTree(nil)
		parent := ""
		for i := 0; i < 50; i++ {
			name := fmt.Sprintf("level-%d", i)
			if parent == "" {
				g := tree.CreateGroup(name)
				parent = g.Path
			} else {
				g := tree.CreateSubgroup(parent, name)
				parent = g.Path
			}
		}
	})

	if got > budget {
		t.Fatalf("CreateSubgroup deep-50 median = %v, budget = %v (regression in groups.go:700)", got, budget)
	}
	t.Logf("CreateSubgroup deep-50 median = %v (budget = %v)", got, budget)
}

// TestPerf_GroupDelete_100Flat_With5Each populates 100 groups × 5 sessions
// and times bulk DeleteGroup. Setup is excluded from the timed window.
// Exercises the subgroup-collection + default-group-merge branches at
// groups.go:884.
func TestPerf_GroupDelete_100Flat_With5Each(t *testing.T) {
	testutil.SkipIfShort(t)
	// Last local median: 6.40ms.
	budget := testutil.Budget(t, 32*time.Millisecond)

	var tree *GroupTree
	var paths []string

	got := testutil.MedianTimedOp(5,
		func() {
			tree = NewGroupTree(nil)
			paths = paths[:0]
			for i := 0; i < 100; i++ {
				name := fmt.Sprintf("perf-group-%d", i)
				g := tree.CreateGroup(name)
				for j := 0; j < 5; j++ {
					g.Sessions = append(g.Sessions, &Instance{
						ID:        fmt.Sprintf("s-%d-%d", i, j),
						GroupPath: g.Path,
					})
				}
				paths = append(paths, g.Path)
			}
		},
		func() {
			for _, p := range paths {
				tree.DeleteGroup(p)
			}
		},
	)

	if got > budget {
		t.Fatalf("DeleteGroup x100 (5 sessions each) median = %v, budget = %v (regression in groups.go:884)", got, budget)
	}
	t.Logf("DeleteGroup x100 median = %v (budget = %v)", got, budget)
}

// TestPerf_GroupTree_TraverseLargeTree pre-builds 1k groups and times
// GetAllInstances + GetGroupNames (groups.go:942, :951). Catches accidental
// O(n²) walks added to either traversal helper.
func TestPerf_GroupTree_TraverseLargeTree(t *testing.T) {
	testutil.SkipIfShort(t)
	// Last local median: 104µs.
	budget := testutil.Budget(t, 500*time.Microsecond)

	var tree *GroupTree
	got := testutil.MedianTimedOp(5,
		func() {
			tree = NewGroupTree(nil)
			for i := 0; i < 1000; i++ {
				g := tree.CreateGroup(fmt.Sprintf("g-%d", i))
				g.Sessions = append(g.Sessions, &Instance{
					ID:        fmt.Sprintf("s-%d", i),
					GroupPath: g.Path,
				})
			}
		},
		func() {
			_ = tree.GetAllInstances()
			_ = tree.GetGroupNames()
		},
	)

	if got > budget {
		t.Fatalf("Traverse 1k tree median = %v, budget = %v (regression in groups.go:942 or :951)", got, budget)
	}
	t.Logf("Traverse 1k tree median = %v (budget = %v)", got, budget)
}

// ---- Track A: advisory benchmarks (no CI gate) ----------------------------

func BenchmarkGroupCreate(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tree := NewGroupTree(nil)
		for j := 0; j < 100; j++ {
			tree.CreateGroup(fmt.Sprintf("bench-%d", j))
		}
	}
}

func BenchmarkGroupDelete(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tree := NewGroupTree(nil)
		paths := make([]string, 0, 100)
		for j := 0; j < 100; j++ {
			g := tree.CreateGroup(fmt.Sprintf("bench-%d", j))
			for k := 0; k < 5; k++ {
				g.Sessions = append(g.Sessions, &Instance{
					ID:        fmt.Sprintf("s-%d-%d", j, k),
					GroupPath: g.Path,
				})
			}
			paths = append(paths, g.Path)
		}
		b.StartTimer()
		for _, p := range paths {
			tree.DeleteGroup(p)
		}
	}
}

func BenchmarkGroupTreeTraverse(b *testing.B) {
	tree := NewGroupTree(nil)
	for i := 0; i < 1000; i++ {
		g := tree.CreateGroup(fmt.Sprintf("bench-%d", i))
		g.Sessions = append(g.Sessions, &Instance{
			ID:        fmt.Sprintf("s-%d", i),
			GroupPath: g.Path,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tree.GetAllInstances()
		_ = tree.GetGroupNames()
	}
}
