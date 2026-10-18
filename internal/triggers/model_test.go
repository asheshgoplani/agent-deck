package triggers

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var modelNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func testResolver() Resolver {
	return NewResolver([]ProfileSessions{
		{Profile: "default", Sessions: []Resolved{
			{SessionID: "3f2a1b4c-1712345678", Title: "inbox-triage"},
			{SessionID: "9e8d7c6b-1712345679", Title: "conductor-ops"},
		}},
		{Profile: "work", Sessions: []Resolved{
			{SessionID: "aaaabbbb-1712345680", Title: "mail-desk"},
			{SessionID: "ccccdddd-1712345681", Title: "inbox-triage"},
		}},
	})
}

func fire(outcome, sid string) *statedb.TriggerFireRow {
	return &statedb.TriggerFireRow{TS: modelNow.Add(-time.Minute), Outcome: outcome, TargetSessionID: sid, Line: "tick"}
}

func viewByID(t *testing.T, views []View, id string) View {
	t.Helper()
	for _, v := range views {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no view %s", id)
	return View{}
}

func TestHealthRulesAndPrecedence(t *testing.T) {
	rows := []statedb.TriggerRow{
		// A dead target outranks a failing last fire.
		{ID: "systemd:dead", Kind: KindSystemd, Source: "dead", Target: "overnight-orchestrator", Scanned: true, Unit: "dead"},
		// No written target, but the last fire hit nothing.
		{ID: "cron:poll.sh", Kind: KindCron, Source: "poll.sh", Pushed: true},
		{ID: "launchd:never", Kind: KindLaunchd, Source: "never", Target: "inbox-triage", Scanned: true, Unit: "never"},
		{ID: "heartbeat:ops", Kind: KindHeartbeat, Source: "ops", Target: "conductor-ops", Pushed: true},
		{ID: "watcher:gh", Kind: KindWatcher, Source: "gh", Target: "inbox-triage", Pushed: true},
		// A failing fire with no target written at all.
		{ID: "ssh:ssh", Kind: KindSSH, Source: "ssh", Pushed: true},
	}
	stats := map[string]*statedb.TriggerStats{
		"systemd:dead":  {LastFire: fire(OutcomeFailed, ""), Fires24h: 3},
		"cron:poll.sh":  {LastFire: fire(OutcomeDeadTarget, ""), Fires24h: 1},
		"heartbeat:ops": {LastFire: fire(OutcomeFailed, "9e8d7c6b-1712345679"), Fires24h: 1},
		"watcher:gh":    {LastFire: fire(OutcomeMuted, "3f2a1b4c-1712345678"), Fires24h: 1},
		"ssh:ssh":       {LastFire: fire(OutcomeQueued, ""), Fires24h: 1},
	}
	views := Build(rows, stats, testResolver(), "deckbox", modelNow)
	want := map[string]string{
		"systemd:dead":  HealthDeadTarget,
		"cron:poll.sh":  HealthDeadTarget,
		"launchd:never": HealthNeverFired,
		"heartbeat:ops": HealthFailing,
		"watcher:gh":    HealthOK,
		"ssh:ssh":       HealthOK,
	}
	for id, h := range want {
		if got := viewByID(t, views, id).Health; got != h {
			t.Errorf("%s health = %s, want %s", id, got, h)
		}
	}
	dead := viewByID(t, views, "systemd:dead")
	if dead.Target.Resolved || dead.Target.SessionID != "" || dead.Host != "deckbox" {
		t.Errorf("dead view: %+v", dead)
	}
	never := viewByID(t, views, "launchd:never")
	if !never.Target.Resolved || never.Target.SessionID != "3f2a1b4c-1712345678" || never.Target.Profile != "default" || never.LastFire != nil {
		t.Errorf("resolved never-fired: %+v", never)
	}
}

func TestBuildOriginRunnableMuteAndNoise(t *testing.T) {
	until := modelNow.Add(2 * time.Hour)
	rows := []statedb.TriggerRow{
		{ID: "systemd:both", Kind: KindSystemd, Source: "both", Pushed: true, Scanned: true, Unit: "both", Target: "inbox-triage", MutedUntil: until},
		{ID: "launchd:scan", Kind: KindLaunchd, Source: "scan", Scanned: true, Unit: "com.x.scan", Target: "mail-desk", TargetProfile: "work"},
		{ID: "systemd:pushonly", Kind: KindSystemd, Source: "pushonly", Pushed: true, Target: "inbox-triage", MutedUntil: modelNow.Add(-time.Minute)},
		{ID: "heartbeat:ops", Kind: KindHeartbeat, Source: "ops", Pushed: true, Scanned: true, Unit: "x", Target: "conductor-ops"},
	}
	stats := map[string]*statedb.TriggerStats{
		"systemd:both":     {LastFire: fire(OutcomeDelivered, "3f2a1b4c-1712345678"), Fires24h: 10, Noops24h: 9},
		"systemd:pushonly": {LastFire: fire(OutcomeDelivered, ""), Fires24h: 4, Noops24h: 1},
	}
	views := Build(rows, stats, testResolver(), "deckbox", modelNow)

	both := viewByID(t, views, "systemd:both")
	if both.Origin != "push+scan" || !both.Runnable || !both.Muted || both.MutedUntil == nil || !both.MutedUntil.Equal(until) {
		t.Errorf("both: %+v", both)
	}
	if both.NoopShare24h == nil || *both.NoopShare24h != 0.9 || !both.IsProblem() {
		t.Errorf("noise share 9/10 must be very noisy: %v", both.NoopShare24h)
	}
	scan := viewByID(t, views, "launchd:scan")
	if scan.Origin != "scan" || !scan.Runnable || scan.NoopShare24h != nil || scan.Fires24h != 0 {
		t.Errorf("scan-only: %+v", scan)
	}
	// The sender's -p profile picks the session in that profile.
	if scan.Target.SessionID != "aaaabbbb-1712345680" || scan.Target.Profile != "work" {
		t.Errorf("profile-scoped target: %+v", scan.Target)
	}
	push := viewByID(t, views, "systemd:pushonly")
	if push.Origin != "push" || push.Runnable || push.Muted || push.MutedUntil != nil {
		t.Errorf("push-only (expired mute): %+v", push)
	}
	if push.NoopShare24h == nil || *push.NoopShare24h != 0.25 || push.IsProblem() {
		t.Errorf("quiet trigger flagged: %v", push.NoopShare24h)
	}
	if hb := viewByID(t, views, "heartbeat:ops"); hb.Runnable {
		t.Errorf("only launchd/systemd units are runnable: %+v", hb)
	}
	if _, err := RunCommand(push, 501); err == nil {
		t.Error("RunCommand accepted a push-only trigger")
	}
	argv, err := RunCommand(both, 501)
	if err != nil || len(argv) != 4 || argv[0] != "systemctl" || argv[3] != "both.service" {
		t.Errorf("systemd run argv = %v %v", argv, err)
	}
	argv, err = RunCommand(scan, 501)
	if err != nil || len(argv) != 3 || argv[0] != "launchctl" || argv[1] != "kickstart" || argv[2] != "gui/501/com.x.scan" {
		t.Errorf("launchd run argv = %v %v", argv, err)
	}

	s := Summarize(views)
	if s.Total != 4 || s.VeryNoisy != 1 || s.DeadTargets != 0 || s.Failing != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func filterFixture() []View {
	rows := []statedb.TriggerRow{
		{ID: "systemd:a", Kind: KindSystemd, Source: "a", Host: "deckbox", Target: "inbox-triage", Pushed: true},
		{ID: "launchd:b", Kind: KindLaunchd, Source: "B", Host: "mac", Target: "gone", Scanned: true, Unit: "b"},
		{ID: "heartbeat:ops", Kind: KindHeartbeat, Source: "ops", Host: "deckbox", Target: "conductor-ops", Pushed: true},
		{ID: "session:9e8d7c6b-1712345679", Kind: KindSession, Source: "9e8d7c6b-1712345679", Host: "deckbox", Target: "inbox-triage", Pushed: true},
		{ID: "cli:cli", Kind: KindCLI, Source: "cli", Host: "deckbox", Target: "inbox-triage", Pushed: true},
		{ID: "cron:x", Kind: KindCron, Source: "x", Host: "deckbox", Pushed: true},
	}
	stats := map[string]*statedb.TriggerStats{
		"systemd:a":     {LastFire: fire(OutcomeDelivered, "3f2a1b4c-1712345678"), Fires24h: 2, FiresByID: map[string]int{"3f2a1b4c-1712345678": 2}},
		"heartbeat:ops": {LastFire: fire(OutcomeFailed, "9e8d7c6b-1712345679"), Fires24h: 1, FiresByID: map[string]int{"9e8d7c6b-1712345679": 1}},
		// cron:x has no written target but fired 5 times at inbox-triage.
		"cron:x":                      {LastFire: fire(OutcomeDelivered, "3f2a1b4c-1712345678"), Fires24h: 5, FiresByID: map[string]int{"3f2a1b4c-1712345678": 5}},
		"session:9e8d7c6b-1712345679": {LastFire: fire(OutcomeDelivered, "3f2a1b4c-1712345678"), Fires24h: 1, FiresByID: map[string]int{"3f2a1b4c-1712345678": 1}},
	}
	return Build(rows, stats, testResolver(), "deckbox", modelNow)
}

func ids(views []View) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.ID)
	}
	return out
}

