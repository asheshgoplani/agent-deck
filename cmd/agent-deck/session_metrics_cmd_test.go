package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedMetricsJournal writes a fixture journal into the default profile of an
// isolated HOME: one session with two turns, two sends, one restart.
func seedMetricsJournal(t *testing.T, home string) []health.Event {
	t.Helper()
	// runAgentDeck points XDG_DATA_HOME under home and selects profile ch_support_test.
	dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "logs", "health")
	j := health.NewJournal(dir)
	base := time.Now().UTC().Add(-30 * time.Minute)
	events := []health.Event{
		{TS: base, SessionID: "metrics-s1", Kind: health.KindStatus, From: "idle", To: "running"},
		{TS: base.Add(10 * time.Second), SessionID: "metrics-s1", Kind: health.KindStatus, From: "running", To: "waiting"},
		{TS: base.Add(20 * time.Second), SessionID: "metrics-s1", Kind: health.KindSend, Detail: map[string]any{"outcome": health.SendConfirmed, "ack_ms": 250.0}},
		{TS: base.Add(21 * time.Second), SessionID: "metrics-s1", Kind: health.KindStatus, From: "waiting", To: "running"},
		{TS: base.Add(51 * time.Second), SessionID: "metrics-s1", Kind: health.KindStatus, From: "running", To: "waiting"},
		{TS: base.Add(60 * time.Second), SessionID: "metrics-s1", Kind: health.KindSend, Detail: map[string]any{"outcome": health.SendUnconfirmed}},
		{TS: base.Add(70 * time.Second), SessionID: "metrics-s1", Kind: health.KindRestart},
		{TS: base.Add(5 * time.Second), SessionID: "metrics-s2", Kind: health.KindStatus, From: "running", To: "idle"},
	}
	for _, e := range events {
		if err := j.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return events
}

