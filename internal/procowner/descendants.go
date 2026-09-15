package procowner

import "fmt"

// descendantsOf follows parent links down from root over one snapshot of the
// process table, and refuses every edge it cannot prove.
//
// A snapshot is not atomic: its rows are read one at a time, and a pid can
// change hands between two of those reads. A naive walk that follows numeric
// ppid links would then attribute a stranger's children to a pid that used to
// be ours. Three checks close that, and each can only REJECT a candidate:
//
//  1. The snapshot's own row for root must carry root's identity. A root that
//     is absent from, or different in, the snapshot has died or been recycled
//     during the scan; nothing below it is attributable.
//  2. A child is accepted only if its start identity is not earlier than its
//     parent's. A process forked by the parent's current occupant started after
//     that occupant did, so a child that predates its parent is a stranger's
//     child left behind by a recycled pid.
//  3. Every accepted node that itself has accepted children — root included —
//     is re-inspected AFTER the snapshot, and its subtree is dropped when the
//     identity has changed. An identity that is the same before and after the
//     scan was the same throughout it (a pid cannot come back to a previous
//     occupant), so every ppid link observed in between pointed at the process
//     we verified.
//
// Together: a node is recorded only when it was observed as a child of a pid
// whose occupant is proven to have been the same verified process for the whole
// scan, and whose own start predates the child's. That is attribution, not a
// guess. A dropped subtree is a set of processes we decline to own, which is
// the safe direction.
func descendantsOf(p Prober, root ProcInfo, table []ProcInfo) ([]ProcInfo, error) {
	if root.PID <= 0 {
		return nil, fmt.Errorf("%w: pid %d", ErrNoProcess, root.PID)
	}
	byParent := map[int][]ProcInfo{}
	var snapshotRoot *ProcInfo
	for idx := range table {
		info := table[idx]
		if info.PID == root.PID {
			snapshotRoot = &table[idx]
		}
		if info.PID <= 1 || info.IsZombie() {
			continue
		}
		byParent[info.PPID] = append(byParent[info.PPID], info)
	}
	if snapshotRoot == nil {
		return nil, fmt.Errorf("%w: root pid %d is not in the process table", ErrNoProcess, root.PID)
	}
	if !sameIdentity(*snapshotRoot, root) {
		return nil, fmt.Errorf("%w: root pid %d changed identity during the scan", ErrNoProcess, root.PID)
	}

	// Breadth-first, parent before child, siblings in table order. children
	// records the accepted tree so a post-scan re-check can prune whole
	// subtrees.
	accepted := map[int]ProcInfo{root.PID: root}
	children := map[int][]int{}
	var order []ProcInfo
	queue := []int{root.PID}
	for len(queue) > 0 {
		parentPID := queue[0]
		queue = queue[1:]
		parent := accepted[parentPID]
		for _, child := range byParent[parentPID] {
			if _, seen := accepted[child.PID]; seen {
				continue
			}
			cmp, err := CompareStart(p, child.StartID, parent.StartID)
			if err != nil || cmp < 0 {
				continue // not comparable, or older than its parent: not its child
			}
			accepted[child.PID] = child
			children[parentPID] = append(children[parentPID], child.PID)
			order = append(order, child)
			queue = append(queue, child.PID)
		}
	}

	// Post-scan identity check of every interior node. Only nodes with
	// accepted children matter: a leaf that died or was recycled after its row
	// was read is recorded with the identity we saw, and verification later
	// reports it gone or stranger — never signalled.
	pruned := map[int]bool{}
	var prune func(pid int)
	prune = func(pid int) {
		for _, child := range children[pid] {
			pruned[child] = true
			prune(child)
		}
	}
	for pid := range children {
		if pruned[pid] {
			continue
		}
		live, err := p.Inspect(pid)
		if err != nil || !sameIdentity(live, accepted[pid]) {
			if pid == root.PID {
				return nil, fmt.Errorf("%w: root pid %d changed identity during the scan", ErrNoProcess, root.PID)
			}
			pruned[pid] = true
			prune(pid)
		}
	}

	out := make([]ProcInfo, 0, len(order))
	for _, info := range order {
		if pruned[info.PID] {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// sameIdentity reports whether two observations are of the same process.
func sameIdentity(a, b ProcInfo) bool {
	return a.PID == b.PID && a.StartID == b.StartID && a.UID == b.UID
}