func sameIDs(t *testing.T, name string, got []View, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Fatalf("%s = %v, want %v", name, g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("%s = %v, want %v", name, g, want)
		}
	}
}

func TestFilter(t *testing.T) {
	views := filterFixture()
	// Default: no session/cli senders; sorted by host, kind, source.
	sameIDs(t, "default", Filter{}.Apply(views), "cron:x", "heartbeat:ops", "systemd:a", "launchd:b")
	sameIDs(t, "--all", Filter{All: true}.Apply(views), "cli:cli", "cron:x", "heartbeat:ops", "session:9e8d7c6b-1712345679", "systemd:a", "launchd:b")
	sameIDs(t, "--kind session", Filter{Kind: KindSession}.Apply(views), "session:9e8d7c6b-1712345679")
	sameIDs(t, "--kind systemd", Filter{Kind: KindSystemd}.Apply(views), "systemd:a")
	sameIDs(t, "--health problems", Filter{Health: "problems"}.Apply(views), "heartbeat:ops", "launchd:b")
	sameIDs(t, "--health dead-target", Filter{Health: HealthDeadTarget}.Apply(views), "launchd:b")
	// Reverse view: busiest at the session first; a trigger that only fired
	// at it (no written target) counts too.
	sameIDs(t, "--session", Filter{SessionID: "3f2a1b4c-1712345678"}.Apply(views), "cron:x", "systemd:a")
	sameIDs(t, "--session --all", Filter{SessionID: "3f2a1b4c-1712345678", All: true}.Apply(views), "cron:x", "systemd:a", "session:9e8d7c6b-1712345679", "cli:cli")
}

