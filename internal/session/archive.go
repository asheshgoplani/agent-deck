package session

import "time"

// IsArchived reports whether the session is in the user archive. It reads
// ArchivedAt under the instance's archive lock: the TUI renders, filters and
// looks up conductors on other goroutines than the background commands that
// archive, unarchive, restart from the archive or switch harness, which
// write it through SetArchivedAtThreadSafe. That lock guards ArchivedAt
// alone, so a render never waits on a status sweep holding mu.
func (i *Instance) IsArchived() bool {
	if i == nil {
		return false
	}
	i.archiveMu.RLock()
	defer i.archiveMu.RUnlock()
	return !i.ArchivedAt.IsZero()
}

// ArchivedAtThreadSafe returns ArchivedAt, read under the archive lock.
func (i *Instance) ArchivedAtThreadSafe() time.Time {
	i.archiveMu.RLock()
	defer i.archiveMu.RUnlock()
	return i.ArchivedAt
}

// SetArchivedAtThreadSafe sets ArchivedAt under the archive lock. The zero
// time unarchives the session.
func (i *Instance) SetArchivedAtThreadSafe(t time.Time) {
	i.archiveMu.Lock()
	i.ArchivedAt = t
	i.archiveMu.Unlock()
}

// FilterInstancesByArchive returns instances whose archive state matches archived.
func FilterInstancesByArchive(instances []*Instance, archived bool) []*Instance {
	if len(instances) == 0 {
		return nil
	}
	out := make([]*Instance, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		isArchived := inst.IsArchived()
		if archived == isArchived {
			out = append(out, inst)
		}
	}
	return out
}

// VisibleInstances returns instances excluding archived rows retained for
// cross-harness recovery (superseded sources) — the canonical tracked set
// that list --json, status --json, TUI header counts and group preview panels
// must all derive from, so aggregates never diverge from the enumerable list.
func VisibleInstances(instances []*Instance) []*Instance {
	visible := make([]*Instance, 0, len(instances))
	for _, inst := range instances {
		if inst != nil && !(inst.IsArchived() && inst.SupersededBy != "") {
			visible = append(visible, inst)
		}
	}
	return visible
}

// SameArchivePartition reports whether two sessions sit in the same archive
// partition — both active, or both archived.
//
// The deck renders exactly one partition at a time: rebuildFlatItems keeps
// only the rows whose IsArchived matches the current view, so an archived
// session is not on screen in the active list and vice versa. Ordering
// operations on GroupTree therefore have to pick their neighbour from the
// same partition as the session being moved; a neighbour from the other
// partition is a row the user cannot see, which makes the move look like it
// did nothing.
func SameArchivePartition(a, b *Instance) bool {
	return a.IsArchived() == b.IsArchived()
}

// ArchivedClaudeSessionIDHolder returns the ARCHIVED record, other than self,
// that already holds sessionID as its Claude conversation id, or nil.
//
// Issue #2683: the save-time duplicate guard (UpdateClaudeSessionsWithDedup)
// keeps the id on the oldest record and silently clears it on the newer one,
// archived or not. An archived holder is invisible in the normal list, so
// `add --resume-session <id>` quietly launched a fresh conversation and
// `session set <x> claude-session-id <id>` printed success and then reverted.
// Callers use this to refuse up front, naming the holder.
func ArchivedClaudeSessionIDHolder(instances []*Instance, self *Instance, sessionID string) *Instance {
	if sessionID == "" {
		return nil
	}
	for _, inst := range instances {
		if inst == nil || inst == self || (self != nil && inst.ID == self.ID) {
			continue
		}
		if !inst.IsArchived() || !IsClaudeCompatible(inst.Tool) {
			continue
		}
		if inst.ClaudeSessionID == sessionID {
			return inst
		}
	}
	return nil
}
