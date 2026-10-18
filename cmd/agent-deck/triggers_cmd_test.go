package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// triggersDoc is `triggers --json` as a client reads it.
type triggersDoc struct {
	Triggers []struct {
		ID         string `json:"id"`
		Host       string `json:"host"`
		Kind       string `json:"kind"`
		Source     string `json:"source"`
		ConfigPath string `json:"config_path"`
		Verb       string `json:"verb"`
		Target     struct {
			AsWritten string `json:"as_written"`
			By        string `json:"by"`
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
			Resolved  bool   `json:"resolved"`
		} `json:"target"`
		Origin       string   `json:"origin"`
		Inferred     bool     `json:"inferred"`
		LastFire     *string  `json:"last_fire"`
		LastLine     string   `json:"last_line"`
		LastOutcome  string   `json:"last_outcome"`
		Fires24h     int      `json:"fires_24h"`
		NoopShare24h *float64 `json:"noop_share_24h"`
		Health       string   `json:"health"`
		Muted        bool     `json:"muted"`
		MutedUntil   *string  `json:"muted_until"`
		Paused       *bool    `json:"paused"`
		Runnable     bool     `json:"runnable"`
	} `json:"triggers"`
	Hosts       map[string]string `json:"hosts"`
	Count       int               `json:"count"`
	GeneratedAt string            `json:"generated_at"`
}

func listTriggers(t *testing.T, home string, args ...string) triggersDoc {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, append([]string{"triggers", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("triggers --json %v: exit %d\n%s\n%s", args, code, stdout, stderr)
	}
	var doc triggersDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("triggers JSON: %v\n%s", err, stdout)
	}
	if doc.Hosts == nil || doc.GeneratedAt == "" || doc.Count != len(doc.Triggers) {
		t.Fatalf("envelope: %+v", doc)
	}
	return doc
}

type firesDoc struct {
	TriggerID string `json:"trigger_id"`
	Count     int    `json:"count"`
	Fires     []struct {
		TS        string `json:"ts"`
		Target    string `json:"target"`
		SessionID string `json:"session_id"`
		Outcome   string `json:"outcome"`
		Line      string `json:"line"`
		Noop      bool   `json:"noop"`
	} `json:"fires"`
}

func listFires(t *testing.T, home, id string) firesDoc {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "triggers", "fires", id, "--json")
	if code != 0 {
		t.Fatalf("triggers fires %s: exit %d\n%s\n%s", id, code, stdout, stderr)
	}
	var doc firesDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("fires JSON: %v\n%s", err, stdout)
	}
	if doc.TriggerID != id || doc.Count != len(doc.Fires) {
		t.Fatalf("fires envelope: %+v", doc)
	}
	return doc
}

