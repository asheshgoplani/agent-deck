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
//
// collapsed holds the header paths the user has folded shut, keyed exactly like
// Item.Path. A collapsed header is still emitted — only its descendants are
// withheld — so the row stays on screen and can be reopened. Passing nil emits
// the full tree, which is what every caller did before collapsing existed.
func buildRemoteFlatItems(remoteName string, sessions []session.RemoteSessionInfo, collapsed map[string]bool) []session.Item {
	return buildRemoteFlatItemsOrdered(remoteName, sessions, collapsed, nil)
}

// buildRemoteFlatItemsOrdered is buildRemoteFlatItems with the manual
// row-order overlay (#1875) applied. order holds THIS remote's group path ->
// session IDs (i.e. remoteOrder.forRemote(remoteName)) and may be nil, in
// which case each bucket keeps the order the remote listed.
func buildRemoteFlatItemsOrdered(remoteName string, sessions []session.RemoteSessionInfo, collapsed map[string]bool, order map[string][]string) []session.Item {
	return buildRemoteFlatItemsWithGroups(remoteName, sessions, collapsed, order, nil)
}

// buildRemoteFlatItemsWithGroups is buildRemoteFlatItemsWithEmptyGroups with
// empty groups left out: only groups that currently hold a session get a
// header row. Filtered views (status, time, archived) use this form so a
// filter never surfaces an empty folder as if it matched.
func buildRemoteFlatItemsWithGroups(remoteName string, sessions []session.RemoteSessionInfo, collapsed map[string]bool, order map[string][]string, groupPaths []string) []session.Item {
	return buildRemoteFlatItemsWithEmptyGroups(remoteName, sessions, collapsed, order, groupPaths, false)
}

