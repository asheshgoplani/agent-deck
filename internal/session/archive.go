package session

import "time"

// IsArchived reports whether the session is in the user archive.
func (i *Instance) IsArchived() bool {
	return i != nil && !i.ArchivedAt.IsZero()
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
		isArchived := !inst.ArchivedAt.IsZero()
		if archived == isArchived {
			out = append(out, inst)
		}
	}
	return out
}

// ArchiveTimeUTC returns the archive timestamp in UTC, or zero when not archived.
func ArchiveTimeUTC(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
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
