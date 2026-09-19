package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// Use the persisted wire format so these behavioral regressions also compile
// on the pre-poll-state base. NewHome must consume the same files as a restart.
func baselineRemotePollFixture(t *testing.T, status string) string {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	rc := session.RemoteConfig{Host: "test@invalid"}
	if err := session.SaveUserConfig(&session.UserConfig{Remotes: map[string]session.RemoteConfig{"dev": rc}}); err != nil {
		t.Fatal(err)
	}
	path, err := agentpaths.CachePath("remote-versions.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"polls": map[string]any{"dev": map[string]any{
		"host": rc.Host, "profile": rc.GetProfile(), "agent_deck_path": rc.GetAgentDeckPath(),
		"last_poll_ms": 13570, "last_poll_status": status, "checked_at": time.Now(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baselinePersistedPolls(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cache struct {
		Polls map[string]json.RawMessage `json:"polls"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	return cache.Polls
}

func TestRemotePollBaselineRestartPersistedSuccess(t *testing.T) {
	path := baselineRemotePollFixture(t, "ok")
	before := string(baselinePersistedPolls(t, path)["dev"])
	wasEnabled := remoteSessionsCacheEnabled
	remoteSessionsCacheEnabled = true
	t.Cleanup(func() { remoteSessionsCacheEnabled = wasEnabled })
	writer := NewHome()
	defer writer.cancel()
	if writer.storage == nil || writer.storage.GetDB() == nil {
		t.Fatal("storage required for real startup regression")
	}
	t.Cleanup(func() { _ = writer.storage.GetDB().SetMeta(remoteSessionsCacheKey, "") })
	live := map[string][]session.RemoteSessionInfo{"dev": {{ID: "r1", Title: "Restored work", Status: "running", RemoteName: "dev"}}}
	writer.remoteSessions = live
	writer.saveRemoteSessionsCache(live)

	reader := NewHome()
	defer reader.cancel()
	reader.width, reader.height = 120, 30
	if len(reader.remoteSessions["dev"]) != 1 || !reader.remoteFromCache["dev"] {
		t.Fatal("startup did not restore cached session")
	}
	if after := string(baselinePersistedPolls(t, path)["dev"]); after != before {
		t.Errorf("startup changed the historical poll observation: %s", after)
	}
	var frame strings.Builder
	rs := reader.remoteSessions["dev"][0]
	reader.renderRemoteGroupItem(&frame, session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev"}, false, 0)
	reader.renderRemoteSessionItem(&frame, session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "dev", RemoteSession: &rs}, false)
	got := stripAnsi(frame.String())
	if strings.Contains(got, "●") || !strings.Contains(got, "last known") {
		t.Errorf("restored success claims live state: %s", got)
	}
	if running, waiting, idle, stopped, errored := reader.countSessionStatuses(); running+waiting+idle+stopped+errored != 0 {
		t.Errorf("cached row contributes live counts: running=%d waiting=%d idle=%d stopped=%d errored=%d", running, waiting, idle, stopped, errored)
	}
	reader.Update(remoteSessionsFetchedMsg{sessions: live})
	if reader.remoteFromCache["dev"] {
		t.Fatal("fresh fetch retained cache flag")
	}
	if running, _, _, _, _ := reader.countSessionStatuses(); running != 1 {
		t.Errorf("fresh success count = %d", running)
	}

}

func TestRemotePollBaselineHeaderDuration(t *testing.T) {
	baselineRemotePollFixture(t, "ok")
	h := newTestHomeWithItems(120, 30, nil)
	defer h.cancel()
	h.remoteLatency = map[string]session.RemoteLatency{"dev": {MS: 97, MeasuredAt: time.Now()}}
	var frame strings.Builder
	h.renderRemoteGroupItem(&frame, session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev"}, true, 0)
	got := stripAnsi(frame.String())
	if !strings.Contains(got, "poll 13570ms") || !strings.Contains(got, "network 97ms") || !strings.Contains(got, "R retry") {
		t.Fatalf("missing distinct durations/retry from persisted poll: %s", got)
	}
}

func TestRemotePollBaselineHeaderRetryEvent(t *testing.T) {
	path := baselineRemotePollFixture(t, "auth_failed")
	h := newTestHomeWithItems(120, 30, []session.Item{{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev"}})
	defer h.cancel()
	_, cmd := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if cmd == nil {
		t.Fatal("R on selected remote header did not schedule retry")
	}
	// Execute only the local reset. The returned command must schedule a poll,
	// but this baseline-compatible test never invokes a real SSH transport.
	msg := cmd()
	if _, exists := baselinePersistedPolls(t, path)["dev"]; exists {
		t.Fatal("retry did not clear persisted authentication pause")
	}
	_, next := h.Update(msg)
	if next == nil {
		t.Fatal("retry reset did not schedule a fresh poll")
	}
}