// buildRemoteFlatItemsWithEmptyGroups is buildRemoteFlatItemsOrdered with the
// remote's OWN group order applied to the group headers. groupPaths is the
// remote's group list as `group list --json` returned it (see
// SSHRunner.FetchGroupPaths): siblings in the remote's persisted order, a
// parent before its children. It may be nil or incomplete, in which case the
// groups it does not mention keep their lexicographic order, so a remote too
// old to report the list, or a group seen only on a session, renders exactly
// as before.
//
// With includeEmpty set, every path in groupPaths also gets a header row even
// when no session lives in it, so a group just created on the remote (or one
// emptied by moves) stays visible and addressable, the way an empty local
// group renders as "name (0)". This is the remote's own list, so a remote too
// old to report one simply shows no empty groups.
func buildRemoteFlatItemsWithEmptyGroups(remoteName string, sessions []session.RemoteSessionInfo, collapsed map[string]bool, order map[string][]string, groupPaths []string, includeEmpty bool) []session.Item {
	items := make([]session.Item, 0, len(sessions)+2)

	remoteRoot := "remotes/" + remoteName

	// Level-0 remote header. Rendering/latency for this row is unchanged.
	items = append(items, session.Item{
		Type:       session.ItemTypeRemoteGroup,
		RemoteName: remoteName,
		Path:       remoteRoot,
		Level:      0,
	})

	// Whole remote folded shut: the header alone, no groups and no sessions.
	if collapsed[remoteRoot] {
		return items
	}

	// Bucket sessions by normalized group path. Preserve input order within a
	// bucket (the fetch layer already sorted them) by tracking indices.
	buckets := make(map[string][]int)
	for i := range sessions {
		g := normalizeRemoteGroupPath(sessions[i].Group)
		buckets[g] = append(buckets[g], i)
	}

	// Sort group paths so that a parent path lands directly before all of its
	// descendants ("a" < "a/b" < "a/c" < "b"), which lets us emit intermediate
	// headers with a simple prefix walk. Siblings follow the remote's own
	// group order where it is known and their names otherwise.
	bucketPaths := make([]string, 0, len(buckets)+len(groupPaths))
	for g := range buckets {
		bucketPaths = append(bucketPaths, g)
	}
	if includeEmpty {
		for _, p := range groupPaths {
			g := normalizeRemoteGroupPath(p)
			if _, has := buckets[g]; !has {
				buckets[g] = nil // header only, no session rows
				bucketPaths = append(bucketPaths, g)
			}
		}
	}
	sortRemoteGroupPaths(bucketPaths, remoteGroupRank(groupPaths))

	emitted := make(map[string]bool) // group paths whose header we already wrote

	for _, gp := range bucketPaths {
		// Emit a header for every not-yet-emitted prefix of this group path so
		// nested "a/b/c" gets headers for "a", "a/b", "a/b/c" in order.
		segments := strings.Split(gp, "/")
		prefix := ""
		hidden := false // an ancestor header (or gp itself) is collapsed
		for depth, seg := range segments {
			if prefix == "" {
				prefix = seg
			} else {
				prefix = prefix + "/" + seg
			}
			// Set by the previous iteration: everything below that ancestor
			// stays unwritten, including the deeper headers of this path.
			if hidden {
				break
			}
			full := remoteRoot + "/" + prefix
			if !emitted[prefix] {
				emitted[prefix] = true
				items = append(items, session.Item{
					Type:       session.ItemTypeRemoteGroup,
					RemoteName: remoteName,
					Path:       full,
					Level:      depth + 1, // Level 0 is the remote header
				})
			}
			// Checked outside the emit guard: a header emitted while walking an
			// earlier sibling path must still hide this path's descendants.
			if collapsed[full] {
				hidden = true
			}
		}
		if hidden {
			continue // sessions of a collapsed group stay folded away
		}

		// Sessions sit one level below their owning group header.
		sessionLevel := len(segments) + 1
		// #1875: the user's manual order for this bucket, if any.
		idxs := orderRemoteBucket(sessions, buckets[gp], order[gp])
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

// remoteGroupRank maps each remote group path to its index in the remote's
// own listing. A nil or empty listing yields an empty map, and every path
// then falls back to name order in sortRemoteGroupPaths.
func remoteGroupRank(groupPaths []string) map[string]int {
	rank := make(map[string]int, len(groupPaths))
	for i, p := range groupPaths {
		p = normalizeRemoteGroupPath(p)
		if _, seen := rank[p]; !seen {
			rank[p] = i
		}
	}
	return rank
}

// sortRemoteGroupPaths orders group paths for the header walk in
// buildRemoteFlatItemsWithGroups: a parent always precedes its descendants,
// and two paths that part ways at some segment are ordered by the remote's
// rank of the prefixes ending in that segment. Prefixes the remote did not
// rank compare by name, and a ranked prefix precedes an unranked one, which
// mirrors how the remote's own list puts persisted groups before anything
// that exists only as a session's Group string.
func sortRemoteGroupPaths(paths []string, rank map[string]int) {
	sort.SliceStable(paths, func(i, j int) bool {
		return remoteGroupPathLess(paths[i], paths[j], rank)
	})
}

func remoteGroupPathLess(a, b string, rank map[string]int) bool {
	sa := strings.Split(a, "/")
	sb := strings.Split(b, "/")
	prefixA, prefixB := "", ""
	for k := 0; k < len(sa) && k < len(sb); k++ {
		prefixA = joinGroupSegment(prefixA, sa[k])
		prefixB = joinGroupSegment(prefixB, sb[k])
		if sa[k] == sb[k] {
			continue
		}
		ra, okA := rank[prefixA]
		rb, okB := rank[prefixB]
		switch {
		case okA && okB:
			return ra < rb
		case okA != okB:
			return okA
		default:
			return sa[k] < sb[k]
		}
	}
	return len(sa) < len(sb) // the shorter path is the ancestor
}

func joinGroupSegment(prefix, seg string) string {
	if prefix == "" {
		return seg
	}
	return prefix + "/" + seg
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

// remoteStatusCounts aggregates running/waiting session counts for a remote
// group header, mirroring the local group-header status glyphs (#1864 parity).
// An empty groupPath aggregates the whole remote (the Level-0 host header).
func remoteStatusCounts(sessions []session.RemoteSessionInfo, groupPath string) (running, waiting int) {
	for i := range sessions {
		if groupPath != "" {
			g := normalizeRemoteGroupPath(sessions[i].Group)
			if g != groupPath && !strings.HasPrefix(g, groupPath+"/") {
				continue
			}
		}
		// #1945: archiving tears down the pane but does NOT reset Status, so an
		// archived remote session keeps whatever it was doing when it was
		// archived — commonly "running". Counting it here makes the header
		// disagree with its own rows: #1944 gives the archived row the stopped
		// glyph while this tally still calls it running. rowStatusGlyph applies
		// the same override for exactly this reason; the counts have to follow
		// the same rule or the number and the glyphs describe different sets.
		if sessions[i].Archived {
			continue
		}
		switch sessions[i].Status {
		case "running":
			running++
		case "waiting":
			waiting++
		}
	}
	return running, waiting
}

// remoteHeaderCount is what a remote header row shows: the sessions under it
// (the whole remote for the host header, the subtree for a group header) and
// how many of those are running or waiting.
type remoteHeaderCount struct {
	total, running, waiting int
}

// remoteHeaderCounts computes the counts of every header row one remote's
// rows can have, keyed by Item.Path, in one pass over the sessions: the host
// header ("remotes/<name>") and every group prefix a session's Group path
// implies. The numbers equal remoteSubGroupCount and remoteStatusCounts for
// the same slice; computing them once with the rows spares the renderer a
// rescan of every session for every visible header on every frame.
func remoteHeaderCounts(remoteName string, sessions []session.RemoteSessionInfo) map[string]remoteHeaderCount {
	root := "remotes/" + remoteName
	counts := make(map[string]remoteHeaderCount)
	add := func(path string, running, waiting bool) {
		c := counts[path]
		c.total++
		if running {
			c.running++
		}
		if waiting {
			c.waiting++
		}
		counts[path] = c
	}
	for i := range sessions {
		// Archived rows count as sessions but never as running or waiting,
		// matching remoteStatusCounts (#1945).
		running := !sessions[i].Archived && sessions[i].Status == "running"
		waiting := !sessions[i].Archived && sessions[i].Status == "waiting"
		add(root, running, waiting)
		prefix := ""
		for _, seg := range strings.Split(normalizeRemoteGroupPath(sessions[i].Group), "/") {
			prefix = joinGroupSegment(prefix, seg)
			add(root+"/"+prefix, running, waiting)
		}
	}
	return counts
}
