// Scale hardening of the pushed-change path (findings 1, 10, 11 and 12 of
// the remote scale review): a chatty pushing remote must not starve or
// hollow out the fleet poll for the quiet ones, a stale push must not bring
// back a row the user just removed, header counts and the on-disk snapshot
// must not cost a full rescan or a full rewrite per push, and a remote with
// zero sessions must keep its header and folders across foreign pushes.

package ui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func pushWithData(remote string, sessions []session.RemoteSessionInfo, groups []string) remoteChangedMsg {
	for i := range sessions {
		sessions[i].RemoteName = remote
	}
	return remoteChangedMsg{remoteName: remote, change: session.RemoteChange{Remote: remote, Sessions: sessions, Groups: groups, HasData: true}}
}

// Finding 1: a remote that pushes several listings a second coexists with a
// remote that only answers the fleet poll.
func TestRemotePush_ChattyRemoteDoesNotStarveThePoll(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"chatty": {remoteInfo("chatty", "c1", "chatty-cached", "running")},
		"quiet":  {remoteInfo("quiet", "q1", "quiet-cached", "running")},
	}
	home.remoteCosts = map[string]*costs.RemoteCostSummary{"quiet": {CostTodayMicrodollars: 5}}
	polledAt := time.Now().Add(-20 * time.Second)
	home.lastRemoteFetch = polledAt

	// Pushes land while the poll is due: they must not reset the poll clock
	// or raise the in-flight guard.
	h := home
	for i := 0; i < 3; i++ {
		model, _ := h.Update(pushWithData("chatty", []session.RemoteSessionInfo{{ID: "c1", Title: "chatty-push"}}, nil))
		h = model.(*Home)
	}
	if !h.lastRemoteFetch.Equal(polledAt) {
		t.Fatalf("a push must not touch lastRemoteFetch; got %v want %v", h.lastRemoteFetch, polledAt)
	}
	if h.remotesFetchActive {
		t.Fatal("a push must not raise the in-flight guard")
	}
	if !h.shouldFetchRemoteSessions(time.Now()) {
		t.Fatal("the fleet poll must still be due after a burst of pushes")
	}

	// The poll round starts; another push beats both of its results home.
	round := atomic.AddUint64(&h.remoteFetchSeq, 1)
	model, _ := h.Update(remoteFetchRoundMsg{gen: round, fetches: nil})
	h = model.(*Home)
	h.remoteSessionsMu.Lock()
	h.remoteFetchOutstanding = 2
	h.remoteSessionsMu.Unlock()
	model, _ = h.Update(pushWithData("chatty", []session.RemoteSessionInfo{{ID: "c1", Title: "chatty-newer"}}, nil))
	h = model.(*Home)

	result := func(name, title string, other string) remoteSessionsFetchedMsg {
		return remoteSessionsFetchedMsg{
			gen:          round,
			inRound:      true,
			sessions:     map[string][]session.RemoteSessionInfo{name: {remoteInfo(name, name+"1", title, "running")}},
			costs:        map[string]*costs.RemoteCostSummary{name: {CostTodayMicrodollars: 42}},
			groups:       map[string][]string{name: {name + "-group"}},
			failed:       map[string]bool{other: true},
			groupsFailed: map[string]bool{other: true},
		}
	}
	// The chatty remote's poll result is older than its push: rows stay
	// as pushed, but the cost figure and group list the push lacked apply,
	// and the result still counts toward the round.
	model, _ = h.Update(result("chatty", "chatty-polled", "quiet"))
	h = model.(*Home)
	if got := remoteTitles(h, "chatty"); len(got) != 1 || got[0] != "chatty-newer" {
		t.Fatalf("a poll result older than the push must not overwrite its rows; got %v", got)
	}
	if h.remoteCosts["chatty"] == nil || h.remoteCosts["chatty"].CostTodayMicrodollars != 42 {
		t.Fatalf("a superseded poll result must still deliver the cost summary; got %+v", h.remoteCosts["chatty"])
	}
	if got := h.remoteGroups["chatty"]; len(got) != 1 || got[0] != "chatty-group" {
		t.Fatalf("a superseded poll result must still deliver the group list the push lacked; got %v", got)
	}
	if !h.lastRemoteFetch.After(polledAt) {
		t.Fatal("a discarded poll result must advance lastRemoteFetch or the next tick re-polls at once")
	}
	if !h.remotesFetchActive {
		t.Fatal("the round is not over while the quiet remote is outstanding")
	}
	// The quiet remote's result applies in full.
	model, _ = h.Update(result("quiet", "quiet-polled", "chatty"))
	h = model.(*Home)
	if got := remoteTitles(h, "quiet"); len(got) != 1 || got[0] != "quiet-polled" {
		t.Fatalf("the quiet remote's poll result must apply; got %v", got)
	}
	if got := remoteTitles(h, "chatty"); len(got) != 1 || got[0] != "chatty-newer" {
		t.Fatalf("the quiet remote's result must keep the chatty remote's pushed rows; got %v", got)
	}
	if h.remotesFetchActive {
		t.Fatal("the last result of the round must release the in-flight guard even when another was discarded")
	}
	if h.remoteCosts["quiet"] == nil || h.remoteCosts["quiet"].CostTodayMicrodollars != 42 {
		t.Fatalf("the quiet remote's cost summary must apply; got %+v", h.remoteCosts["quiet"])
	}
}

