package session

// FilterArchived applies the archive-visibility rule to an already-flattened
// item list, mirroring the post-flatten status-filter machinery in the UI.
//
// When showArchived is true the list is returned unchanged — archived rows
// stay where SortInstancesByActionable placed them (bottom of their group)
// and the renderer dims them.
//
// When showArchived is false, archived session rows are dropped, but every
// group header is kept. A group whose only sessions were archived therefore
// renders like a genuinely-empty group (header, no visible rows) rather than
// vanishing from the tree — the same end-state as a group after its last
// session is *removed*, so archiving and removing stay consistent and a
// subgroup's structure never silently disappears.
//
// keepIDs lists archived session IDs that should stay visible despite
// showArchived being false. This powers the "just-archived" affordance: the row
// a user has only this moment archived remains selected and on-screen (dimmed),
// so a second press of the archive key undoes it, and it hides on the next
// rebuild once the cursor moves away. An empty / non-matching ID has no effect.
func FilterArchived(items []Item, showArchived bool, keepIDs ...string) []Item {
	if showArchived {
		return items
	}

	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		if id != "" {
			keep[id] = true
		}
	}

	// visible == not archived, OR explicitly kept (just-archived sticky row).
	visible := func(s *Instance) bool {
		return s != nil && (!s.IsArchived() || keep[s.ID])
	}

	// Drop archived session rows (unless kept); keep group headers and every
	// other row untouched.
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Type == ItemTypeSession {
			if visible(it.Session) {
				out = append(out, it)
			}
			continue
		}
		out = append(out, it)
	}
	return out
}
