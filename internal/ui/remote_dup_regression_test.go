// Regression coverage for the P1 visual-corruption bug reported live on
// v1.16.13: duplicate remote group/session rows (the same header shown 2-4
// times with different counts and poll timers) and duplicate preview blocks
// (the same "Viewers:" header and body lines repeated verbatim).
//
// Root cause: a remote's own `list --json` answer is untrusted wire data.
// Nothing on the client enforced that it names each session id at most once
// within a single fetch — a misconfigured/oversized profile scope on the
// remote side, or a remote-side listing bug, can hand fetchOneRemote a
// slice with the same RemoteSessionInfo.ID appearing more than once (see
// established fact: `agent-deck -p personal list --json` on the affected
// remote returns 1 session while the TUI's cached fleet carried 70+ —
// something upstream of this merge point is already handing it more than
// one id-scope worth of rows). mergeRemoteSessions took that slice at face
// value and installed it as-is; buildRemoteFlatItemsWithEmptyGroups then
// buckets and renders every entry, so a same-id collision became two
// visible rows with two different Status/Group snapshots of "the same"
// session, and the group headers counting them disagreed with each other —
// exactly the symptom (root cause: internal/ui/home.go, mergeRemoteSessions,
// pre-fix at the line now calling dedupeRemoteSessionsByID).
//
// The fix de-duplicates by id at the one place every path into
// h.remoteSessions passes through — mergeRemoteSessions — so a refresh
// round replaces the model instead of accumulating into it, regardless of
// how many overlapping/out-of-order/concurrent fetches produced the input.
package ui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// assertNoDuplicateRows is the invariant every TUI test that builds a frame
// (a []session.Item slice from rebuildFlatItemsAt / buildRemoteFlatItems*)
// should hold: each local group Path appears once, each remote group
// (RemoteName, Path) pair appears once, each local session id appears once,
// and each (RemoteName, RemoteSession.ID) pair appears once. It intentionally
// does not defend against session.GroupViewMode partitioning (active-on-top /
// populated-on-top), which by design shows the same group header once per
// section (see rebuildFlatItemsAt's IsLastInGroup comment) — callers that
// exercise a non-Normal view mode are out of scope for this helper.
func assertNoDuplicateRows(t *testing.T, items []session.Item) {
	t.Helper()
	groupSeen := make(map[string]bool, len(items))
	sessionSeen := make(map[string]bool, len(items))
	for i, it := range items {
		switch it.Type {
		case session.ItemTypeGroup:
			if it.Group == nil {
				continue
			}
			key := "local:" + it.Group.Path
			if groupSeen[key] {
				t.Fatalf("row %d: duplicate local group header for path %q", i, it.Group.Path)
			}
			groupSeen[key] = true
		case session.ItemTypeRemoteGroup:
			key := "remote:" + it.RemoteName + "\x00" + it.Path
			if groupSeen[key] {
				t.Fatalf("row %d: duplicate remote group header for remote %q path %q", i, it.RemoteName, it.Path)
			}
			groupSeen[key] = true
		case session.ItemTypeSession:
			if it.Session == nil || it.IsCreatingPlaceholder() {
				continue
			}
			key := "local:" + it.Session.ID
			if sessionSeen[key] {
				t.Fatalf("row %d: duplicate local session row for id %q", i, it.Session.ID)
			}
			sessionSeen[key] = true
		case session.ItemTypeRemoteSession:
			if it.RemoteSession == nil {
				continue
			}
			key := "remote:" + it.RemoteName + "\x00" + it.RemoteSession.ID
			if sessionSeen[key] {
				t.Fatalf("row %d: duplicate remote session row for remote %q id %q", i, it.RemoteName, it.RemoteSession.ID)
			}
			sessionSeen[key] = true
		case session.ItemTypeWindow:
			key := "window:" + it.WindowSessionID + "\x00" + it.WindowID + "\x00" + strconv.Itoa(it.WindowIndex)
			if sessionSeen[key] {
				t.Fatalf("row %d: duplicate window row for session %q window %q", i, it.WindowSessionID, it.WindowID)
			}
			sessionSeen[key] = true
		}
	}
}

// --- Table/property test over the tree builder --------------------------

