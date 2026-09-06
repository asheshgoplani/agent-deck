// Per-remote delivery of fleet polls (#2177 follow-up): each remote's
// answer lands on its own, so a slow or wedged host cannot hold back the
// others; the stale-result guard, the in-flight guard and the cost merge
// all work per remote.

package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// stubFetchRunner answers FetchSessions once release is closed (or at once
// when release is nil).
type stubFetchRunner struct {
	name     string
	sessions []session.RemoteSessionInfo
	release  chan struct{}
}

func (s stubFetchRunner) FetchSessions(ctx context.Context) ([]session.RemoteSessionInfo, error) {
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]session.RemoteSessionInfo(nil), s.sessions...), nil
}

func (s stubFetchRunner) FetchCostSummary(context.Context) (*costs.RemoteCostSummary, error) {
	return &costs.RemoteCostSummary{}, nil
}

func (s stubFetchRunner) FetchGroupPaths(context.Context) ([]string, error) {
	return []string{s.name}, nil
}

func runFetchCmds(cmds []tea.Cmd) <-chan remoteSessionsFetchedMsg {
	out := make(chan remoteSessionsFetchedMsg, len(cmds))
	for _, cmd := range cmds {
		go func(cmd tea.Cmd) { out <- cmd().(remoteSessionsFetchedMsg) }(cmd)
	}
	return out
}

func TestRemoteFetch_SlowRemoteDoesNotDelayFastOne(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"slow": {{ID: "s-old", Title: "slow-cached", RemoteName: "slow"}},
	}
	release := make(chan struct{})
	home.newRemoteFetchRunner = func(name string, rc session.RemoteConfig) remoteFetchRunner {
		if name == "slow" {
			return stubFetchRunner{name: name, release: release, sessions: []session.RemoteSessionInfo{{ID: "s-new", Title: "slow-live"}}}
		}
		return stubFetchRunner{name: name, sessions: []session.RemoteSessionInfo{{ID: "f1", Title: "fast-live"}}}
	}
	remotes := map[string]session.RemoteConfig{"fast": {Host: "fast@x"}, "slow": {Host: "slow@x"}}

	cmds := home.remoteFetchCmds(3, remotes)
	model, _ := home.Update(remoteFetchRoundMsg{gen: 3, fetches: cmds})
	h := model.(*Home)
	if !h.remotesFetchActive {
		t.Fatal("starting a round must raise the in-flight guard")
	}

	results := runFetchCmds(cmds)
	var first remoteSessionsFetchedMsg
	select {
	case first = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("the fast remote's result must arrive while the slow remote is still blocked")
	}
	if _, ok := first.sessions["fast"]; !ok {
		t.Fatalf("first result must be the fast remote's; got sessions=%v failed=%v", first.sessions, first.failed)
	}
	if !first.failed["slow"] {
		t.Fatal("a per-remote result must mark the other remotes failed so the merge keeps their rows")
	}

	model, _ = h.Update(first)
	h = model.(*Home)
	if got := remoteTitles(h, "fast"); len(got) != 1 || got[0] != "fast-live" {
		t.Fatalf("fast remote must render before the slow one answers; got %v", got)
	}
	if got := remoteTitles(h, "slow"); len(got) != 1 || got[0] != "slow-cached" {
		t.Fatalf("slow remote must keep its cached rows meanwhile; got %v", got)
	}
	if !h.remotesFetchActive {
		t.Fatal("the round is not over while the slow remote is outstanding")
	}
	if got := h.remoteGroups["fast"]; len(got) != 1 || got[0] != "fast" {
		t.Fatalf("fast remote's group list must land with its sessions; got %v", got)
	}

	close(release)
	var second remoteSessionsFetchedMsg
	select {
	case second = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("the slow remote's result must arrive once released")
	}
	model, _ = h.Update(second)
	h = model.(*Home)
	if got := remoteTitles(h, "slow"); len(got) != 1 || got[0] != "slow-live" {
		t.Fatalf("slow remote must update when it finally answers; got %v", got)
	}
	if got := remoteTitles(h, "fast"); len(got) != 1 || got[0] != "fast-live" {
		t.Fatalf("the slow remote's result must not disturb the fast remote's rows; got %v", got)
	}
	if h.remotesFetchActive {
		t.Fatal("the last result of the round must release the in-flight guard")
	}
}

func TestRemoteFetch_StaleGuardIsPerRemote(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	perRemote := func(gen uint64, name, title string, others ...string) remoteSessionsFetchedMsg {
		msg := remoteSessionsFetchedMsg{
			gen:      gen,
			sessions: map[string][]session.RemoteSessionInfo{name: {{ID: name + "-1", Title: title}}},
			failed:   map[string]bool{},
		}
		for _, o := range others {
			msg.failed[o] = true
		}
		return msg
	}

	// A newer round's fast answer lands first...
	model, _ := home.Update(perRemote(6, "fast", "fast-6", "slow"))
	h := model.(*Home)
	// ...then the older round's slow answer: still the newest data for
	// "slow", so it must apply rather than be dropped as stale.
	model, _ = h.Update(perRemote(5, "slow", "slow-5", "fast"))
	h = model.(*Home)
	if got := remoteTitles(h, "slow"); len(got) != 1 || got[0] != "slow-5" {
		t.Fatalf("an older-round result for a remote with nothing newer applied must land; got %v", got)
	}
	// An older answer for "fast" itself is stale and must be ignored.
	model, _ = h.Update(perRemote(5, "fast", "fast-5", "slow"))
	h = model.(*Home)
	if got := remoteTitles(h, "fast"); len(got) != 1 || got[0] != "fast-6" {
		t.Fatalf("a result older than the last applied one for the same remote must be ignored; got %v", got)
	}
}

