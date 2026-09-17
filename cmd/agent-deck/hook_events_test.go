package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// hookEventsHome isolates HOME so the hooks dir and config.toml are the
// test's own; the user-config cache is cleared so the kill-switch test's
// config.toml is read.
func hookEventsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

func readHookEvents(t *testing.T, path string) []hookEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	var events []hookEvent
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var ev hookEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("history line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

// Every status-file write leaves one history line beside it, carrying what
// the status file cannot (a millisecond timestamp, the writing pid) and
// what it can (event, status, session id), and the status file itself is
// unchanged by the addition.
func TestHookEventHistory_WrittenBesideStatusFile(t *testing.T) {
	hookEventsHome(t)
	const id = "evt-instance-1"
	before := time.Now().UTC().Add(-time.Second)
	writeHookStatus(id, "running", "sess-1", "UserPromptSubmit", "")
	writeHookStatus(id, "waiting", "sess-1", "Stop", "")

	sf, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
	if err != nil {
		t.Fatalf("status file: %v", err)
	}
	var last hookStatusFile
	if err := json.Unmarshal(sf, &last); err != nil || last.Event != "Stop" || last.Status != "waiting" {
		t.Fatalf("status file = %s (%v), want the Stop", sf, err)
	}

	events := readHookEvents(t, hookEventHistoryPath(getHooksDir(), id))
	if len(events) != 2 {
		t.Fatalf("history = %d events, want 2: %+v", len(events), events)
	}
	want := []struct{ event, status string }{{"UserPromptSubmit", "running"}, {"Stop", "waiting"}}
	for i, w := range want {
		ev := events[i]
		if ev.Event != w.event || ev.Status != w.status || ev.SessionID != "sess-1" || ev.PID != os.Getpid() {
			t.Fatalf("event %d = %+v, want %s/%s sess-1 pid %d", i, ev, w.event, w.status, os.Getpid())
		}
		at, err := time.Parse(time.RFC3339Nano, ev.At)
		if err != nil || at.Before(before) || at.After(time.Now().UTC().Add(time.Second)) {
			t.Fatalf("event %d at = %q (%v), want a current RFC3339Nano stamp", i, ev.At, err)
		}
	}
}

// The history is bounded to the last hookEventHistoryMax events: past the
// cap every append rewrites the tail, keeping order and the newest events.
func TestHookEventHistory_CapKeepsLastEvents(t *testing.T) {
	hookEventsHome(t)
	const id = "evt-instance-cap"
	path := hookEventHistoryPath(getHooksDir(), id)
	total := hookEventHistoryMax*2 + 37
	for n := 1; n <= total; n++ {
		writeHookStatus(id, "running", fmt.Sprintf("sess-%d", n), "UserPromptSubmit", "")
		if n == hookEventHistoryMax {
			if got := len(readHookEvents(t, path)); got != hookEventHistoryMax {
				t.Fatalf("at the cap: %d events, want %d (no trimming yet)", got, hookEventHistoryMax)
			}
		}
	}
	events := readHookEvents(t, path)
	if len(events) != hookEventHistoryMax {
		t.Fatalf("after %d writes: %d events, want %d", total, len(events), hookEventHistoryMax)
	}
	for i, ev := range events {
		if want := fmt.Sprintf("sess-%d", total-hookEventHistoryMax+1+i); ev.SessionID != want {
			t.Fatalf("event %d session = %q, want %q (oldest dropped first, order kept)", i, ev.SessionID, want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history mode = %v, want 0600", info.Mode().Perm())
	}
	// No temp file from the rewrite is left behind.
	entries, _ := os.ReadDir(getHooksDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("rewrite left a temp file in the hooks dir: %s", e.Name())
		}
	}
}

// `[health] enabled = false` turns the history off with the health sampler;
// the status file is still written.
func TestHookEventHistory_HealthKillSwitch(t *testing.T) {
	home := hookEventsHome(t)
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[health]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	if hookEventHistoryEnabled() {
		t.Fatalf("history enabled with [health] enabled = false in %s (HOME=%s)", cfgPath, home)
	}
	const id = "evt-instance-off"
	writeHookStatus(id, "running", "sess-1", "UserPromptSubmit", "")
	if _, err := os.Stat(filepath.Join(getHooksDir(), id+".json")); err != nil {
		t.Fatalf("status file must still be written: %v", err)
	}
	if _, err := os.Stat(hookEventHistoryPath(getHooksDir(), id)); !os.IsNotExist(err) {
		t.Fatalf("history written despite the kill switch (err=%v)", err)
	}
}
