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
			Name: "default", Known: true, FiveHour: session.AccountUsageWindow{Known: true, Percent: 42},
		}}},
		FetchedAt: time.Now(),
	}}
	h.remoteSessionsMu.Unlock()

	out := h.renderRemotePreview(remoteContextTestItem(), 100, 30)
	if !strings.Contains(out, "account") || !strings.Contains(out, "5h 42%") {
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

func TestRemoteAccountsLine_MultiAccountWorkVsDefaultWithAge(t *testing.T) {
	h := NewHome()
	h.remoteHostStats = remoteAccountsStats(time.Now().Add(-2*time.Minute), "default", "work")
	work := h.remoteAccountsLine("box", "work", time.Now())
	if !strings.Contains(work, "work") || !strings.Contains(work, "5h 20%") || strings.Contains(work, "5h 10%") {
		t.Fatalf("work session must show work's usage only: %q", work)
	}
	def := h.remoteAccountsLine("box", "", time.Now())
	if !strings.Contains(def, "default") || !strings.Contains(def, "5h 10%") || strings.Contains(def, "5h 20%") {
		t.Fatalf("empty account must show default's usage only: %q", def)
	}
	if !strings.Contains(work, "polled") || !strings.Contains(work, "2m ago") {
		t.Fatalf("poll age missing: %q", work)
	}
}

func TestRemoteAccountsLine_AvailableButEmptyAccounts(t *testing.T) {
	h := NewHome()
	h.remoteHostStats = remoteAccountsStats(time.Now())
	want := `account unknown (slot "default" not reported by remote)`
	if got := h.remoteAccountsLine("box", "", time.Now()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemoteAccountsLine_NamedSlotAbsentFromNonEmptyList(t *testing.T) {
	h := NewHome()
	h.remoteHostStats = remoteAccountsStats(time.Now(), "default", "other")
	want := `account unknown (slot "work" not reported by remote)`
	if got := h.remoteAccountsLine("box", "work", time.Now()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemoteAccountsLine_UnpolledHostExactLine(t *testing.T) {
	h := NewHome()
	want := remoteStatsUnknownLine(session.RemoteVersionState{}, Version)
	if got := h.remoteAccountsLine("box", "work", time.Now()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func remoteAccountsStats(fetched time.Time, names ...string) map[string]remoteHostStatsResult {
	accts := make([]session.AccountUsage, 0, len(names))
	for i, n := range names {
		accts = append(accts, session.AccountUsage{Name: n, Known: true, FiveHour: session.AccountUsageWindow{Known: true, Percent: float64(10 * (i + 1))}})
	}
	return map[string]remoteHostStatsResult{"box": {Stats: session.RemoteHostStats{AccountsAvailable: true, Accounts: accts}, FetchedAt: fetched}}
}