// A send tagged with --source records a fire under that source: the row,
// its target resolved to the session, and a fire with the first line.
// A stopped target makes the send fail (exit 1, unchanged): outcome failed.
func TestTriggersSendSourceRecordsFire(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "trig-target", "claude")
	_, _, code := runAgentDeck(t, home, "session", "send", id, "[agent-deck from:x] tick: 3 new mails\nbody", "--source", "channel:telegram")
	if code != 1 {
		t.Fatalf("send to a stopped session: exit %d, want 1 (unchanged)", code)
	}
	doc := listTriggers(t, home)
	if len(doc.Triggers) != 1 {
		t.Fatalf("rows = %+v", doc.Triggers)
	}
	row := doc.Triggers[0]
	if row.ID != "channel:telegram" || row.Kind != "channel" || row.Source != "telegram" || row.Inferred || row.Origin != "push" || row.Verb != "send" {
		t.Fatalf("row identity: %+v", row)
	}
	if row.Target.AsWritten != id || row.Target.By != "title" && row.Target.By != "id" || !row.Target.Resolved || row.Target.SessionID != id || row.Target.Title != "trig-target" {
		t.Fatalf("row target: %+v", row.Target)
	}
	if row.LastOutcome != "failed" || row.Health != "failing" || row.LastLine != "tick: 3 new mails" || row.Fires24h != 1 || row.LastFire == nil || row.Paused == nil || *row.Paused {
		t.Fatalf("row state: %+v", row)
	}
	if row.NoopShare24h == nil || *row.NoopShare24h != 0 {
		t.Fatalf("noop share: %v", row.NoopShare24h)
	}
	for _, v := range doc.Hosts {
		if v != "ok" {
			t.Fatalf("local host state: %v", doc.Hosts)
		}
	}
	fires := listFires(t, home, "channel:telegram")
	if len(fires.Fires) != 1 || fires.Fires[0].Outcome != "failed" || fires.Fires[0].SessionID != id || fires.Fires[0].Target != id || fires.Fires[0].Line != "tick: 3 new mails" || fires.Fires[0].Noop {
		t.Fatalf("fires: %+v", fires)
	}

	// AGENTDECK_SOURCE tags a send the same way; the same text again is a no-op.
	for i := 0; i < 2; i++ {
		if _, _, code := runAgentDeckEnv(t, home, "", []string{"AGENTDECK_SOURCE=watcher:gh"}, "session", "send", "trig-target", "same text"); code != 1 {
			t.Fatalf("send %d: exit %d", i, code)
		}
	}
	gh := listFires(t, home, "watcher:gh")
	if len(gh.Fires) != 2 || !gh.Fires[0].Noop || gh.Fires[1].Noop {
		t.Fatalf("AGENTDECK_SOURCE fires / no-op: %+v", gh)
	}
	// A plain shell send is a cli sender: kept, shown only with --all.
	if _, _, code := runAgentDeck(t, home, "session", "send", "trig-target", "hello"); code != 1 {
		t.Fatalf("plain send: exit %d", code)
	}
	if doc := listTriggers(t, home); len(doc.Triggers) != 2 {
		t.Fatalf("default list must hide the cli sender: %+v", doc.Triggers)
	}
	all := listTriggers(t, home, "--all")
	found := false
	for _, r := range all.Triggers {
		found = found || (r.Kind == "cli" && r.Inferred)
	}
	if len(all.Triggers) != 3 || !found {
		t.Fatalf("--all: %+v", all.Triggers)
	}
	// --session answers "what wakes" this session, by title.
	if doc := listTriggers(t, home, "--session", "trig-target"); len(doc.Triggers) != 2 {
		t.Fatalf("--session: %+v", doc.Triggers)
	}
	stdout, _, code := runAgentDeck(t, home, "triggers", "--session", "trig-target")
	if code != 0 || !strings.HasPrefix(stdout, "What wakes trig-target (") || !strings.Contains(stdout, "2 triggers · 0 dead targets · 2 failing") {
		t.Fatalf("--session table: exit %d\n%s", code, stdout)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "--session", "no-such-session"); code != 2 {
		t.Fatalf("--session of an unknown session: exit %d, want 2", code)
	}
}

