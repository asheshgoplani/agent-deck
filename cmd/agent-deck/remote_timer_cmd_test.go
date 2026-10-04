package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// stubTimerRemote answers like a remote agent-deck would: a current one in
// JSON, an older one (before #2472) through session.ParseRemoteTimerStatus
// on its text answer, so the backward-compat path is the real parser.
type stubTimerRemote struct {
	version string
	found   bool
	// statusOut/statusErr are what `update --timer-status --json` prints.
	statusOut  string
	statusErr  error
	install    session.RemoteTimerInstall
	installErr error
	installs   int
}

func (s *stubTimerRemote) CheckBinary(context.Context) (string, bool) { return s.version, s.found }
func (s *stubTimerRemote) FetchTimerStatus(context.Context) update.TimerStatus {
	return session.ParseRemoteTimerStatus([]byte(s.statusOut), s.statusErr)
}
func (s *stubTimerRemote) InstallUpdateTimer(_ context.Context, ensureOnly bool) (session.RemoteTimerInstall, error) {
	if ensureOnly {
		return session.RemoteTimerInstall{}, errors.New("remote update --install-timer is the explicit install")
	}
	s.installs++
	return s.install, s.installErr
}

const legacyStatusJSON = `{"installed":true,"kind":"systemd-legacy","path":"/home/u/.config/systemd/user/agentdeck-autoupdate.timer","active":true,"legacy_unit":"agentdeck-autoupdate.timer","last_run":"2026-10-03T00:01:44Z","next_run":"2026-10-04T00:00:00Z"}`

// remote list --check --json carries each remote's timer; an older remote
// degrades to kind unknown and the command still succeeds.
func TestRemoteListCheckJSON_TimerPerRemote(t *testing.T) {
	setupRemoteListJSONTest(t)
	stubs := map[string]*stubTimerRemote{
		"lab": {version: "1.16.25", found: true, statusOut: legacyStatusJSON},
	}
	prev := remoteProbeRunner
	remoteProbeRunner = func(name string, _ session.RemoteConfig) remoteProber { return stubs[name] }
	t.Cleanup(func() { remoteProbeRunner = prev })

	type row struct {
		Name           string              `json:"name"`
		Timer          *update.TimerStatus `json:"timer"`
		TimerCheckedAt string              `json:"timer_checked_at"`
	}
	read := func() row {
		t.Helper()
		out := captureStdout(t, func() { handleRemoteList([]string{"--check", "--json"}) })
		var rows []row
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if len(rows) != 1 || rows[0].Timer == nil {
			t.Fatalf("rows = %+v", rows)
		}
		return rows[0]
	}

	got := read()
	if got.Timer.Kind != "systemd-legacy" || !got.Timer.Installed || !got.Timer.Active || got.Timer.LastRun == "" || got.Timer.NextRun == "" || got.TimerCheckedAt == "" {
		t.Fatalf("timer = %+v", got.Timer)
	}

	// An older remote prints text for --timer-status.
	stubs["lab"] = &stubTimerRemote{version: "1.16.20", found: true, statusOut: "Update timer: not installed (run `agent-deck update --install-timer`)\n"}
	if got = read(); got.Timer.Kind != "unknown" {
		t.Fatalf("old remote timer = %+v, want kind unknown", got.Timer)
	}
	// An unreachable remote is unknown too, never "none".
	stubs["lab"] = &stubTimerRemote{found: false}
	if got = read(); got.Timer.Kind != "unknown" {
		t.Fatalf("unreachable remote timer = %+v", got.Timer)
	}

	// Without --check the cached reading is shown.
	out := captureStdout(t, func() { handleRemoteList([]string{"--json"}) })
	if !strings.Contains(out, `"kind": "unknown"`) {
		t.Fatalf("cached list = %s", out)
	}
	text := captureStdout(t, func() { handleRemoteList(nil) })
	if !strings.Contains(text, "timer unknown") {
		t.Fatalf("text list = %s", text)
	}
}

// remote update --install-timer runs each remote's own install, reads the
// timer back and caches it; an older remote that answers in text is
// reported, not failed; a remote that fails is counted.
func TestRemoteUpdateInstallTimer(t *testing.T) {
	setupRemoteListJSONTest(t)
	stubs := map[string]*stubTimerRemote{
		"new": {statusOut: `{"installed":true,"kind":"systemd","active":true}`, install: session.RemoteTimerInstall{TimerEnsureResult: update.TimerEnsureResult{Action: update.TimerActionMigrated, Migrated: update.LegacySystemdTimerTimer}}},
		"old": {statusOut: "Update timer: active (systemd)\n", install: session.RemoteTimerInstall{Legacy: true, Output: "✓ Update timer installed (systemd): /home/u/.config/systemd/user/agent-deck-autoupdate.timer"}},
		"bad": {statusErr: errors.New("ssh: connect to host x port 22: Operation timed out"), installErr: errors.New("ssh: connect to host x port 22: Operation timed out")},
	}
	prev := remoteTimerRunner
	remoteTimerRunner = func(name string, _ session.RemoteConfig) session.RemoteTimerManager { return stubs[name] }
	t.Cleanup(func() { remoteTimerRunner = prev })

	remotes := map[string]session.RemoteConfig{"new": {Host: "u@new"}, "old": {Host: "u@old"}, "bad": {Host: "u@bad"}}
	results := runRemoteTimerInstall(context.Background(), remotes)
	if len(results) != 3 || results[0].Name != "bad" || results[1].Name != "new" || results[2].Name != "old" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].OK || !strings.Contains(results[0].Error, "Operation timed out") || results[0].Timer.Kind != "unknown" {
		t.Fatalf("bad = %+v", results[0])
	}
	if !results[1].OK || results[1].Action != "migrated" || results[1].Timer.Kind != "systemd" {
		t.Fatalf("new = %+v", results[1])
	}
	if !results[2].OK || !strings.Contains(results[2].Summary, "predates timer migration") || results[2].Timer.Kind != "unknown" {
		t.Fatalf("old = %+v", results[2])
	}
	for name, s := range stubs {
		if s.installs != 1 {
			t.Fatalf("%s: installs = %d", name, s.installs)
		}
	}
	if st := session.LoadRemoteVersions()["new"].Timer; st == nil || st.Kind != "systemd" {
		t.Fatalf("cached timer for new = %+v", st)
	}

	var text bytes.Buffer
	if failed := printRemoteTimerInstall(&text, results, false); failed != 1 {
		t.Fatalf("failed = %d", failed)
	}
	if !strings.Contains(text.String(), "new: migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer; now timer active (systemd)") {
		t.Fatalf("text = %s", text.String())
	}
	var js bytes.Buffer
	printRemoteTimerInstall(&js, results, true)
	var rows []map[string]any
	if err := json.Unmarshal(js.Bytes(), &rows); err != nil || len(rows) != 3 || rows[1]["timer"].(map[string]any)["kind"] != "systemd" {
		t.Fatalf("json = %s (%v)", js.String(), err)
	}
}