// Finding 1: a push that brings its own group list is newer than a poll
// result that started earlier, so the poll's list must not replace it.
func TestRemotePush_StalePollDoesNotRegressPushedGroups(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"box": {}}
	round := atomic.AddUint64(&home.remoteFetchSeq, 1)
	model, _ := home.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "b1", Title: "b"}}, []string{"new-folder"}))
	h := model.(*Home)
	model, _ = h.Update(remoteSessionsFetchedMsg{
		gen:      round,
		inRound:  true,
		sessions: map[string][]session.RemoteSessionInfo{"box": {remoteInfo("box", "b1", "b-old", "running")}},
		groups:   map[string][]string{"box": {"old-folder"}},
		failed:   map[string]bool{},
	})
	h = model.(*Home)
	if got := h.remoteGroups["box"]; len(got) != 1 || got[0] != "new-folder" {
		t.Fatalf("a poll's group list older than the push's must not apply; got %v", got)
	}
}

// Finding 1: a poll that could not read the config must still advance the
// poll clock, or every tick re-reads the broken file and re-raises the error.
func TestRemotePoll_ConfigErrorAdvancesPollClock(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.lastRemoteFetch = time.Now().Add(-time.Minute)
	model, _ := home.Update(remoteSessionsFetchedMsg{gen: 1, configErr: errTest})
	h := model.(*Home)
	if h.shouldFetchRemoteSessions(time.Now()) {
		t.Fatal("a config error must not leave the poll due on the next tick")
	}
}

var errTest = &testError{"toml: bad key"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

// Finding 10: the agent's probe listed the DB before the delete ran and its
// push lands after the delete was confirmed; the row must stay gone.
func TestRemotePush_StaleListingAfterDeleteKeepsRowGone(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {remoteInfo("box", "s1", "doomed", "running"), remoteInfo("box", "s2", "kept", "running")},
	}
	home.setRemotePending("s1", "deleting…")
	if !home.remoteActionInProgress("box") {
		t.Fatal("a pending row must count as an action in progress for its remote")
	}
	model, _ := home.Update(remoteSessionDeletedMsg{remoteName: "box", sessionID: "s1", title: "doomed"})
	h := model.(*Home)
	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("delete confirmation must drop the row; got %v", got)
	}

	// The stale push still lists s1.
	model, _ = h.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "s1", Title: "doomed"}, {ID: "s2", Title: "kept"}}, nil))
	h = model.(*Home)
	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "kept" {
		t.Fatalf("a push listing the session the user just deleted must not resurrect it; got %v", got)
	}

	// A push that agrees with the delete applies, new rows included.
	model, _ = h.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "s2", Title: "kept"}, {ID: "s3", Title: "fresh"}}, nil))
	h = model.(*Home)
	if got := remoteTitles(h, "box"); len(got) != 2 || got[1] != "fresh" {
		t.Fatalf("a push that does not contain the removed session must apply; got %v", got)
	}

	// After the grace the remote's listing is the truth again, even if it
	// contains the id (a session recreated with the same id, say).
	h.remoteActionSettled["box"] = time.Now().Add(-2 * remoteActionGrace)
	h.remoteActionRemoved["box"]["s1"] = remoteActionRecord{at: time.Now().Add(-2 * remoteActionGrace)}
	h.remoteActionRemoved["box"]["s3"] = remoteActionRecord{at: time.Now().Add(-2 * remoteActionGrace)}
	model, _ = h.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "s3", Title: "back"}}, nil))
	h = model.(*Home)
	if got := remoteTitles(h, "box"); len(got) != 1 || got[0] != "back" {
		t.Fatalf("outside the grace a push must apply as usual; got %v", got)
	}
	if _, ok := h.remoteActionRemoved["box"]; ok {
		t.Fatal("expired removal records must be dropped")
	}
}

