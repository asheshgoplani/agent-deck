package ui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// fakeTriggersFeed counts reloads so the tests can tell a cheap rev check
// from a full load.
type fakeTriggersFeed struct {
	rev      string
	revErr   error
	views    []triggers.View
	loads    int
	sessions []triggers.Resolved
}

func (f *fakeTriggersFeed) Rev() (string, error) { return f.rev, f.revErr }

func (f *fakeTriggersFeed) Load(_ string, sessions []triggers.Resolved) ([]triggers.View, error) {
	f.loads++
	f.sessions = sessions
	return f.views, nil
}

// runTriggersPoll runs one poll the way the runtime would: execute the
// command, feed its message back into Update, and return the follow-up.
func runTriggersPoll(t *testing.T, h *Home) (*Home, tea.Cmd) {
	t.Helper()
	cmd := h.triggersPollCmd()
	if cmd == nil {
		t.Fatal("triggersPollCmd returned nil with a feed configured")
	}
	msg := cmd()
	polled, ok := msg.(triggersPolledMsg)
	if !ok {
		t.Fatalf("poll produced %T, want triggersPolledMsg", msg)
	}
	m, next := h.Update(polled)
	return m.(*Home), next
}

func TestTriggersLive_ReloadsOnlyWhenRevChanges(t *testing.T) {
	views := triggersFixtureViews(t)
	feed := &fakeTriggersFeed{rev: "100", views: views}
	h := newSeamATestHome()
	h.triggersFeed = feed

	h, next := runTriggersPoll(t, h)
	if feed.loads != 1 {
		t.Fatalf("first poll loaded %d times, want 1", feed.loads)
	}
	if next == nil {
		t.Error("a poll must schedule the next tick")
	}
	if got := h.triggerBadges[fixtureConductorID]; got.Count != 2 || !got.Bad {
		t.Errorf("badge after load = %+v, want 2 triggers, failing", got)
	}

	// Same rev, same sessions: only the meta row is read.
	h, _ = runTriggersPoll(t, h)
	h, _ = runTriggersPoll(t, h)
	if feed.loads != 1 {
		t.Fatalf("unchanged rev reloaded: %d loads, want 1", feed.loads)
	}

	// A write elsewhere (a fire, a scan) bumps the rev: reload.
	feed.rev = "101"
	feed.views = views[:1]
	h, _ = runTriggersPoll(t, h)
	if feed.loads != 2 {
		t.Fatalf("changed rev did not reload: %d loads, want 2", feed.loads)
	}
	if len(h.triggerViews) != 1 {
		t.Errorf("cached views = %d after reload, want 1", len(h.triggerViews))
	}

	// A failing rev read keeps what was loaded.
	feed.revErr = errors.New("database is locked")
	h, next = runTriggersPoll(t, h)
	if feed.loads != 2 || len(h.triggerViews) != 1 {
		t.Errorf("rev error: loads=%d views=%d, want the cache kept", feed.loads, len(h.triggerViews))
	}
	if next == nil {
		t.Error("a failed poll must still schedule the next tick")
	}
}

func TestTriggersLive_ReloadsWhenSessionsChange(t *testing.T) {
	feed := &fakeTriggersFeed{rev: "7"}
	h := newSeamATestHome()
	h.triggersFeed = feed
	inst := session.NewInstanceWithTool("inbox-triage", "/tmp/i", "shell")
	h.instances = []*session.Instance{inst}

	h, _ = runTriggersPoll(t, h)
	if feed.loads != 1 || len(feed.sessions) != 1 || feed.sessions[0].Title != "inbox-triage" {
		t.Fatalf("first load: loads=%d sessions=%+v", feed.loads, feed.sessions)
	}
	// A rename can turn a target dead (or alive) without any trigger write.
	inst.Title = "inbox-triage-2"
	_, _ = runTriggersPoll(t, h)
	if feed.loads != 2 {
		t.Fatalf("a renamed session did not re-resolve targets: %d loads, want 2", feed.loads)
	}
}

func TestTriggersLive_PanelVisiblePollsFaster(t *testing.T) {
	if triggersPollInterval(true) >= triggersPollInterval(false) {
		t.Fatalf("visible interval %v is not shorter than hidden %v", triggersPollInterval(true), triggersPollInterval(false))
	}
	if triggersPollInterval(true) > 2*time.Second {
		t.Errorf("visible interval %v, want at most 2s", triggersPollInterval(true))
	}
}

func TestTriggersLive_StaleTickIsDropped(t *testing.T) {
	feed := &fakeTriggersFeed{rev: "1"}
	h := newSeamATestHome()
	h.triggersFeed = feed
	h.triggersPollGen = 3
	// A tick from a loop that opening the panel replaced must not start a
	// second poll loop.
	m, cmd := h.Update(triggersTickMsg{gen: 2})
	h = m.(*Home)
	if cmd != nil {
		t.Error("a stale tick scheduled work")
	}
	m, cmd = h.Update(triggersTickMsg{gen: 3})
	if cmd == nil {
		t.Error("the current tick did not poll")
	}
	_ = m
}

func TestTriggersLive_PanelGetsReloadedViews(t *testing.T) {
	views := triggersFixtureViews(t)
	feed := &fakeTriggersFeed{rev: "1", views: views[:2]}
	h := newSeamATestHome()
	h.triggersFeed = feed
	h, _ = runTriggersPoll(t, h)
	h = pressHome(t, h, "alt+t")
	before := len(h.triggersPanel.views)
	feed.rev, feed.views = "2", views
	h, _ = runTriggersPoll(t, h)
	if before != 2 || len(h.triggersPanel.views) != len(views) {
		t.Errorf("panel views %d -> %d, want 2 -> %d without a keypress", before, len(h.triggersPanel.views), len(views))
	}
}
