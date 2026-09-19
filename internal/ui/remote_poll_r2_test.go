package ui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

type retryPollRunner struct {
	stubFetchRunner
	calls *atomic.Int32
}

func (r retryPollRunner) FetchSessions(context.Context) ([]session.RemoteSessionInfo, error) {
	r.calls.Add(1)
	return nil, nil
}

func TestRemotePollHeaderRetryEvent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rc := session.RemoteConfig{Host: "test@invalid"}
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"dev": rc, "other": {Host: "other@invalid"}}}); err != nil {
		t.Fatal(err)
	}
	session.RecordRemotePoll("dev", rc, time.Millisecond, errors.New("Permission denied (publickey)"))
	h := newTestHomeWithItems(120, 30, nil)
	defer h.cancel()
	other := session.RemoteConfig{Host: "other@invalid"}
	session.RecordRemotePoll("other", other, time.Millisecond, errors.New("Permission denied (publickey)"))
	h.seedRemotePolls(map[string]session.RemoteConfig{"dev": rc, "other": other})
	h.remoteSessions["other"] = []session.RemoteSessionInfo{{ID: "keep", Title: "Other work", RemoteName: "other", Group: "work"}}
	h.remoteGroups = map[string][]string{"other": {"work"}}
	h.flatItems = []session.Item{{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev"}}
	var calls atomic.Int32
	h.newRemoteFetchRunner = func(string, session.RemoteConfig) remoteFetchRunner { return retryPollRunner{calls: &calls} }
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil {
		t.Fatal("R on remote header did not schedule retry")
	}
	// Execute the reset and the event path that schedules the fresh poll.
	msg := cmd()
	_, next := h.Update(msg)
	if next == nil {
		t.Fatal("retry did not schedule a poll")
	}
	var run func(tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				run(child)
			}
			return
		}
		_, next := h.Update(msg)
		// Fetch result commands include unrelated timers. The observation is enough.
		if _, ok := msg.(remoteSessionsFetchedMsg); !ok {
			run(next)
		}
	}
	run(next)
	if calls.Load() != 1 {
		t.Fatalf("retry calls = %d, want 1", calls.Load())
	}
	if len(h.remoteSessions["other"]) != 1 || h.remoteSessions["other"][0].ID != "keep" || len(h.remoteGroups["other"]) != 1 {
		t.Fatal("retry discarded another host's rows/groups")
	}
	if session.LoadRemotePolls()["other"].LastPollStatus != "auth_failed" {
		t.Fatal("retry reset another host's authentication pause")
	}
	if state := session.LoadRemotePolls()["dev"]; state.LastPollStatus != "ok" {
		t.Fatalf("retry did not persist recovery: %+v", state)
	}
}
