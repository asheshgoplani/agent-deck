package ui

import "github.com/asheshgoplani/agent-deck/internal/session"

// Manual ordering of remote session rows (#1875).
//
// Remote rows are RemoteSessionInfo values fetched over SSH, not Instances
// living in groupTree, so groupTree.MoveSessionUp/Down has nothing to move.
// The order is therefore kept as a purely local overlay: a
// map[bucketKey][]sessionID on Home, applied when the flat item list is built
// and persisted in ui_state next to the other view preferences. Nothing is
// sent to the remote — ordering is a viewing preference of the machine doing
// the viewing, and a remote-authoritative reorder would need a new subcommand
// plus a wire protocol that silently breaks against older remote binaries.
//
// The overlay drifts by design: sessions come and go on the other machine
// without asking us. The rules, in one place, are
//
//   - stale IDs (in the overlay, gone from the remote) are skipped;
//   - unseen sessions (on the remote, absent from the overlay) keep their
//     natural position, i.e. the index the remote's own listing gave them;
//   - every fetched session is emitted exactly once.
//
// The last rule is the important one: getting it wrong either duplicates a row
// or makes one vanish, both worse than the no-op this fixes.

// remoteOrderKey is the overlay bucket key: one ordering per remote per group
// path. It matches the Path that buildRemoteFlatItems stamps on the rows of
// that bucket ("remotes/<remote>/<group>"), so two remotes owning a same-named
// group never share an ordering, and IDs — never titles — are the values, so
// two identically titled sessions on different hosts cannot collide either.
func remoteOrderKey(remoteName, groupPath string) string {
	return "remotes/" + remoteName + "/" + groupPath
}

// applyRemoteSessionOrder returns natural (session IDs in the order the remote
// listed them) permuted by the overlay order.
//
// The permutation is positional: the natural indices held by sessions the
// overlay knows about are the movable slots, and they are refilled in overlay
// order. A session the overlay has never seen is not a slot, so it stays at
// the exact index the remote gave it rather than being flushed to the end.
//
// The result is always a permutation of natural — same length, same multiset
// of IDs — so no row can be dropped or doubled. If natural somehow contains a
// duplicate ID the slot mapping is no longer 1:1, and the fetched order is
// returned untouched rather than risking that invariant.
func applyRemoteSessionOrder(natural, order []string) []string {
	out := make([]string, len(natural))
	copy(out, natural)
	if len(order) == 0 || len(natural) < 2 {
		return out
	}

	present := make(map[string]bool, len(natural))
	for _, id := range natural {
		if id == "" {
			continue // an ID-less row can never be addressed by the overlay
		}
		present[id] = true
	}

	// Overlay entries that still exist on the remote, overlay order, deduped.
	ordered := make([]string, 0, len(order))
	known := make(map[string]bool, len(order))
	for _, id := range order {
		if !present[id] || known[id] {
			continue // stale overlay entry, or a repeat within the overlay
		}
		known[id] = true
		ordered = append(ordered, id)
	}
	if len(ordered) == 0 {
		return out
	}

	slots := make([]int, 0, len(ordered))
	for i, id := range natural {
		if known[id] {
			slots = append(slots, i)
		}
	}
	if len(slots) != len(ordered) {
		return out // duplicate IDs on the remote: bail rather than lose a row
	}
	for k, i := range slots {
		out[i] = ordered[k]
	}
	return out
}

// orderRemoteBucket permutes one group bucket's indices into sessions by the
// overlay, leaving the caller's slice untouched.
func orderRemoteBucket(sessions []session.RemoteSessionInfo, idxs []int, order []string) []int {
	if len(order) == 0 || len(idxs) < 2 {
		return idxs
	}

	natural := make([]string, len(idxs))
	byID := make(map[string]int, len(idxs))
	for k, idx := range idxs {
		natural[k] = sessions[idx].ID
		byID[sessions[idx].ID] = idx
	}
	if len(byID) != len(idxs) {
		return idxs // duplicate (or empty) IDs: not addressable, leave as fetched
	}

	want := applyRemoteSessionOrder(natural, order)
	out := make([]int, 0, len(idxs))
	for _, id := range want {
		out = append(out, byID[id])
	}
	return out
}