// TestMergeRemoteSessions_RefreshSequences runs mergeRemoteSessions across a
// battery of refresh sequences (overlapping, out-of-order, slow-then-fast,
// error-then-success, empty-then-full, and a same-fetch id collision) and
// asserts the invariant holds after every step: the tree built from the
// resulting map never contains a duplicate group key or a duplicate session
// id, no matter what order or shape the refreshes arrived in.
func TestMergeRemoteSessions_RefreshSequences(t *testing.T) {
	type step struct {
		name    string
		fetched map[string][]session.RemoteSessionInfo
		failed  map[string]bool
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{
			name: "overlapping ids within one fetch (the reported bug)",
			steps: []step{
				{name: "first fetch has a same-id collision", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {
						remoteInfo("agentbox", "s1", "buddi-9-running", "running"),
						remoteInfo("agentbox", "s1", "buddi-8-waiting", "waiting"),
						remoteInfo("agentbox", "s2", "yasir", "idle"),
					},
				}},
			},
		},
		{
			name: "out-of-order: a stale full fetch lands after a fresher partial one",
			steps: []step{
				{name: "fresh", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {remoteInfo("agentbox", "s1", "fresh", "running")},
				}},
				{name: "stale duplicate id", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {remoteInfo("agentbox", "s1", "stale", "waiting"), remoteInfo("agentbox", "s1", "stale-again", "waiting")},
				}},
			},
		},
		{
			name: "slow-then-fast: two remotes complete in reverse start order",
			steps: []step{
				{name: "quiet remote answers first", fetched: map[string][]session.RemoteSessionInfo{
					"quiet": {remoteInfo("quiet", "q1", "q", "running")},
				}, failed: map[string]bool{"chatty": true}},
				{name: "chatty remote answers late", fetched: map[string][]session.RemoteSessionInfo{
					"chatty": {remoteInfo("chatty", "c1", "c", "running"), remoteInfo("chatty", "c1", "c-dup", "waiting")},
				}, failed: map[string]bool{"quiet": true}},
			},
		},
		{
			name: "error-then-success: a failed round keeps last-good, then a clean fetch replaces it",
			steps: []step{
				{name: "initial good fetch", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {remoteInfo("agentbox", "s1", "v1", "running")},
				}},
				{name: "ssh hiccup", fetched: nil, failed: map[string]bool{"agentbox": true}},
				{name: "recovered fetch has a collision", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {remoteInfo("agentbox", "s1", "v2", "running"), remoteInfo("agentbox", "s1", "v2-again", "running"), remoteInfo("agentbox", "s2", "new", "idle")},
				}},
			},
		},
		{
			name: "empty-then-full: a remote with zero sessions this round, then a full listing",
			steps: []step{
				{name: "empty", fetched: map[string][]session.RemoteSessionInfo{"agentbox": {}}},
				{name: "full with dup", fetched: map[string][]session.RemoteSessionInfo{
					"agentbox": {remoteInfo("agentbox", "s1", "a", "running"), remoteInfo("agentbox", "s1", "b", "waiting"), remoteInfo("agentbox", "s1", "c", "running")},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions := map[string][]session.RemoteSessionInfo{}
			for _, st := range tc.steps {
				sessions = mergeRemoteSessions(sessions, st.fetched, st.failed)
				var items []session.Item
				for name, sess := range sessions {
					items = append(items, buildRemoteFlatItemsWithEmptyGroups(name, sess, nil, nil, nil, false)...)
				}
				assertNoDuplicateRows(t, items)
			}
		})
	}
}

// TestDedupeRemoteSessionsByID_KeepsFreshestDataAtStablePosition pins the
// dedup contract directly: a same-id collision within one fetch keeps the
// LAST occurrence's data (the freshest the remote sent this round) but at
// the FIRST position, so row order stays stable across a round that
// happens to repeat an id, and an id-less session never dedupes against
// another id-less session.
func TestDedupeRemoteSessionsByID_KeepsFreshestDataAtStablePosition(t *testing.T) {
	in := []session.RemoteSessionInfo{
		remoteInfo("agentbox", "s1", "old", "running"),
		remoteInfo("agentbox", "s2", "unrelated", "idle"),
		remoteInfo("agentbox", "s1", "new", "waiting"),
		remoteInfo("agentbox", "", "no-id-a", "idle"),
		remoteInfo("agentbox", "", "no-id-b", "idle"),
	}
	out := dedupeRemoteSessionsByID(in)
	if len(out) != 4 {
		t.Fatalf("want 4 rows (s1 collapsed, s2, and both id-less rows kept), got %d: %+v", len(out), out)
	}
	if out[0].ID != "s1" || out[0].Title != "new" {
		t.Fatalf("want s1's kept row at position 0 with the LATEST data (\"new\"), got %+v", out[0])
	}
	if out[1].ID != "s2" || out[1].Title != "unrelated" {
		t.Fatalf("want s2 unaffected at position 1, got %+v", out[1])
	}
	if out[2].Title != "no-id-a" || out[3].Title != "no-id-b" {
		t.Fatalf("want both id-less rows kept (never deduped against each other), got %+v and %+v", out[2], out[3])
	}
	single := []session.RemoteSessionInfo{remoteInfo("agentbox", "s1", "only", "running")}
	if noDup := dedupeRemoteSessionsByID(single); len(noDup) != 1 || noDup[0].Title != "only" {
		t.Fatalf("no-collision input must survive untouched, got %+v", noDup)
	}
}

