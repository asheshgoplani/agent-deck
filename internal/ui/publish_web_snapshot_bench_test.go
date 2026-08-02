package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// benchInstances builds n sessions with roughly a third archived, matching a
// well-used deck. Shared fixture for the benchmarks below.
//
// These exist because publishWebMenuSnapshot runs at the end of
// rebuildFlatItems, which is on the TUI list hot path, so the cost of the
// archive filter is measured rather than assumed.
func benchInstances(n int) []*session.Instance {
	out := make([]*session.Instance, 0, n)
	for i := range n {
		inst := session.NewInstanceWithTool(fmt.Sprintf("s%d", i), "/tmp/x", "claude")
		inst.Status = session.StatusIdle
		if i%3 == 0 {
			inst.ArchivedAt = time.Now().UTC()
		}
		out = append(out, inst)
	}
	return out
}

// BenchmarkPublishFilterOnly measures the shape that shipped: the filter
// allocates the copy directly, replacing the previous make+copy.
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

// BenchmarkPublishCopyOnly measures the pre-fix shape (make+copy, no filter) —
// the baseline the archive filter replaced.
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

// BenchmarkPublishWebMenuSnapshot measures end-to-end publish cost, the number
// the filter's overhead should be judged against.
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