func TestBadges(t *testing.T) {
	b := Badges(filterFixture())
	if got := b["3f2a1b4c-1712345678"]; got.Count != 2 || got.Bad {
		t.Errorf("inbox-triage badge = %+v (session/cli senders must not count)", got)
	}
	if got := b["9e8d7c6b-1712345679"]; got.Count != 1 || !got.Bad {
		t.Errorf("conductor badge = %+v, want one failing", got)
	}
	if _, ok := b[""]; ok {
		t.Error("badge for an empty session id")
	}
}

func TestNewResolver(t *testing.T) {
	r := testResolver()
	cases := []struct {
		target, profile, wantID, wantProfile string
		ok                                   bool
	}{
		{"3f2a1b4c-1712345678", "", "3f2a1b4c-1712345678", "default", true},
		{"aaaabbbb", "", "aaaabbbb-1712345680", "work", true}, // 8-char id prefix
		{"aaaa", "", "", "", false},                           // too short for a prefix
		{"inbox-triage", "", "3f2a1b4c-1712345678", "default", true},
		{"inbox-triage", "work", "ccccdddd-1712345681", "work", true},
		{"mail-desk", "default", "", "", false},
		{"mail-desk", "", "aaaabbbb-1712345680", "work", true},
		{"overnight-orchestrator", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		got, ok := r(c.target, c.profile)
		if ok != c.ok || got.SessionID != c.wantID || got.Profile != c.wantProfile {
			t.Errorf("resolve(%q, %q) = %+v %v, want %s/%s %v", c.target, c.profile, got, ok, c.wantID, c.wantProfile, c.ok)
		}
	}
	// An id match in a later profile beats an earlier title match.
	r2 := NewResolver([]ProfileSessions{
		{Profile: "a", Sessions: []Resolved{{SessionID: "11111111-1", Title: "22222222-2"}}},
		{Profile: "b", Sessions: []Resolved{{SessionID: "22222222-2", Title: "x"}}},
	})
	if got, _ := r2("22222222-2", ""); got.Profile != "b" {
		t.Errorf("id must beat title: %+v", got)
	}
}

func TestHealthAfterOutcome(t *testing.T) {
	want := map[string]string{
		OutcomeDelivered:  HealthOK,
		OutcomeQueued:     HealthOK,
		OutcomeMuted:      HealthOK,
		OutcomeFailed:     HealthFailing,
		OutcomeDeadTarget: HealthDeadTarget,
	}
	for outcome, h := range want {
		if got := HealthAfter(outcome); got != h {
			t.Errorf("HealthAfter(%s) = %s, want %s", outcome, got, h)
		}
	}
}