// --- Concurrency test (run under -race in CI) ----------------------------

// TestRemoteSessions_ConcurrentMergesStayDeduped mirrors the real production
// concurrency boundary: fetchOneRemote runs one goroutine per configured
// remote, each independently producing a result that gets merged into
// h.remoteSessions under h.remoteSessionsMu (see applyRemoteFetch). This
// fires many such merges at once, several of them carrying a same-id
// collision (the reported defect's shape), and asserts the final state is
// race-free and fully deduped regardless of interleaving.
func TestRemoteSessions_ConcurrentMergesStayDeduped(t *testing.T) {
	h := NewHome()
	defer h.cancel()
	h.remoteSessions = map[string][]session.RemoteSessionInfo{}

	const rounds = 64
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(round int) {
			defer wg.Done()
			sessions := []session.RemoteSessionInfo{
				remoteInfo("agentbox", "s1", fmt.Sprintf("round-%d-a", round), "running"),
				remoteInfo("agentbox", "s1", fmt.Sprintf("round-%d-b", round), "waiting"), // same id twice in one fetch
				remoteInfo("agentbox", "s2", fmt.Sprintf("round-%d-c", round), "idle"),
			}
			h.remoteSessionsMu.Lock()
			h.remoteSessions = mergeRemoteSessions(h.remoteSessions, map[string][]session.RemoteSessionInfo{"agentbox": sessions}, nil)
			h.remoteSessionsMu.Unlock()
		}(i)
	}
	wg.Wait()

	h.remoteSessionsMu.RLock()
	got := append([]session.RemoteSessionInfo(nil), h.remoteSessions["agentbox"]...)
	h.remoteSessionsMu.RUnlock()

	seen := map[string]bool{}
	for _, s := range got {
		if seen[s.ID] {
			t.Fatalf("duplicate session id %q survived %d concurrent merges: %+v", s.ID, rounds, got)
		}
		seen[s.ID] = true
	}
	if len(got) != 2 {
		t.Fatalf("want exactly 2 deduped sessions (s1, s2) after %d concurrent merges, got %d: %+v", rounds, len(got), got)
	}
}

// --- Preview-buffer test --------------------------------------------------

// TestRemotePreviewCache_WriteAlwaysReplaces pins the pre-existing
// (unmodified by this PR) applyRemotePane write semantics: it is the write
// path for every pushed pane capture (internal/session/remote_channel.go's
// "pane" event), and h.previewCache[key] is a plain map write, so a burst of
// pane pushes for the same session always settles on exactly the latest
// capture with exactly one viewer header - it can never accumulate the way
// an appended slice can. This test exercises no line this PR changed (both
// applyRemotePane and renderRemotePreview are byte-identical to main), so it
// is not a regression test for this PR's row/group dedup fix and does not
// demonstrate that fix also covers the separately reported preview-pane
// duplication; that symptom is tracked separately.
func TestRemotePreviewCache_WriteAlwaysReplaces(t *testing.T) {
	h := NewHome()
	defer h.cancel()
	h.width, h.height = 200, 50

	const remoteName, sessionID = "agentbox", "s1"
	rs := session.RemoteSessionInfo{ID: sessionID, Title: "worker", Status: "running"}
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteName: remoteName, RemoteSession: &rs}

	pushes := []string{
		"first capture line one\nfirst capture line two\n",
		"second capture line one\nsecond capture line two\n",
		"third capture line one\nthird capture line two\n",
	}
	for i, content := range pushes {
		h.applyRemotePane(remoteName, &session.RemotePaneEvent{Session: sessionID, Content: content})
		out := h.renderRemotePreview(item, 200, 50)

		if n := strings.Count(out, "Viewers:"); n != 1 {
			t.Fatalf("push %d: want exactly one \"Viewers:\" header, got %d in:\n%s", i, n, out)
		}
		for j, earlier := range pushes[:i] {
			if strings.Contains(out, strings.TrimSpace(strings.Split(earlier, "\n")[0])) {
				t.Fatalf("push %d: an earlier push's content (push %d, %q) resurfaced instead of being replaced:\n%s", i, j, earlier, out)
			}
		}
		wantLine := strings.TrimSpace(strings.Split(content, "\n")[0])
		if strings.Count(out, wantLine) != 1 {
			t.Fatalf("push %d: want the latest capture's first line exactly once, got %d occurrences in:\n%s", i, strings.Count(out, wantLine), out)
		}
	}
}