// A send to a title no session has records a dead-target fire before it
// exits with its unchanged code.
func TestTriggersSendDeadTarget(t *testing.T) {
	home := t.TempDir()
	addSessionJSON(t, home, "alive", "claude")
	stdout, stderr, code := runAgentDeck(t, home, "session", "send", "overnight-orchestrator", "tick", "--source", "heartbeat:ops", "--json")
	if code != 2 {
		t.Fatalf("send to a dead title: exit %d, want 2 (unchanged)\n%s\n%s", code, stdout, stderr)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res["code"] != ErrCodeNotFound {
		t.Fatalf("dead-target JSON changed: %v %s", err, stdout)
	}
	doc := listTriggers(t, home)
	if len(doc.Triggers) != 1 {
		t.Fatalf("rows: %+v", doc.Triggers)
	}
	row := doc.Triggers[0]
	if row.ID != "heartbeat:ops" || row.Health != "dead-target" || row.LastOutcome != "dead-target" || row.Target.AsWritten != "overnight-orchestrator" || row.Target.Resolved {
		t.Fatalf("dead row: %+v", row)
	}
	fires := listFires(t, home, "heartbeat:ops")
	if len(fires.Fires) != 1 || fires.Fires[0].Outcome != "dead-target" || fires.Fires[0].SessionID != "" {
		t.Fatalf("dead fires: %+v", fires)
	}
	table, _, code := runAgentDeck(t, home, "triggers")
	if code != 0 || !strings.Contains(table, "overnight-orchestrator ✗") || !strings.Contains(table, "dead target") ||
		!strings.Contains(table, "1 triggers · 1 dead targets · 0 failing") || !strings.Contains(table, "HOST     KIND      NAME") {
		t.Fatalf("table:\n%s", table)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "--health", "problems", "--json"); code != 0 {
		t.Fatalf("--health problems: exit %d", code)
	}
	if doc := listTriggers(t, home, "--health", "ok"); len(doc.Triggers) != 0 {
		t.Fatalf("--health ok kept a dead trigger: %+v", doc.Triggers)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "--health", "bogus"); code != 2 {
		t.Fatalf("--health bogus: exit %d", code)
	}
}

// A muted trigger's send is accepted (exit 0, success JSON with muted), logged
// with outcome muted, and not delivered: the stopped target is never even
// asked. Unmute restores the normal send.
func TestTriggersMutedSendNotDelivered(t *testing.T) {
	home := t.TempDir()
	id := addSessionJSON(t, home, "mute-target", "claude")
	if _, _, code := runAgentDeck(t, home, "session", "send", id, "first", "--source", "poller"); code != 1 {
		t.Fatalf("first send: exit %d", code)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "mute", "job:missing", "--json"); code != 2 {
		t.Fatalf("mute of an unknown trigger: exit %d, want 2", code)
	}
	stdout, stderr, code := runAgentDeck(t, home, "triggers", "mute", "job:poller", "--for", "2h", "--json")
	if code != 0 {
		t.Fatalf("mute: exit %d %s %s", code, stdout, stderr)
	}
	var muted struct {
		Success    bool   `json:"success"`
		TriggerID  string `json:"trigger_id"`
		Muted      bool   `json:"muted"`
		MutedUntil string `json:"muted_until"`
	}
	if err := json.Unmarshal([]byte(stdout), &muted); err != nil || !muted.Success || !muted.Muted || muted.TriggerID != "job:poller" {
		t.Fatalf("mute JSON: %v %s", err, stdout)
	}
	until, err := time.Parse(time.RFC3339, muted.MutedUntil)
	if err != nil || until.Sub(time.Now()) < 110*time.Minute || until.Sub(time.Now()) > 121*time.Minute {
		t.Fatalf("muted_until = %q", muted.MutedUntil)
	}

	stdout, stderr, code = runAgentDeck(t, home, "session", "send", id, "second", "--source", "poller", "--json")
	if code != 0 {
		t.Fatalf("muted send: exit %d, want 0\n%s\n%s", code, stdout, stderr)
	}
	var res struct {
		Success    bool   `json:"success"`
		Muted      bool   `json:"muted"`
		MutedUntil string `json:"muted_until"`
		TriggerID  string `json:"trigger_id"`
		Delivery   string `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || !res.Success || !res.Muted || res.TriggerID != "job:poller" || res.MutedUntil == "" || res.Delivery != "" {
		t.Fatalf("muted send JSON: %v %s", err, stdout)
	}
	// A waiting caller (the chat bridge's --wait --json) gets a complete
	// reply naming the mute, not a failure (review of #2738).
	stdout, stderr, code = runAgentDeck(t, home, "session", "send", id, "second", "--source", "poller", "--wait", "--json")
	if code != 0 {
		t.Fatalf("muted --wait send: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var waited struct {
		Success    bool   `json:"success"`
		Muted      bool   `json:"muted"`
		Completion string `json:"completion"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(stdout), &waited); err != nil || !waited.Success || !waited.Muted || waited.Completion != "complete" || !strings.Contains(waited.Content, "muted") {
		t.Fatalf("muted --wait JSON: %v %s", err, stdout)
	}
	fires := listFires(t, home, "job:poller")
	if len(fires.Fires) != 3 || fires.Fires[0].Outcome != "muted" || fires.Fires[1].Outcome != "muted" || fires.Fires[1].Line != "second" || fires.Fires[2].Outcome != "failed" {
		t.Fatalf("fires after a muted send: %+v", fires)
	}
	doc := listTriggers(t, home)
	if len(doc.Triggers) != 1 || !doc.Triggers[0].Muted || doc.Triggers[0].MutedUntil == nil || doc.Triggers[0].LastOutcome != "muted" {
		t.Fatalf("muted row: %+v", doc.Triggers)
	}

	stdout, _, code = runAgentDeck(t, home, "triggers", "unmute", "job:poller", "--json")
	if code != 0 || !strings.Contains(stdout, `"muted": false`) {
		t.Fatalf("unmute: exit %d %s", code, stdout)
	}
	if _, _, code := runAgentDeck(t, home, "session", "send", id, "third", "--source", "poller"); code != 1 {
		t.Fatalf("send after unmute must try delivery again: exit %d", code)
	}
	if doc := listTriggers(t, home); doc.Triggers[0].Muted || doc.Triggers[0].MutedUntil != nil || doc.Triggers[0].LastOutcome != "failed" {
		t.Fatalf("row after unmute: %+v", doc.Triggers[0])
	}
}

// `triggers scan` finds a systemd user unit under HOME that calls
// agent-deck; `run` refuses a trigger that is not a scanned unit.
func TestTriggersScanAndRunRefusal(t *testing.T) {
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "nightly.service"), []byte("[Service]\nExecStart=/usr/bin/agent-deck session send gone-title \"tick\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "triggers", "scan", "--json")
	if code != 0 {
		t.Fatalf("scan: exit %d %s %s", code, stdout, stderr)
	}
	var doc triggersDoc
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Triggers) != 1 {
		t.Fatalf("scan JSON: %v %s", err, stdout)
	}
	row := doc.Triggers[0]
	if row.ID != "systemd:nightly" || row.Origin != "scan" || !row.Runnable || row.Health != "dead-target" || row.ConfigPath != filepath.Join(unitDir, "nightly.service") || row.LastFire != nil {
		t.Fatalf("scanned row: %+v", row)
	}

	if _, _, code := runAgentDeck(t, home, "session", "send", "gone-title", "x", "--source", "cli-job"); code != 2 {
		t.Fatalf("send: exit %d", code)
	}
	stdout, _, code = runAgentDeck(t, home, "triggers", "run", "job:cli-job", "--json")
	if code != 1 || !strings.Contains(stdout, "cannot be run") {
		t.Fatalf("run of a push-only trigger: exit %d %s", code, stdout)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "run"); code != 2 {
		t.Fatalf("run without an id: exit %d, want 2", code)
	}
	if _, _, code := runAgentDeck(t, home, "triggers", "bogus"); code != 2 {
		t.Fatalf("unknown subcommand: exit %d, want 2", code)
	}
	stdout, _, code = runAgentDeck(t, home, "session", "send", "--help")
	if code != 0 || !strings.Contains(stdout, "-source") {
		t.Fatalf("send --help lacks --source: %s", stdout)
	}
}

func openRunTestDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// `triggers run` executes only the start argv of a scanned unit through the
// injectable runner, and never anything that pauses or stops it.
func TestTriggersRunUsesRunner(t *testing.T) {
	db := openRunTestDB(t)
	if err := db.ReplaceScannedTriggers([]statedb.TriggerRow{
		{ID: "systemd:nightly", Kind: "systemd", Source: "nightly", Unit: "nightly"},
		{ID: "launchd:com.x.poll", Kind: "launchd", Source: "com.x.poll", Unit: "com.x.poll"},
	}); err != nil {
		t.Fatal(err)
	}
	var ran [][]string
	fake := func(argv []string) ([]byte, error) {
		ran = append(ran, argv)
		return []byte("started\n"), nil
	}
	resolve := triggers.NewResolver(nil)
	data, err := runTrigger(db, resolve, "systemd:nightly", 1000, fake)
	if err != nil || strings.Join(ran[0], " ") != "systemctl --user start nightly.service" || data["output"] != "started" || data["success"] != true {
		t.Fatalf("systemd run: %v %v %v", ran, data, err)
	}
	if _, err := runTrigger(db, resolve, "launchd:com.x.poll", 501, fake); err != nil || strings.Join(ran[1], " ") != "launchctl kickstart gui/501/com.x.poll" {
		t.Fatalf("launchd run: %v %v", ran, err)
	}
	for _, argv := range ran {
		for _, a := range argv {
			for _, bad := range []string{"stop", "disable", "bootout", "unload", "kill"} {
				if a == bad {
					t.Fatalf("run issued %q: %v", bad, argv)
				}
			}
		}
	}
	failing := func(argv []string) ([]byte, error) { return []byte("unit not found"), errors.New("exit status 5") }
	data, err = runTrigger(db, resolve, "systemd:nightly", 1000, failing)
	if err == nil || data["success"] != false || data["output"] != "unit not found" {
		t.Fatalf("failed run: %v %v", data, err)
	}
	if _, err := runTrigger(db, resolve, "systemd:missing", 1000, fake); err == nil {
		t.Fatal("run of an unknown trigger succeeded")
	}
}

// fakeTriggersRemote answers one forwarded command per remote.
type fakeTriggersRemote struct {
	out  string
	err  error
	args *[]string
}

func (f fakeTriggersRemote) Run(_ context.Context, args ...string) ([]byte, error) {
	if f.args != nil {
		*f.args = args
	}
	return []byte(f.out), f.err
}

