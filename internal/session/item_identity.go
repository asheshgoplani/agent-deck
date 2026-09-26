package session

import "strconv"

// Identity returns the key that names this row uniquely within one frame of
// the session list, and false for rows that carry no identity (dividers).
// Every list row type has one: local group (path), remote group (remote,
// path), local session (id), creating placeholder (temp id), remote session
// (remote, id) and window (session id, window id/index).
func (it Item) Identity() (string, bool) {
	switch it.Type {
	case ItemTypeGroup:
		path := it.Path
		if it.Group != nil {
			path = it.Group.Path
		}
		return "group\x00" + path, true
	case ItemTypeRemoteGroup:
		return "remote-group\x00" + it.RemoteName + "\x00" + it.Path, true
	case ItemTypeSession:
		if it.Session != nil {
			return "session\x00" + it.Session.ID, true
		}
		if it.CreatingID != "" {
			return "creating\x00" + it.CreatingID, true
		}
	case ItemTypeRemoteSession:
		if it.RemoteSession != nil {
			return "remote-session\x00" + it.RemoteName + "\x00" + it.RemoteSession.ID, true
		}
	case ItemTypeWindow:
		return "window\x00" + it.WindowSessionID + "\x00" + it.WindowID + "\x00" + strconv.Itoa(it.WindowIndex), true
	}
	return "", false
}

// FirstDuplicateRow reports the index and identity of the first row whose
// identity already appeared earlier in items. GroupViewMode partitioning
// (active-on-top / populated-on-top) intentionally repeats a group header once
// per section, so callers must not use it on a partitioned list.
func FirstDuplicateRow(items []Item) (int, string, bool) {
	seen := make(map[string]struct{}, len(items))
	for i, it := range items {
		id, ok := it.Identity()
		if !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			return i, id, true
		}
		seen[id] = struct{}{}
	}
	return 0, "", false
}