// normalizeMetricsJSON blanks the wall-clock fields so the shape can be
// compared against a golden file.
func normalizeMetricsJSON(t *testing.T, raw string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	m["since"], m["until"] = "<since>", "<until>"
	if last, ok := m["last_status_change"].(map[string]any); ok {
		last["at"], last["age_ms"] = "<at>", "<age_ms>"
	}
	if w, ok := m["waiting_ms"].(float64); ok && w > 0 {
		m["waiting_ms"] = "<positive>"
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestSessionMetricsCLIGolden(t *testing.T) {
	home := t.TempDir()
	seedMetricsJournal(t, home)
	out, stderr, code := runAgentDeck(t, home, "session", "metrics", "metrics-s1", "--json", "--since", "1h")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	got := normalizeMetricsJSON(t, out)
	golden := filepath.Join("testdata", "session_metrics.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.WriteFile(golden, []byte(got), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Fatalf("golden mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
	// Human output names the same numbers.
	out, _, code = runAgentDeck(t, home, "session", "metrics", "metrics-s1", "--since", "1h")
	if code != 0 || !strings.Contains(out, "turns: 2") || !strings.Contains(out, "restarts: 1") {
		t.Fatalf("human output: %d %s", code, out)
	}
}

func TestSessionMetricsCLIUnknownsAndAll(t *testing.T) {
	home := t.TempDir()
	// No journal at all: unknowns, never zeros dressed as facts.
	out, stderr, code := runAgentDeck(t, home, "session", "metrics", "nobody", "--json")
	if code != 2 {
		t.Fatalf("no registry and no journal must be not found: %d %s %s", code, out, stderr)
	}
	seedMetricsJournal(t, home)
	out, stderr, code = runAgentDeck(t, home, "session", "metrics", "metrics-s2", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	turns := m["turns"].(map[string]any)
	if m["journal"] != "ok" || turns["count"].(float64) != 1 || turns["p50_ms"] != nil {
		t.Fatalf("terminal edge without a start: %s", out)
	}
	if m["sends"].(map[string]any)["unconfirmed_rate"] != nil || m["worker"] != nil {
		t.Fatalf("fabricated: %s", out)
	}
	out, stderr, code = runAgentDeck(t, home, "session", "metrics", "--all", "--json")
	if code != 0 {
		t.Fatalf("--all exit %d: %s %s", code, out, stderr)
	}
	var all []map[string]any
	if err := json.Unmarshal([]byte(out), &all); err != nil || len(all) != 2 || all[0]["session_id"] != "metrics-s1" {
		t.Fatalf("--all: %v %s", err, out)
	}
	for _, args := range [][]string{{"--since", "0s"}, {"--all", "x"}, {}} {
		if _, _, code := runAgentDeck(t, home, append([]string{"session", "metrics"}, args...)...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestSessionMetricsCLIKillSwitchReadsAsDisabled(t *testing.T) {
	home := t.TempDir()
	seedMetricsJournal(t, home)
	configPath := filepath.Join(home, ".config", "agent-deck", "config.toml")
	os.MkdirAll(filepath.Dir(configPath), 0o700)
	os.WriteFile(configPath, []byte("[health]\nsession_events = false\n"), 0o600)
	out, _, code := runAgentDeck(t, home, "session", "metrics", "metrics-s1", "--json")
	if code != 0 || !strings.Contains(out, `"journal":"disabled"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestHealthJSONCarriesSessionAggregate(t *testing.T) {
	home := t.TempDir()
	seedMetricsJournal(t, home)
	out, stderr, code := runAgentDeck(t, home, "health", "--json", "--since", "1h")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	var report struct {
		Sessions health.SessionAggregate `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	a := report.Sessions
	if a.Journal != "ok" || a.SessionsObserved != 2 || a.Turns != 3 || a.TurnsPerDay == nil || *a.TurnsPerDay != 72 {
		t.Fatalf("aggregate: %+v", a)
	}
	if a.MedianTurnMS == nil || *a.MedianTurnMS != 20000 || a.UnconfirmedSendRate == nil || *a.UnconfirmedSendRate != 0.5 || a.RestartsPerDay == nil || *a.RestartsPerDay != 24 {
		t.Fatalf("aggregate: %+v", a)
	}
	if a.SessionsWithDeadLetters == nil || *a.SessionsWithDeadLetters != 0 {
		t.Fatalf("dead letters: %+v", a)
	}
	out, _, _ = runAgentDeck(t, home, "health", "--since", "1h")
	if !strings.Contains(out, "Session metrics") || !strings.Contains(out, "turns/day") {
		t.Fatalf("human health output: %s", out)
	}
}

func TestJournalSendOutcome(t *testing.T) {
	cases := map[string]string{
		deliverySubmitted:         health.SendConfirmed,
		deliveryQueued:            health.SendConfirmed,
		deliveryUnverified:        health.SendUnconfirmed,
		deliveryQueuedSocket:      health.SendUnconfirmed,
		"delivered":               health.SendUnconfirmed,
		deliveryTypedNotSubmitted: health.SendFailed,
		deliveryNoEvidence:        health.SendFailed,
		deliverySendFailed:        health.SendFailed,
		"menu_open":               health.SendFailed,
	}
	for delivery, want := range cases {
		if got := journalSendOutcome(delivery, nil); got != want {
			t.Errorf("%s: got %s want %s", delivery, got, want)
		}
	}
	if got := journalSendOutcome(deliverySubmitted, os.ErrClosed); got != health.SendFailed {
		t.Errorf("an error is a failure whatever the delivery says: %s", got)
	}
}

func TestRemoteSessionMetricsForwardingAndUnknownRemote(t *testing.T) {
	args, err := remoteCommandArgs([]string{"session", "metrics", "abc", "--json"})
	if err != nil || strings.Join(args, " ") != "session metrics abc --json" {
		t.Fatalf("forwarding: %v %v", args, err)
	}
	if _, err := remoteCommandArgs([]string{"metrics", "abc"}); err == nil {
		t.Fatal("bare metrics must not be forwarded")
	}
	old := "Error: unknown session command: metrics\nUsage: agent-deck session <command> [options]\n..."
	msg, ok := remoteMetricsUnsupported("lab", []string{"session", "metrics", "abc"}, 1, old)
	if !ok || strings.Count(msg, "\n") != 0 || !strings.Contains(msg, "lab") || !strings.Contains(msg, "session metrics") {
		t.Fatalf("older remote must read as one clear line: %q %v", msg, ok)
	}
	if _, ok := remoteMetricsUnsupported("lab", []string{"session", "metrics", "abc"}, 1, "Error: session not found"); ok {
		t.Fatal("other remote errors must pass through")
	}
	if _, ok := remoteMetricsUnsupported("lab", []string{"session", "show", "abc"}, 1, old); ok {
		t.Fatal("only session metrics is translated")
	}
}

// End to end: a daemon pass observes running->waiting for a registered
// session, and the CLI derives one turn from the journal it wrote.
func TestSessionMetricsReportsDaemonObservedTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	for _, key := range []string{"XDG_CACHE_HOME", "XDG_STATE_HOME", "AGENT_DECK_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(key, "")
	}
	const profile = "ch_support_test" // the profile runAgentDeck selects
	t.Setenv("AGENTDECK_PROFILE", profile)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	inst := &session.Instance{ID: "daemon-turn-1", Title: "daemon turn", ProjectPath: home, GroupPath: session.DefaultGroupPath, Tool: "claude", Status: session.StatusRunning, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil { // a live TUI heartbeat: the daemon reads DB rows, no tmux probe
		t.Fatal(err)
	}
	if err := db.WriteStatus(inst.ID, "running", inst.Tool); err != nil {
		t.Fatal(err)
	}
	d := session.NewTransitionDaemon()
	d.SyncOnce(context.Background())
	if err := db.WriteStatus(inst.ID, "waiting", inst.Tool); err != nil {
		t.Fatal(err)
	}
	d.SyncOnce(context.Background())

	out, stderr, code := runAgentDeck(t, home, "session", "metrics", "daemon turn", "--json", "--since", "1h")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	var m health.SessionMetrics
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m.SessionID != inst.ID || m.Title != inst.Title || m.Journal != health.JournalOK || m.Turns.Count != 1 {
		t.Fatalf("one daemon-observed turn expected: %s", out)
	}
	if m.LastStatusChange == nil || m.LastStatusChange.Status != "waiting" || m.WaitingMS == nil {
		t.Fatalf("last status change: %s", out)
	}
}
