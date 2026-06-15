package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ccTestMenu builds a MenuSnapshot with a conductor session plus active and
// noise (error/stopped) children, used across the command-center tests.
func ccTestMenu() *MenuSnapshot {
	return &MenuSnapshot{
		Profile:       "personal",
		TotalSessions: 4,
		Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "cond-ad", Title: "conductor-agent-deck", IsConductor: true,
				Status: "running", GroupPath: "agent-deck", LatestPrompt: "v1.9.67 release wave",
			}},
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "child-1", Title: "fix-1431", Status: "running",
				GroupPath: "agent-deck", LatestPrompt: "investigating spawn race",
			}},
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "child-err", Title: "broken", Status: "error", GroupPath: "agent-deck",
			}},
			{Type: MenuItemTypeSession, Session: &MenuSession{
				ID: "child-stop", Title: "old", Status: "stopped", GroupPath: "agent-deck",
			}},
		},
	}
}

func TestCommandCenterStatusFiltersNoiseAndGroupsByConductor(t *testing.T) {
	snap := buildCommandCenterSnapshot(ccTestMenu(), "personal", "", nil)

	if len(snap.Conductors) != 1 {
		t.Fatalf("expected 1 conductor row, got %d: %+v", len(snap.Conductors), snap.Conductors)
	}
	cd := snap.Conductors[0]
	if cd.Name != "agent-deck" {
		t.Fatalf("expected conductor name agent-deck, got %q", cd.Name)
	}
	if cd.Target != "conductor-agent-deck" {
		t.Fatalf("expected target conductor-agent-deck, got %q", cd.Target)
	}
	if cd.Status != "running" {
		t.Fatalf("expected conductor status running, got %q", cd.Status)
	}
	if cd.CurrentlyWorkingOn != "v1.9.67 release wave" {
		t.Fatalf("expected currentlyWorkingOn from latest prompt, got %q", cd.CurrentlyWorkingOn)
	}
	// error + stopped children must be filtered OUT (the rejected noise).
	if len(cd.Sessions) != 1 {
		t.Fatalf("expected 1 active session (error/stopped filtered), got %d: %+v", len(cd.Sessions), cd.Sessions)
	}
	if cd.Sessions[0].ID != "child-1" {
		t.Fatalf("expected active child-1, got %q", cd.Sessions[0].ID)
	}
	for _, s := range cd.Sessions {
		if s.Status == "error" || s.Status == "stopped" {
			t.Fatalf("noise session leaked into output: %+v", s)
		}
	}
	if snap.Totals.Running != 1 {
		t.Fatalf("expected totals.running 1, got %d", snap.Totals.Running)
	}
	// maestro is always an allowed ask target; the conductor session adds itself.
	if !targetAllowed(snap.AskTargets, "maestro") {
		t.Fatalf("expected maestro in askTargets, got %v", snap.AskTargets)
	}
	if !targetAllowed(snap.AskTargets, "conductor-agent-deck") {
		t.Fatalf("expected conductor-agent-deck in askTargets, got %v", snap.AskTargets)
	}
}

func TestCommandCenterSurfacesHonestStatusSubstate(t *testing.T) {
	menu := ccTestMenu()
	menu.Items[1].Session.Substate = "model-unavailable"
	snap := buildCommandCenterSnapshot(menu, "personal", "", nil)
	if snap.Conductors[0].Sessions[0].Substate != "model-unavailable" {
		t.Fatalf("expected substate surfaced, got %q", snap.Conductors[0].Sessions[0].Substate)
	}
}

func TestCommandCenterDetectsCompletions(t *testing.T) {
	tracker := ccStatusTracker{}
	// First pass: child-1 running, no completions, tracker seeded.
	first := buildCommandCenterSnapshot(ccTestMenu(), "personal", "", tracker)
	if len(first.RecentlyCompleted) != 0 {
		t.Fatalf("expected no completions on first pass, got %+v", first.RecentlyCompleted)
	}
	// Second pass: child-1 transitions running -> waiting => one completion.
	menu2 := ccTestMenu()
	menu2.Items[1].Session.Status = "waiting"
	second := buildCommandCenterSnapshot(menu2, "personal", "", tracker)
	if len(second.RecentlyCompleted) != 1 {
		t.Fatalf("expected 1 completion, got %+v", second.RecentlyCompleted)
	}
	if second.RecentlyCompleted[0].ID != "child-1" || second.RecentlyCompleted[0].Status != "waiting" {
		t.Fatalf("unexpected completion: %+v", second.RecentlyCompleted[0])
	}
}

