package procowner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The descendant walk runs over a process-table snapshot whose rows were read
// one at a time. These tests script the exact interleavings a naive ppid walk
// gets wrong: a pid that changed hands between the snapshot and the walk.

func snapshotOf(p *fakeProber, pids ...int) []ProcInfo {
	table := make([]ProcInfo, 0, len(pids))
	for _, pid := range pids {
		info, err := p.Inspect(pid)
		if err == nil {
			table = append(table, info)
		}
	}
	return table
}

func TestDescendantsOf_RecordsTheLiveTree(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	p.add(200, 100, "12", 1000)
	p.add(300, 200, "14", 1000)
	p.add(900, 1, "13", 1000) // unrelated

	kids, err := descendantsOf(p, root, snapshotOf(p, 100, 200, 300, 900))
	require.NoError(t, err)
	require.Len(t, kids, 2)
	assert.Equal(t, 200, kids[0].PID)
	assert.Equal(t, 300, kids[1].PID)
}

func TestDescendantsOf_RootRecycledInsideTheSnapshotIsRefused(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	p.add(200, 100, "12", 1000)
	// The snapshot's own row for the root pid names a different occupant: the
	// root died and the pid was reused while the table was being read. Every
	// child observed under it belongs to the stranger.
	table := snapshotOf(p, 100, 200)
	table[0].StartID = "50"

	_, err := descendantsOf(p, root, table)
	require.ErrorIs(t, err, ErrNoProcess)
}

func TestDescendantsOf_RootRecycledAfterTheSnapshotIsRefused(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	p.add(200, 100, "12", 1000)
	table := snapshotOf(p, 100, 200)
	// Recycle the root between the snapshot and the walk.
	p.remove(100)
	p.add(100, 1, "60", 1000)

	_, err := descendantsOf(p, root, table)
	require.ErrorIs(t, err, ErrNoProcess)
}

func TestDescendantsOf_IntermediateRecycledAfterTheSnapshotDropsItsSubtree(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	p.add(200, 100, "12", 1000) // intermediate
	p.add(300, 200, "14", 1000) // its child
	p.add(210, 100, "11", 1000) // a sibling branch that stays intact
	table := snapshotOf(p, 100, 200, 300, 210)
	// pid 200 changes hands after its row was read: 300's ppid link now points
	// at a stranger, and 200 itself is no longer the process we saw.
	p.remove(200)
	p.add(200, 1, "70", 1000)

	kids, err := descendantsOf(p, root, table)
	require.NoError(t, err)
	pids := make([]int, 0, len(kids))
	for _, k := range kids {
		pids = append(pids, k.PID)
	}
	assert.ElementsMatch(t, []int{210}, pids,
		"the recycled intermediate and everything below it must be dropped; the intact branch stays")
}

func TestDescendantsOf_ChildOlderThanItsParentIsNotItsChild(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	// A process that started BEFORE its supposed parent cannot have been forked
	// by it: the ppid link is left over from a pid the parent's occupant reused.
	p.add(200, 100, "5", 1000)
	p.add(300, 100, "12", 1000)

	kids, err := descendantsOf(p, root, snapshotOf(p, 100, 200, 300))
	require.NoError(t, err)
	require.Len(t, kids, 1)
	assert.Equal(t, 300, kids[0].PID)
}

func TestDescendantsOf_ZombiesAreNotLiveDescendants(t *testing.T) {
	p := newFakeProber()
	root := p.add(100, 1, "10", 1000)
	p.add(200, 100, "12", 1000)
	table := snapshotOf(p, 100, 200)
	table[1].State = "Z"

	kids, err := descendantsOf(p, root, table)
	require.NoError(t, err)
	assert.Empty(t, kids)
}