// --host all merges every remote; a remote that cannot say (an old binary
// that exits 2 on the unknown command, non-JSON, an unreachable host, a
// JSON error) is "unknown" with no rows, never guessed.
func TestTriggersRemoteMergeAndUnknown(t *testing.T) {
	okJSON := `{"triggers":[{"id":"systemd:agentdeck-overnight","host":"g14-real-hostname","kind":"systemd","source":"agentdeck-overnight",
		"target":{"as_written":"overnight-orchestrator","by":"title","session_id":"","title":"","resolved":false},
		"origin":"push+scan","inferred":false,"fires_24h":288,"noop_share_24h":1,"health":"dead-target","muted":false,"paused":false,"runnable":true,"future_field":1}],
		"hosts":{"g14-real-hostname":"ok"},"count":1,"generated_at":"2026-10-18T09:00:00Z"}`
	var forwarded []string
	remotes := map[string]fakeTriggersRemote{
		"g14":      {out: okJSON, args: &forwarded},
		"old":      {out: "", err: errors.New("exit status 2")},
		"garbled":  {out: "Unknown command: triggers\nUsage: agent-deck ..."},
		"offline":  {err: errors.New("ssh: connect to host: Connection refused")},
		"errjson":  {out: `{"success":false,"error":"boom","code":"INVALID_OPERATION"}`},
		"emptyset": {out: `{"triggers":[],"hosts":{},"count":0,"generated_at":"2026-10-18T09:00:00Z"}`},
	}
	orig := newTriggersRemoteRunner
	t.Cleanup(func() { newTriggersRemoteRunner = orig })
	newTriggersRemoteRunner = func(name string, _ session.RemoteConfig) triggersRemoteRunner { return remotes[name] }

	cfgs := map[string]session.RemoteConfig{}
	for name := range remotes {
		cfgs[name] = session.RemoteConfig{Host: name + "@remote.invalid"}
	}
	opts := triggersListOptions{kind: "systemd", health: "problems", all: true}
	rows, hosts := collectRemoteTriggers(cfgs, opts.filterArgs())
	want := map[string]string{"g14": "ok", "old": "unknown", "garbled": "unknown", "offline": "unknown", "errjson": "unknown", "emptyset": "ok"}
	for name, state := range want {
		if hosts[name] != state {
			t.Errorf("hosts[%s] = %q, want %q", name, hosts[name], state)
		}
	}
	if len(rows) != 1 || rows[0].ID != "systemd:agentdeck-overnight" || rows[0].Host != "g14" || rows[0].Health != "dead-target" || rows[0].Fires24h != 288 {
		t.Fatalf("merged rows: %+v", rows)
	}
	if got := strings.Join(forwarded, " "); got != "triggers --json --kind systemd --health problems --all" {
		t.Fatalf("forwarded args = %q", got)
	}

	// With --session, a remote without that session answers NOT_FOUND
	// (exit 2 with JSON): it is "ok" with no rows, unlike an old remote.
	remotes["nosession"] = fakeTriggersRemote{out: `{"success":false,"error":"session not found","code":"NOT_FOUND"}`, err: errors.New("exit status 2")}
	_, hosts = collectRemoteTriggers(map[string]session.RemoteConfig{"nosession": {}, "old": {}}, []string{"--session", "inbox"})
	if hosts["nosession"] != "ok" || hosts["old"] != "unknown" {
		t.Fatalf("--session hosts = %v", hosts)
	}

	table := renderTriggersTable(triggersListJSON{Triggers: rows, Hosts: hosts}, "", time.Now())
	if !strings.Contains(table, "host old: unknown") || !strings.Contains(table, "overnight-orchestrator ✗") || !strings.Contains(table, "100%") {
		t.Fatalf("table:\n%s", table)
	}
}

func TestRemoteTriggersAllowlist(t *testing.T) {
	ok := [][]string{
		{"triggers", "--json"},
		{"triggers", "list", "--json", "--all"},
		{"triggers", "--kind", "systemd", "--health=problems", "--session", "inbox", "--json"},
		{"triggers", "--help"},
	}
	for _, args := range ok {
		if _, err := remoteCommandArgs(args); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
	}
	refused := [][]string{
		{"triggers", "mute", "systemd:x"},
		{"triggers", "unmute", "systemd:x"},
		{"triggers", "run", "systemd:x"},
		{"triggers", "scan"},
		{"triggers", "fires", "systemd:x"},
		{"triggers", "--host", "all", "--json"},
		{"triggers", "--json=false"},
		{"triggers", "--kind"},
		{"triggers", "--bogus"},
	}
	for _, args := range refused {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