// --- Golden-shaped frame test at 200x50 ----------------------------------

// TestRemoteFrame200x50_MidRefreshNoDuplicates renders a remote group mid
// refresh (one round already landed with a same-id collision — the shape of
// the reported bug — followed by a second concurrent-looking round for a
// different remote) at the 200x50 size the maintainer's screenshots were
// taken at, and asserts the rendered row set and text have no duplicate
// group/session rows. Unlike the repo's other *_golden_test.go files this
// does not diff against a committed byte-for-byte fixture: generating that
// fixture requires running `go test` to capture the exact rendered bytes,
// which this change's SAFETY constraints forbid doing on this host. The
// invariant assertions below still make a real regression (a duplicate row
// or a duplicate rendered header line reappearing) fail loudly; a
// byte-exact fixture can be captured with UPDATE_GOLDEN-style tooling in a
// follow-up once CI has run this test once.
func TestRemoteFrame200x50_MidRefreshNoDuplicates(t *testing.T) {
	h := newTestHomeWithItems(200, 50, nil)
	defer h.cancel()
	forceTrueColorProfile()

	// Round 1 lands with the exact collision shape from the bug report:
	// the same session id twice with different Status/Group data.
	round1 := map[string][]session.RemoteSessionInfo{
		"agentbox": {
			{ID: "b1", RemoteName: "agentbox", Title: "conductor-buddi-sharjeel", Tool: "claude", Status: "running", Group: "buddi"},
			{ID: "b1", RemoteName: "agentbox", Title: "conductor-buddi-sharjeel", Tool: "claude", Status: "waiting", Group: "buddi"},
			{ID: "b2", RemoteName: "agentbox", Title: "fix-xdg-store-r2", Tool: "claude", Status: "running", Group: "buddi"},
			{ID: "y1", RemoteName: "agentbox", Title: "yasir-worker", Tool: "claude", Status: "running", Group: "yasir"},
		},
	}
	h.remoteSessions = mergeRemoteSessions(nil, round1, nil)
	h.remotePolls = map[string]session.RemotePollState{"agentbox": {LastPollStatus: "ok"}}
	h.rebuildFlatItems()
	assertNoDuplicateRows(t, h.flatItems)

	// Round 2: a second, overlapping-looking refresh for the same remote
	// (as if an earlier in-flight fetch's result landed after a newer one)
	// must still leave the tree fully replaced, not appended-to.
	round2 := map[string][]session.RemoteSessionInfo{
		"agentbox": {
			{ID: "b1", RemoteName: "agentbox", Title: "conductor-buddi-sharjeel", Tool: "claude", Status: "waiting", Group: "buddi"},
			{ID: "b2", RemoteName: "agentbox", Title: "fix-xdg-store-r2", Tool: "claude", Status: "running", Group: "buddi"},
			{ID: "y1", RemoteName: "agentbox", Title: "yasir-worker", Tool: "claude", Status: "idle", Group: "yasir"},
			{ID: "s1", RemoteName: "agentbox", Title: "sharjeel-task", Tool: "claude", Status: "running", Group: "sharjeel"},
		},
	}
	h.remoteSessions = mergeRemoteSessions(h.remoteSessions, round2, nil)
	h.rebuildFlatItems()
	assertNoDuplicateRows(t, h.flatItems)

	var frame strings.Builder
	for _, it := range h.flatItems {
		switch it.Type {
		case session.ItemTypeRemoteGroup:
			h.renderRemoteGroupItem(&frame, it, false, 200)
		case session.ItemTypeRemoteSession:
			h.renderRemoteSessionItem(&frame, it, false)
		}
	}
	got := stripAnsi(frame.String())

	if n := strings.Count(got, "remotes/agentbox"); n != 1 {
		t.Fatalf("want the agentbox host header exactly once in the 200x50 frame, got %d:\n%s", n, got)
	}
	// "buddi (2)" (the group header's name+count, e.g. "▾ buddi (2) ...")
	// rather than the bare substring "buddi": the session title
	// "conductor-buddi-sharjeel" also contains "buddi" and would otherwise
	// make this assertion count a real, unrelated row as a duplicate header.
	if n := strings.Count(got, "buddi (2)"); n != 1 {
		t.Fatalf("want the buddi sub-group header exactly once, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "conductor-buddi-sharjeel"); n != 1 {
		t.Fatalf("want the deduped session row exactly once, got %d:\n%s", n, got)
	}
}