// Finding 10: an archive is confirmed, then a push listing the session as
// still active lands; the row must not pop back into the active view. A
// push that agrees with the archive applies.
func TestRemotePush_StaleListingAfterArchiveKeepsRowArchived(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {remoteInfo("box", "s1", "shelved", "running"), remoteInfo("box", "s2", "kept", "running")},
	}
	model, _ := home.Update(remoteSessionArchivedMsg{remoteName: "box", sessionID: "s1", title: "shelved", archived: true})
	h := model.(*Home)
	if !h.remoteSessions["box"][0].Archived {
		t.Fatal("archive confirmation must mark the row archived")
	}
	model, _ = h.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "s1", Title: "shelved", Status: "running"}, {ID: "s2", Title: "kept"}}, nil))
	h = model.(*Home)
	if !h.remoteSessions["box"][0].Archived {
		t.Fatal("a push listing the session the user just archived as active must not apply")
	}
	model, _ = h.Update(pushWithData("box", []session.RemoteSessionInfo{{ID: "s1", Title: "shelved", Archived: true}, {ID: "s2", Title: "kept-pushed"}}, nil))
	h = model.(*Home)
	if got := remoteTitles(h, "box"); len(got) != 2 || got[1] != "kept-pushed" {
		t.Fatalf("a push that agrees with the archive must apply; got %v", got)
	}
}

// Finding 10: a stale push is not dropped for unrelated remotes.
func TestRemotePush_DeleteOnOneRemoteDoesNotGateAnother(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"a": {remoteInfo("a", "s1", "doomed", "running")},
		"b": {remoteInfo("b", "s1", "twin", "running")},
	}
	model, _ := home.Update(remoteSessionDeletedMsg{remoteName: "a", sessionID: "s1", title: "doomed"})
	h := model.(*Home)
	model, _ = h.Update(pushWithData("b", []session.RemoteSessionInfo{{ID: "s1", Title: "twin-pushed"}}, nil))
	h = model.(*Home)
	if got := remoteTitles(h, "b"); len(got) != 1 || got[0] != "twin-pushed" {
		t.Fatalf("a delete on remote a must not gate pushes from remote b; got %v", got)
	}
}

// Finding 11: the counts computed once with the rows equal the per-header
// scans the renderer used to run, header by header.
func TestRemoteHeaderCounts_MatchScanHelpers(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "1", Group: "", Status: "running"},
		{ID: "2", Group: "work", Status: "waiting"},
		{ID: "3", Group: "work/api", Status: "running"},
		{ID: "4", Group: "work/api", Status: "running", Archived: true},
		{ID: "5", Group: "work/web/v2", Status: "stopped"},
		{ID: "6", Group: "/play/", Status: "waiting"},
	}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, []string{"work", "idle"}, true)
	got := remoteHeaderCounts("box", sessions)
	headers := 0
	for _, it := range items {
		if it.Type != session.ItemTypeRemoteGroup {
			continue
		}
		headers++
		want := remoteHeaderCount{total: len(sessions)}
		groupPath := ""
		if it.Level > 0 {
			groupPath = strings.TrimPrefix(it.Path, "remotes/box/")
			want.total = remoteSubGroupCount(sessions, groupPath)
		}
		want.running, want.waiting = remoteStatusCounts(sessions, groupPath)
		if got[it.Path] != want {
			t.Errorf("%s: counts %+v, scan helpers say %+v", it.Path, got[it.Path], want)
		}
	}
	if headers < 7 {
		t.Fatalf("expected the host header, work, work/api, work/web, work/web/v2, play, idle and my-sessions; got %d headers", headers)
	}
}

