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
