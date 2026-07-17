package ui

import (
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// buildRemoteFlatItems renders a single remote's sessions as a nested
// group tree instead of a flat Level-1 dump (#1553).
//
// Before this helper the remote-append loop in rebuildFlatItems emitted one
// ItemTypeRemoteGroup header per remote and appended every session flat at
// Level 1, discarding sessions[i].Group even though RemoteSessionInfo.Group
// crosses the wire (internal/session/ssh.go). This buckets each remote's
// sessions by their Group path (empty Group -> session.DefaultGroupPath),
// emits intermediate headers for nested "a/b/c" paths, and places sessions one
// level below their owning group — mirroring how the local tree nests groups.
//
// The returned slice always starts with the Level-0 remote header
// (Path = "remotes/<name>"), so callers append it directly. Sub-group headers
// carry Path = "remotes/<name>/<group-path>" so cursor-identity restore, which
// matches ItemTypeRemoteGroup on RemoteName+Path, keeps working unchanged.
func buildRemoteFlatItems(remoteName string, sessions []session.RemoteSessionInfo) []session.Item {
	items := make([]session.Item, 0, len(sessions)+2)

	// Level-0 remote header. Rendering/latency for this row is unchanged.
	items = append(items, session.Item{
		Type:       session.ItemTypeRemoteGroup,
		RemoteName: remoteName,
		Path:       "remotes/" + remoteName,
		Level:      0,
	})

	// Bucket sessions by normalized group path. Preserve input order within a
	// bucket (the fetch layer already sorted them) by tracking indices.
	buckets := make(map[string][]int)
	for i := range sessions {
		g := normalizeRemoteGroupPath(sessions[i].Group)
		buckets[g] = append(buckets[g], i)
	}

	// Sort group paths lexicographically. Lexicographic order places a parent
	// path directly before all of its descendants ("a" < "a/b" < "a/c" < "b"),
	// which lets us emit intermediate headers with a simple prefix walk.
	groupPaths := make([]string, 0, len(buckets))
	for g := range buckets {
		groupPaths = append(groupPaths, g)
	}
	sort.Strings(groupPaths)

	emitted := make(map[string]bool) // group paths whose header we already wrote

	for _, gp := range groupPaths {
		// Emit a header for every not-yet-emitted prefix of this group path so
		// nested "a/b/c" gets headers for "a", "a/b", "a/b/c" in order.
		segments := strings.Split(gp, "/")
		prefix := ""
		for depth, seg := range segments {
			if prefix == "" {
				prefix = seg
			} else {
				prefix = prefix + "/" + seg
			}
			if emitted[prefix] {
				continue
			}
			emitted[prefix] = true
			items = append(items, session.Item{
				Type:       session.ItemTypeRemoteGroup,
				RemoteName: remoteName,
				Path:       "remotes/" + remoteName + "/" + prefix,
				Level:      depth + 1, // Level 0 is the remote header
			})
		}

		// Sessions sit one level below their owning group header.
		sessionLevel := len(segments) + 1
		idxs := buckets[gp]
		for j, idx := range idxs {
			items = append(items, session.Item{
				Type:          session.ItemTypeRemoteSession,
				RemoteSession: &sessions[idx],
				RemoteName:    remoteName,
				Path:          "remotes/" + remoteName + "/" + gp,
				Level:         sessionLevel,
				IsLastInGroup: j == len(idxs)-1,
			})
		}
	}

	return items
}

// normalizeRemoteGroupPath maps an empty remote group to the default group
// path so ungrouped remote sessions nest under "my-sessions" just like local
// ungrouped sessions.
func normalizeRemoteGroupPath(group string) string {
	g := strings.Trim(strings.TrimSpace(group), "/")
	if g == "" {
		return session.DefaultGroupPath
	}
	return g
}

// remoteSubGroupCount counts sessions belonging to a remote sub-group header
// (Path "remotes/<name>/<group>") or any of its descendant groups. Used by the
// header renderer to show a subtree count without threading a count field
// through session.Item.
func remoteSubGroupCount(sessions []session.RemoteSessionInfo, groupPath string) int {
	count := 0
	for i := range sessions {
		g := normalizeRemoteGroupPath(sessions[i].Group)
		if g == groupPath || strings.HasPrefix(g, groupPath+"/") {
			count++
		}
	}
	return count
}
