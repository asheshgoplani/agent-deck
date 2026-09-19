package session

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// BenchmarkGroupActivityMap measures the collapse-independent activity pass
// used by the TUI with a fleet spread over nested project groups.
func BenchmarkGroupActivityMap(b *testing.B) {
	for _, size := range []int{10, 100, 500} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tree := &GroupTree{Groups: make(map[string]*Group)}
			for i := 0; i < size; i++ {
				path := fmt.Sprintf("work/team-%d/project-%d", i%3, i%10)
				if tree.Groups[path] == nil {
					tree.Groups[path] = &Group{Path: path}
				}
				status := StatusIdle
				if i%3 == 0 {
					status = StatusRunning
				}
				tree.Groups[path].Sessions = append(tree.Groups[path].Sessions, &Instance{Status: status})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(tree.GroupActivityMap(false)) == 0 {
					b.Fatal("missing activity")
				}
			}
		})
	}
}

func TestGroupActivityMapArchiveAndAncestors(t *testing.T) {
	archived := time.Unix(1, 0)
	tree := &GroupTree{Groups: map[string]*Group{
		"work/idle":   {Path: "work/idle", Sessions: []*Instance{{Status: StatusIdle}, {Status: StatusRunning, ArchivedAt: archived}}},
		"work/active": {Path: "work/active", Sessions: []*Instance{{Status: StatusWaiting}, {Status: StatusIdle, ArchivedAt: archived}}},
		"empty":       {Path: "empty"},
		"hidden":      {Path: "hidden", Sessions: []*Instance{{Status: StatusStarting, ArchivedAt: archived}}},
		"":            {Path: "", Sessions: []*Instance{{Status: StatusRunning}}},
	}}
	for _, tc := range []struct {
		archived bool
		want     map[string]GroupActivity
	}{
		{false, map[string]GroupActivity{"work": {true, true}, "work/idle": {true, false}, "work/active": {true, true}}},
		{true, map[string]GroupActivity{"work": {true, true}, "work/idle": {true, true}, "work/active": {true, false}, "hidden": {true, true}}},
	} {
		if got := tree.GroupActivityMap(tc.archived); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("archive=%v: got %v, want %v", tc.archived, got, tc.want)
		}
	}
}
