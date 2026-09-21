package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteContextTestItem() session.Item {
	rs := session.RemoteSessionInfo{ID: "r1", Title: "s", Status: "running", Tool: "claude", RemoteName: "box"}
	return session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "box", RemoteSession: &rs}
}

func TestRemoteSessionPreview_ShowsAccountUsageFromCachedStats(t *testing.T) {
	h := NewHome()
	h.width, h.height = 100, 30
	h.remoteSessionsMu.Lock()
	h.remoteHostStats = map[string]remoteHostStatsResult{"box": {
		Stats: session.RemoteHostStats{AccountsAvailable: true, Accounts: []session.AccountUsage{{
			Name: "work", Known: true, FiveHour: session.AccountUsageWindow{Known: true, Percent: 42},
		}}},
		FetchedAt: time.Now(),
	}}
	h.remoteSessionsMu.Unlock()

	out := h.renderRemotePreview(remoteContextTestItem(), 100, 30)
	if !strings.Contains(out, "accounts") || !strings.Contains(out, "5h 42%") {
		t.Fatalf("remote session preview missing account usage line:\n%s", out)
	}
}

func TestRemoteSessionPreview_OlderRemoteWithoutAccountsDegradesOneLine(t *testing.T) {
	h := NewHome()
	h.remoteSessionsMu.Lock()
	h.remoteHostStats = map[string]remoteHostStatsResult{"box": {FetchedAt: time.Now()}}
	h.remoteSessionsMu.Unlock()

	out := h.renderRemotePreview(remoteContextTestItem(), 100, 30)
	if strings.Count(out, "accounts unknown (remote does not report accounts)") != 1 {
		t.Fatalf("expected one degrade line:\n%s", out)
	}
}

func TestContextInspector_RemoteRowShowsNoticeNotSilence(t *testing.T) {
	h := NewHome()
	h.width, h.height = 100, 30
	item := remoteContextTestItem()
	h.flatItems = []session.Item{item}
	h.cursor = 0

	model, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(defaultHotkeyBindings[hotkeyContextInspector])})
	got := model.(*Home)
	if cmd != nil {
		t.Fatalf("remote row must not start an inspection")
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "not available for remote sessions yet") {
		t.Fatalf("expected remote notice, got err=%v", got.err)
	}
}