func TestParseDecisionsWaiting(t *testing.T) {
	dir := t.TempDir()
	adDir := filepath.Join(dir, "agent-deck")
	if err := os.MkdirAll(adDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# Open Items\n\n## D. DECISIONS — waiting on Ashesh (not work, rulings)\n" +
		"#1361 is conductor.enabled necessary? · #1358 auto-allow read-only cmds · self-heal approve\n\n" +
		"## E. HOUSEKEEPING\nroutine\n"
	if err := os.WriteFile(filepath.Join(adDir, "OPEN-ITEMS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	decisions := parseDecisionsWaiting(dir)
	if len(decisions) != 3 {
		t.Fatalf("expected 3 decisions, got %d: %+v", len(decisions), decisions)
	}
	if decisions[0].ID != "#1361" {
		t.Fatalf("expected first id #1361, got %q", decisions[0].ID)
	}
	if decisions[0].Route != "conductor-agent-deck" {
		t.Fatalf("expected route conductor-agent-deck, got %q", decisions[0].Route)
	}
	// The non-#-prefixed item still parses with empty id.
	if decisions[2].ID != "" || !strings.Contains(decisions[2].Question, "self-heal") {
		t.Fatalf("unexpected third decision: %+v", decisions[2])
	}
}

func TestParseDecisionsWaitingMultilineList(t *testing.T) {
	dir := t.TempDir()
	adDir := filepath.Join(dir, "agent-deck")
	if err := os.MkdirAll(adDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Markdown-list shape (the other common form): each decision on its own
	// line with a list marker. Subsequent items must NOT be lost, and the
	// list marker must be stripped while the #id is preserved.
	content := "# Open Items\n\n## D. DECISIONS — waiting on Ashesh\n" +
		"- #1361 is conductor.enabled necessary?\n" +
		"- #1399 docs revamp (PR)\n" +
		"* self-heal: approve\n\n" +
		"## E. HOUSEKEEPING\nroutine\n"
	if err := os.WriteFile(filepath.Join(adDir, "OPEN-ITEMS.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	decisions := parseDecisionsWaiting(dir)
	if len(decisions) != 3 {
		t.Fatalf("expected 3 decisions from multiline list, got %d: %+v", len(decisions), decisions)
	}
	if decisions[0].ID != "#1361" {
		t.Fatalf("expected #1361 with marker stripped, got %q (q=%q)", decisions[0].ID, decisions[0].Question)
	}
	if decisions[1].ID != "#1399" {
		t.Fatalf("expected #1399, got %q", decisions[1].ID)
	}
	if strings.HasPrefix(decisions[2].Question, "*") || !strings.Contains(decisions[2].Question, "self-heal") {
		t.Fatalf("list marker not stripped on third item: %+v", decisions[2])
	}
}

func TestCommandCenterStatusEndpointJSON(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: ccTestMenu()}

	req := httptest.NewRequest(http.MethodGet, "/api/command-center/status", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var snap CommandCenterSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}
	if len(snap.Conductors) != 1 {
		t.Fatalf("expected 1 conductor, got %d", len(snap.Conductors))
	}
	// Privacy: snapshot must carry no secret-shaped fields.
	body := rr.Body.String()
	for _, banned := range []string{"token", "Token", "apiKey", "api_key", "secret", "password", "credential"} {
		if strings.Contains(body, banned) {
			t.Fatalf("snapshot leaked a secret-shaped field %q: %s", banned, body)
		}
	}
}

func TestCommandCenterEventsUnauthorizedWhenTokenEnabled(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret-token"})
	srv.menuData = &fakeMenuDataLoader{snapshot: ccTestMenu()}

	req := httptest.NewRequest(http.MethodGet, "/events/command-center", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestCommandCenterEventsStreamInitialAndChange(t *testing.T) {
	origPoll := commandCenterPollInterval
	commandCenterPollInterval = 30 * time.Millisecond
	defer func() { commandCenterPollInterval = origPoll }()
	origHB := commandCenterHeartbeatInterval
	commandCenterHeartbeatInterval = 2 * time.Second
	defer func() { commandCenterHeartbeatInterval = origHB }()

	first := ccTestMenu()
	second := ccTestMenu()
	second.Items[1].Session.Status = "waiting" // child-1 completes

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &rotatingMenuDataLoader{snapshots: []*MenuSnapshot{first, second}}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/command-center", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected event-stream, got %q", ct)
	}
	reader := bufio.NewReader(resp.Body)

	event, payload1, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if event != "command-center" {
		t.Fatalf("expected event command-center, got %q", event)
	}
	if !strings.Contains(payload1, `"name":"agent-deck"`) {
		t.Fatalf("first payload missing conductor: %s", payload1)
	}

	// Next emit should reflect the change and carry the completion.
	_, payload2, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("read second event: %v", err)
	}
	if !strings.Contains(payload2, `"recentlyCompleted"`) || !strings.Contains(payload2, `"child-1"`) {
		t.Fatalf("second payload missing completion: %s", payload2)
	}
}

func TestCommandCenterAskRejectsUnknownTarget(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: ccTestMenu()}

	body := strings.NewReader(`{"target":"conductor-evil","text":"do bad things"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/ask", body)
	req.Header.Set("Content-Type", "application/json")
	// Same-origin so the CSRF gate (no token configured here) passes.
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown target, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INVALID_TARGET") {
		t.Fatalf("expected INVALID_TARGET, got: %s", rr.Body.String())
	}
}

func TestCommandCenterAskForbiddenWhenMutationsDisabled(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.menuData = &fakeMenuDataLoader{snapshot: ccTestMenu()}

	body := strings.NewReader(`{"target":"maestro","text":"hi"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/ask", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when mutations disabled, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCommandCenterAskRequiresText(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: ccTestMenu()}

	body := strings.NewReader(`{"target":"maestro","text":"   "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/ask", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty text, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestResolveAskTarget(t *testing.T) {
	allow := []string{"maestro", "conductor-agent-deck", "conductor-maestro"}
	if got := resolveAskTarget("maestro", allow); got != "conductor-maestro" {
		t.Fatalf("maestro should resolve to conductor-maestro when present, got %q", got)
	}
	if got := resolveAskTarget("maestro", []string{"maestro"}); got != "maestro" {
		t.Fatalf("maestro should fall back to bare maestro, got %q", got)
	}
	if got := resolveAskTarget("conductor-agent-deck", allow); got != "conductor-agent-deck" {
		t.Fatalf("explicit allowed target should resolve, got %q", got)
	}
	if got := resolveAskTarget("conductor-evil", allow); got != "" {
		t.Fatalf("disallowed target should return empty, got %q", got)
	}
}