func TestRemoteFetch_CostsMergePerRemote(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteCosts = map[string]*costs.RemoteCostSummary{
		"a": {CostTodayMicrodollars: 1},
		"b": {CostTodayMicrodollars: 2},
	}
	rows := func(name string) map[string][]session.RemoteSessionInfo {
		return map[string][]session.RemoteSessionInfo{name: {{ID: name + "-1", Title: name}}}
	}

	// a answers with a fresh summary: b's figure must survive.
	model, _ := home.Update(remoteSessionsFetchedMsg{
		sessions: rows("a"),
		costs:    map[string]*costs.RemoteCostSummary{"a": {CostTodayMicrodollars: 10}},
		failed:   map[string]bool{"b": true},
	})
	h := model.(*Home)
	if h.remoteCosts["a"].CostTodayMicrodollars != 10 || h.remoteCosts["b"] == nil || h.remoteCosts["b"].CostTodayMicrodollars != 2 {
		t.Fatalf("a per-remote cost update must not blank other remotes; got %+v", h.remoteCosts)
	}

	// a answers but its cost fetch failed: a contributes zero, b untouched.
	model, _ = h.Update(remoteSessionsFetchedMsg{
		sessions: rows("a"),
		costs:    map[string]*costs.RemoteCostSummary{},
		failed:   map[string]bool{"b": true},
	})
	h = model.(*Home)
	if _, ok := h.remoteCosts["a"]; ok {
		t.Fatalf("a remote whose cost fetch failed must contribute zero; got %+v", h.remoteCosts)
	}
	if h.remoteCosts["b"] == nil {
		t.Fatalf("a remote still marked failed must keep its last-good figure; got %+v", h.remoteCosts)
	}

	// b is no longer configured (absent from both lists): its figure goes.
	model, _ = h.Update(remoteSessionsFetchedMsg{
		sessions: rows("a"),
		costs:    map[string]*costs.RemoteCostSummary{"a": {CostTodayMicrodollars: 11}},
		failed:   map[string]bool{},
	})
	h = model.(*Home)
	if _, ok := h.remoteCosts["b"]; ok {
		t.Fatalf("a deconfigured remote must drop out of the cost map; got %+v", h.remoteCosts)
	}
}

func TestRemoteFetch_ActiveClearsWhenLastResultLands(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	noop := func() tea.Msg { return nil }
	model, _ := home.Update(remoteFetchRoundMsg{gen: 1, fetches: []tea.Cmd{noop, noop}})
	h := model.(*Home)
	result := func(name string, other string) remoteSessionsFetchedMsg {
		return remoteSessionsFetchedMsg{
			gen:      1,
			inRound:  true,
			sessions: map[string][]session.RemoteSessionInfo{name: {}},
			failed:   map[string]bool{other: true},
		}
	}
	model, _ = h.Update(result("a", "b"))
	h = model.(*Home)
	if !h.remotesFetchActive {
		t.Fatal("one of two results must leave the round in flight")
	}
	// A config error from a refetch started outside this round must not
	// take the slow remote's slot: the round is still in flight.
	model, _ = h.Update(remoteSessionsFetchedMsg{gen: 2, configErr: errors.New("toml: bad key")})
	h = model.(*Home)
	if !h.remotesFetchActive {
		t.Fatal("a message from outside the round must not end it while a fetch is outstanding")
	}
	// A pushed change in between is not part of the round.
	pushed := result("a", "b")
	pushed.pushed = true
	pushed.inRound = false
	model, _ = h.Update(pushed)
	h = model.(*Home)
	if !h.remotesFetchActive {
		t.Fatal("a pushed change must not count as a round result")
	}
	model, _ = h.Update(result("b", "a"))
	h = model.(*Home)
	if h.remotesFetchActive {
		t.Fatal("the second of two results must end the round")
	}
}

func TestRemoteFetch_DeconfiguredRemoteDoesNotReturnFromOlderRound(t *testing.T) {
	home := newTestHomeWithItems(100, 30, nil)
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"gone": {{ID: "g-1", Title: "gone-cached", RemoteName: "gone"}},
	}
	// Round 6 was built from a config that no longer lists "gone": its
	// only result names "a" and marks nobody else failed, so "gone" drops.
	model, _ := home.Update(remoteSessionsFetchedMsg{
		gen:      6,
		inRound:  true,
		sessions: map[string][]session.RemoteSessionInfo{"a": {{ID: "a-1", Title: "a-6"}}},
		failed:   map[string]bool{},
	})
	h := model.(*Home)
	if got := remoteTitles(h, "gone"); len(got) != 0 {
		t.Fatalf("a remote absent from the config must drop; got %v", got)
	}
	// A slow result for "gone" from the older round 5 lands afterwards.
	model, _ = h.Update(remoteSessionsFetchedMsg{
		gen:      5,
		inRound:  true,
		sessions: map[string][]session.RemoteSessionInfo{"gone": {{ID: "g-2", Title: "gone-5"}}},
		failed:   map[string]bool{"a": true},
	})
	h = model.(*Home)
	if got := remoteTitles(h, "gone"); len(got) != 0 {
		t.Fatalf("an older round's result must not resurrect a deconfigured remote; got %v", got)
	}
	if got := remoteTitles(h, "a"); len(got) != 1 || got[0] != "a-6" {
		t.Fatalf("the newer round's rows must stay; got %v", got)
	}
}