// Finding 11: the rendered header rows are byte-identical whether the counts
// come from the rows' side map or from the renderer's fallback scan.
func TestRemoteHeaderCounts_RenderIdenticalToScan(t *testing.T) {
	home := newTestHomeWithItems(120, 40, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {
			remoteInfo("box", "1", "a", "running"),
			{ID: "2", Title: "b", Group: "work", Status: "waiting", RemoteName: "box"},
			{ID: "3", Title: "c", Group: "work/api", Status: "running", RemoteName: "box"},
			{ID: "4", Title: "d", Group: "work", Status: "running", Archived: true, RemoteName: "box"},
		},
	}
	home.remoteGroups = map[string][]string{"box": {"work", "empty"}}
	home.rebuildFlatItems()
	render := func() string {
		var b strings.Builder
		for _, it := range home.flatItems {
			if it.Type == session.ItemTypeRemoteGroup {
				home.renderRemoteGroupItem(&b, it, false)
			}
		}
		return b.String()
	}
	if len(home.remoteHeaderCounts) == 0 {
		t.Fatal("rebuildFlatItems must compute the header counts")
	}
	for _, it := range home.flatItems {
		if it.Type == session.ItemTypeRemoteGroup {
			if _, ok := home.remoteHeaderCounts[it.Path]; !ok {
				t.Errorf("header %s has no precomputed counts", it.Path)
			}
		}
	}
	withMap := render()
	home.remoteHeaderCounts = nil
	withScan := render()
	if withMap != withScan {
		t.Fatalf("rendering differs:\nmap:\n%s\nscan:\n%s", withMap, withScan)
	}
	if !strings.Contains(withMap, "(3)") || !strings.Contains(withMap, "empty") {
		t.Fatalf("expected the host header count and the empty group; got:\n%s", withMap)
	}
}

// Finding 11: the snapshot is written at most once per interval, only when
// the sessions changed, and always on demand (quit).
func TestRemoteSessionsCache_SaveIsDebounced(t *testing.T) {
	remoteSessionsCacheEnabled = true
	t.Cleanup(func() { remoteSessionsCacheEnabled = false })

	home := NewHome()
	if home.storage == nil || home.storage.GetDB() == nil {
		t.Skip("no storage backend in this environment")
	}
	db := home.storage.GetDB()
	t.Cleanup(func() { _ = db.SetMeta(remoteSessionsCacheKey, "") })
	_ = db.SetMeta(remoteSessionsCacheKey, "")

	meta := func() string {
		val, _ := db.GetMeta(remoteSessionsCacheKey)
		return val
	}
	set := func(title string) map[string][]session.RemoteSessionInfo {
		live := map[string][]session.RemoteSessionInfo{"box": {remoteInfo("box", "r1", title, "running")}}
		home.remoteSessionsMu.Lock()
		home.remoteSessions = live
		home.remoteSessionsMu.Unlock()
		return live
	}

	home.saveRemoteSessionsCache(set("first"))
	if !strings.Contains(meta(), "first") {
		t.Fatal("the first save must write at once")
	}
	home.saveRemoteSessionsCache(set("second"))
	if strings.Contains(meta(), "second") {
		t.Fatal("a save inside the interval must be held back")
	}
	home.flushRemoteSessionsCache(false)
	if strings.Contains(meta(), "second") {
		t.Fatal("a tick flush inside the interval must be held back too")
	}
	home.remoteCacheLastSave = time.Now().Add(-remoteSessionsCacheSaveInterval)
	home.flushRemoteSessionsCache(false)
	if !strings.Contains(meta(), "second") {
		t.Fatal("once the interval passed the held snapshot must land")
	}

	// Same sessions again after the interval: nothing to write.
	_ = db.SetMeta(remoteSessionsCacheKey, "")
	home.remoteCacheLastSave = time.Now().Add(-remoteSessionsCacheSaveInterval)
	home.saveRemoteSessionsCache(set("second"))
	if meta() != "" {
		t.Fatal("an unchanged fleet must not be rewritten")
	}

	// Quit writes what is pending without waiting.
	home.saveRemoteSessionsCache(set("third"))
	home.flushRemoteSessionsCache(true)
	if !strings.Contains(meta(), "third") {
		t.Fatal("a forced flush must write the pending snapshot")
	}

	// And what it wrote loads back through the startup path.
	reader := NewHome()
	if got := reader.remoteSessions["box"]; len(got) != 1 || got[0].Title != "third" || got[0].RemoteName != "box" {
		t.Fatalf("the debounced snapshot must round-trip; got %+v", reader.remoteSessions)
	}
}

