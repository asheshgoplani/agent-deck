package session

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

type groupStorageSnapshot struct {
	dbPath   string
	original *statedb.GroupRow
	stored   *statedb.GroupRow
	ensure   bool
}

type groupDeletion struct {
	completed atomic.Bool
	dbPath    string
	update    statedb.GroupSnapshot
}
type groupDeletionQueue struct {
	mu    sync.Mutex
	items []*groupDeletion
}

func groupToRow(group *Group) *statedb.GroupRow {
	return &statedb.GroupRow{Path: group.Path, Name: group.Name, Expanded: group.Expanded,
		Order: group.Order, DefaultPath: group.DefaultPath, MaxConcurrent: group.MaxConcurrent}
}

func groupDataToRow(group *GroupData) *statedb.GroupRow {
	return &statedb.GroupRow{Path: group.Path, Name: group.Name, Expanded: group.Expanded,
		Order: group.Order, DefaultPath: group.DefaultPath, MaxConcurrent: group.MaxConcurrent}
}

func (tree *GroupTree) initializeDerivedSnapshots() {
	for _, group := range tree.GroupList {
		if group.derived && group.storageSnapshot.Load() == nil {
			group.storageSnapshot.Store(&groupStorageSnapshot{original: groupToRow(group), ensure: true})
		}
	}
}

func (tree *GroupTree) queueGroupDeletion(group *Group) {
	if tree.groupDeletes == nil {
		tree.groupDeletes = &groupDeletionQueue{}
	}
	deletion := &groupDeletion{update: statedb.GroupSnapshot{Original: groupToRow(group)}}
	if snapshot := group.storageSnapshot.Load(); snapshot != nil {
		deletion.dbPath = snapshot.dbPath
		deletion.update.Original, deletion.update.Stored = snapshot.original, snapshot.stored
	}
	tree.groupDeletes.mu.Lock()
	tree.groupDeletes.items = append(tree.groupDeletes.items, deletion)
	tree.groupDeletes.mu.Unlock()
}

func (tree *GroupTree) deletionSnapshot() []*groupDeletion {
	if tree.frozenDeletes {
		return tree.savedDeletes
	}
	if tree.groupDeletes == nil {
		return nil
	}
	tree.groupDeletes.mu.Lock()
	defer tree.groupDeletes.mu.Unlock()
	return append([]*groupDeletion(nil), tree.groupDeletes.items...)
}

type groupSaveBatch struct {
	updates     []statedb.GroupSnapshot
	origins     []*groupStorageSnapshot
	groups      []*Group
	deletes     []*groupDeletion
	deleteQueue *groupDeletionQueue
}

func (s *Storage) prepareGroupSave(tree *GroupTree) (groupSaveBatch, error) {
	var batch groupSaveBatch
	if tree == nil {
		return batch, nil
	}
	batch.groups = tree.GroupList
	batch.origins = make([]*groupStorageSnapshot, len(batch.groups))
	for i, group := range batch.groups {
		update := statedb.GroupSnapshot{Desired: groupToRow(group)}
		snapshot := group.storageSnapshot.Load()
		batch.origins[i] = snapshot
		if snapshot != nil && (snapshot.dbPath == "" || snapshot.dbPath == s.dbPath) {
			update.Original, update.Stored, update.Ensure = snapshot.original, snapshot.stored, snapshot.ensure
		}
		batch.updates = append(batch.updates, update)
	}
	batch.deleteQueue = tree.groupDeletes
	for _, deletion := range tree.deletionSnapshot() {
		if deletion.completed.Load() {
			continue
		}
		batch.deletes = append(batch.deletes, deletion)
		update := deletion.update
		if deletion.dbPath != "" && deletion.dbPath != s.dbPath {
			return groupSaveBatch{}, fmt.Errorf("group deletion belongs to a different database: %s", update.Original.Path)
		}
		batch.updates = append(batch.updates, update)
	}
	return batch, nil
}

func (s *Storage) finishGroupSave(batch groupSaveBatch, committed []*statedb.GroupRow) {
	for i, group := range batch.groups {
		next := &groupStorageSnapshot{dbPath: s.dbPath,
			original: statedb.CloneGroupRow(batch.updates[i].Desired), stored: statedb.CloneGroupRow(committed[i]),
			ensure: batch.updates[i].Ensure && committed[i] == nil}
		// A copied payload owns its captured origin. Advance its live owner only
		// if that owner still has the same origin; an older receipt cannot undo
		// a newer save. Atomic pointers avoid touching live mutable group fields.
		for owner := group; owner != nil; owner = owner.saveOwner {
			owner.storageSnapshot.CompareAndSwap(batch.origins[i], next)
		}
	}
	if batch.deleteQueue == nil || len(batch.deletes) == 0 {
		return
	}
	completed := make(map[*groupDeletion]bool, len(batch.deletes))
	for _, deletion := range batch.deletes {
		deletion.completed.Store(true)
		completed[deletion] = true
	}
	batch.deleteQueue.mu.Lock()
	remaining := batch.deleteQueue.items[:0]
	for _, deletion := range batch.deleteQueue.items {
		if !completed[deletion] {
			remaining = append(remaining, deletion)
		}
	}
	batch.deleteQueue.items = remaining
	batch.deleteQueue.mu.Unlock()
}
