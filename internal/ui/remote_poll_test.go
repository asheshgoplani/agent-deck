package ui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type authPollRunner struct {
	stubFetchRunner
	calls *atomic.Int32
}

func (r authPollRunner) FetchSessions(context.Context) ([]session.RemoteSessionInfo, error) {
	r.calls.Add(1)
	return nil, errors.New("ssh: Permission denied (publickey).")
}

func TestRemotePollAuthBackoffAndReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	var calls atomic.Int32
	h.newRemoteFetchRunner = func(string, session.RemoteConfig) remoteFetchRunner { return authPollRunner{calls: &calls} }
	rc := session.RemoteConfig{Host: "test@invalid"}
	for gen := uint64(1); gen <= 3; gen++ {
		cmds := h.remoteFetchCmds(gen, map[string]session.RemoteConfig{"dev": rc})
		h.Update(remoteFetchRoundMsg{gen: gen, fetches: cmds})
		for _, cmd := range cmds {
			h.Update(cmd())
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("auth failure retried %d times; want one attempt until user retries", got)
	}
	var row strings.Builder
	h.renderRemoteGroupItem(&row, session.Item{Type: session.ItemTypeRemoteGroup, Path: "remotes/dev", RemoteName: "dev"}, false, 0)
	if !strings.Contains(stripAnsi(row.String()), "auth failed") {
		t.Errorf("missing auth failure reason: %s", row.String())
	}
	found := false
	for _, item := range h.flatItems {
		if item.RemoteName == "dev" {
			found = true
		}
	}
	if !found {
		t.Error("a never-reachable configured remote must retain a visible header")
	}
}

func TestRemotePollOverlappingRoundsDoNotOverlapSSH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	h.newRemoteFetchRunner = func(name string, rc session.RemoteConfig) remoteFetchRunner {
		calls.Add(1)
		started <- struct{}{}
		return stubFetchRunner{name: name, release: release}
	}
	remotes := map[string]session.RemoteConfig{"dev": {Host: "test@invalid"}}
	first := h.remoteFetchCmds(1, remotes)
	done := runFetchCmds(first)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first fetch never started")
	}
	second := h.remoteFetchCmds(2, remotes)
	secondDone := runFetchCmds(second)
	if len(second) > 0 {
		select {
		case <-secondDone:
		case <-time.After(200 * time.Millisecond):
			t.Error("overlapping round blocked on the same SSH host")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("concurrent SSH polls = %d, want 1", got)
	}
	h.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll did not honor shutdown cancellation")
	}
}

func TestRemotePollRetryAndRepoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	var calls atomic.Int32
	h.newRemoteFetchRunner = func(string, session.RemoteConfig) remoteFetchRunner { return authPollRunner{calls: &calls} }
	rc := session.RemoteConfig{Host: "test@invalid"}
	run := func(gen uint64) {
		cmds := h.remoteFetchCmds(gen, map[string]session.RemoteConfig{"dev": rc})
		h.Update(remoteFetchRoundMsg{gen: gen, fetches: cmds})
		for _, cmd := range cmds {
			h.Update(cmd())
		}
	}
	run(1)
	if err := session.ResetRemotePoll("dev"); err != nil {
		t.Fatal(err)
	}
	run(2)
	if calls.Load() != 2 {
		t.Fatalf("explicit retry did not release auth pause: %d", calls.Load())
	}
	rc.Host = "other@invalid"
	run(3)
	if calls.Load() != 3 {
		t.Fatalf("new identity inherited auth pause: %d", calls.Load())
	}
}

type stuckPollRunner struct {
	stubFetchRunner
	started, release chan struct{}
}

func (r stuckPollRunner) FetchSessions(context.Context) ([]session.RemoteSessionInfo, error) {
	close(r.started)
	<-r.release
	return nil, context.Canceled
}

func TestRemotePollStartAndExitDoNotWaitForSlowRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"dev": {Host: "test@invalid"}}}); err != nil {
		t.Fatal(err)
	}
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	h.sysStatsCollector = nil
	h.intervalHookRunner = nil
	local := &session.Instance{ID: "local", Title: "Local work", Status: session.StatusIdle}
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: local}}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h.newRemoteFetchRunner = func(string, session.RemoteConfig) remoteFetchRunner {
		return stuckPollRunner{started: started, release: release}
	}
	start := time.Now()
	if cmd := h.Init(); cmd == nil {
		t.Fatal("Init did not schedule background commands")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Init blocked")
	}
	cmds := h.remoteFetchCmds(1, map[string]session.RemoteConfig{"dev": {Host: "test@invalid"}})
	_ = runFetchCmds(cmds)
	<-started
	start = time.Now()
	if frame := stripAnsi(h.View()); !strings.Contains(frame, "Local work") {
		t.Fatalf("local session missing during remote poll: %s", frame)
	}
	if time.Since(start) > time.Second {
		t.Fatal("render waited for remote poll")
	}
	done := make(chan struct{})
	go func() { h.performFinalShutdown(false)(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for in-flight remote poll")
	}
}

func TestRemotePollDropsOldIdentityCompletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	old := session.RemoteConfig{Host: "old@invalid"}
	h.seedRemotePolls(map[string]session.RemoteConfig{"dev": old})
	if !h.beginRemotePoll("dev", old) {
		t.Fatal("initial poll not reserved")
	}
	next := session.RemoteConfig{Host: "new@invalid"}
	h.seedRemotePolls(map[string]session.RemoteConfig{"dev": next})
	h.finishRemotePoll("dev", old, time.Now(), errors.New("Permission denied (publickey)"))
	if _, found := session.LoadRemotePolls()["dev"]; found {
		t.Fatal("old identity completion persisted")
	}
	h.Update(remoteSessionsFetchedMsg{pollName: "dev", pollConfig: old, sessions: map[string][]session.RemoteSessionInfo{"dev": {{ID: "old", Title: "Wrong host"}}}})
	if len(h.remoteSessions["dev"]) != 0 {
		t.Fatal("old identity rows applied to new host")
	}
	if !h.beginRemotePoll("dev", next) {
		t.Fatal("new identity inherited old poll lock")
	}
	h.cancel()
	h.finishRemotePoll("dev", next, time.Now(), context.Canceled)
}

func TestRemotePollPersistedAuthBlocksStartupAuxiliaryProbes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rc := session.RemoteConfig{Host: "test@invalid"}
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"dev": rc}}); err != nil {
		t.Fatal(err)
	}
	session.RecordRemotePoll("dev", rc, time.Millisecond, errors.New("Permission denied (publickey)"))
	h := newTestHomeWithItems(100, 30, nil)
	defer h.cancel()
	t.Setenv("PATH", t.TempDir())
	if !h.remoteAuthBlocked("dev", rc) {
		t.Fatal("auth pause was not available before first poll")
	}
	if msg := h.measureRemoteLatencies().(remoteLatenciesFetchedMsg); len(msg.latencies) != 0 {
		t.Fatal("latency probe bypassed auth pause")
	}
	msg := h.fetchRemotePreview("dev", "session", "key")().(previewFetchedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "auth failed") {
		t.Fatalf("preview bypassed auth pause: %v", msg.err)
	}
}