// Finding 12: a remote known with zero sessions keeps its entry through a
// result that did not cover it.
func TestMergeRemoteSessions_KeepsKnownEmptyRemote(t *testing.T) {
	prev := map[string][]session.RemoteSessionInfo{"fresh": {}, "nilled": nil}
	got := mergeRemoteSessions(prev, map[string][]session.RemoteSessionInfo{"other": {}}, map[string]bool{"fresh": true, "nilled": true})
	if _, ok := got["fresh"]; !ok {
		t.Fatal("a failed remote that existed with zero sessions must be kept")
	}
	if _, ok := got["nilled"]; !ok {
		t.Fatal("a failed remote that existed with a nil slice must be kept")
	}
	if _, ok := mergeRemoteSessions(nil, nil, map[string]bool{"unknown": true})["unknown"]; ok {
		t.Fatal("a failed remote never seen before must not appear")
	}
}

// Finding 12: a push from one remote leaves an empty remote's host header
// and folders in place.
func TestRemotePush_EmptyRemoteKeepsHeaderAndGroups(t *testing.T) {
	home := newTestHomeWithItems(120, 40, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"busy":  {remoteInfo("busy", "b1", "b", "running")},
		"empty": {},
	}
	home.remoteGroups = map[string][]string{"empty": {"inbox"}, "groupsonly": {"todo"}}
	home.remoteCosts = map[string]*costs.RemoteCostSummary{"costsonly": {CostTodayMicrodollars: 1}}

	msg := home.pushedRemoteFetch(session.RemoteChange{Remote: "busy", HasData: true})
	for _, name := range []string{"empty", "groupsonly", "costsonly"} {
		if !msg.failed[name] || !msg.groupsFailed[name] {
			t.Fatalf("a push must mark every other known remote failed; %s missing in %v / %v", name, msg.failed, msg.groupsFailed)
		}
	}

	model, _ := home.Update(pushWithData("busy", []session.RemoteSessionInfo{{ID: "b1", Title: "b-pushed"}}, nil))
	h := model.(*Home)
	if _, ok := h.remoteSessions["empty"]; !ok {
		t.Fatal("an empty remote must survive a foreign push")
	}
	if got := h.remoteGroups["empty"]; len(got) != 1 || got[0] != "inbox" {
		t.Fatalf("an empty remote's group list must survive a foreign push; got %v", got)
	}
	if got := h.remoteGroups["groupsonly"]; len(got) != 1 || got[0] != "todo" {
		t.Fatalf("a remote known only by its group list must keep it; got %v", got)
	}
	if h.remoteCosts["costsonly"] == nil {
		t.Fatal("a remote known only by its cost figure must keep it")
	}
	header, inbox := false, false
	for _, it := range h.flatItems {
		if it.Type != session.ItemTypeRemoteGroup {
			continue
		}
		if it.Path == "remotes/empty" {
			header = true
		}
		if it.Path == "remotes/empty/inbox" {
			inbox = true
		}
	}
	if !header || !inbox {
		t.Fatalf("the empty remote's host header and its folder must still render; header=%v inbox=%v", header, inbox)
	}
}
