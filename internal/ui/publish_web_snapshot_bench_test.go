package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// publishWebMenuSnapshot runs at the end of rebuildFlatItems, which is on the
// TUI list hot path. Benchmarks the two candidate shapes for the archive filter
// so the cost of the fix is measured rather than assumed.
func benchInstances(n int) []*session.Instance {
	out := make([]*session.Instance, 0, n)
	for i := range n {
		inst := session.NewInstanceWithTool(fmt.Sprintf("s%d", i), "/tmp/x", "claude")
		inst.Status = session.StatusIdle
		// Roughly a third archived, matching a well-used deck.
		if i%3 == 0 {
			inst.ArchivedAt = time.Now().UTC()
		}
		out = append(out, inst)
	}
	return out
}

// The shape that shipped: filter allocates the copy directly.
func BenchmarkPublishFilterOnly(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		instances := benchInstances(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = session.FilterInstancesByArchive(instances, false)
			}
		})
	}
}

// The pre-fix shape (make+copy, no filter) — the baseline this replaced.
func BenchmarkPublishCopyOnly(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		instances := benchInstances(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				cp := make([]*session.Instance, len(instances))
				copy(cp, instances)
				_ = cp
			}
		})
	}
}

// End-to-end publish cost, the number that actually matters.
func BenchmarkPublishWebMenuSnapshot(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		instances := benchInstances(n)
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			home := NewHome()
			home.initialLoading = false
			home.instancesMu.Lock()
			home.instances = instances
			home.instancesMu.Unlock()
			home.groupTree = session.NewGroupTree(instances)
			home.SetWebMenuData(web.NewMemoryMenuData(nil))

			b.ReportAllocs()
			for b.Loop() {
				home.publishWebMenuSnapshot()
			}
		})
	}
}
