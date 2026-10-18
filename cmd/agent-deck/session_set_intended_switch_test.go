package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Intended account/model switches (limit-guard BUG 5): a conductor that
// switches a session on purpose marks it with `session set ... --intended`,
// and an external guard reads the record back from `list --json` /
// `session show --json` instead of flagging the switch UNEXPECTED.

type intendedSwitchJSON struct {
	Account   string `json:"account"`
	Model     string `json:"model"`
	Reason    string `json:"reason"`
	At        string `json:"at"`
	ExpiresAt string `json:"expires_at"`
}

// intendedSwitchHome is an isolated HOME with two Claude account slots.
func intendedSwitchHome(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	adDir := filepath.Join(home, ".agent-deck")
	var cfg strings.Builder
	for _, slot := range []string{"personal", "work"} {
		dir := filepath.Join(home, ".claude-"+slot)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&cfg, "[profiles.%s.claude]\nconfig_dir = %q\n\n", slot, dir)
	}
	if err := os.MkdirAll(adDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(cfg.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func runSetOK(t *testing.T, home string, args ...string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, append([]string{"session", "set"}, args...)...)
	if code != 0 {
		t.Fatalf("session set %v failed (exit %d)\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
	}
}

// showIntendedSwitch returns show --json's intended_switch (nil when absent)
// and the raw output.
func showIntendedSwitch(t *testing.T, home, id string) (*intendedSwitchJSON, string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		IntendedSwitch *intendedSwitchJSON `json:"intended_switch"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	return resp.IntendedSwitch, stdout
}

// listIntendedSwitch returns list --json's intended_switch for id.
func listIntendedSwitch(t *testing.T, home, id string) (*intendedSwitchJSON, string) {
	t.Helper()
	stdout := readSessionsJSON(t, home)
	var rows []struct {
		ID             string              `json:"id"`
		IntendedSwitch *intendedSwitchJSON `json:"intended_switch"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("parse list response: %v\nstdout: %s", err, stdout)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.IntendedSwitch, stdout
		}
	}
	t.Fatalf("session %s not in list --json:\n%s", id, stdout)
	return nil, ""
}

func parseRFC3339(t *testing.T, label, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("%s %q is not RFC3339: %v", label, s, err)
	}
	return ts
}

// expectIntended checks both JSON surfaces carry the same record and returns it.
func expectIntended(t *testing.T, home, id, account, model, reason string, dur time.Duration) {
	t.Helper()
	shown, raw := showIntendedSwitch(t, home, id)
	if shown == nil {
		t.Fatalf("show --json has no intended_switch:\n%s", raw)
	}
	listed, rawList := listIntendedSwitch(t, home, id)
	if listed == nil {
		t.Fatalf("list --json has no intended_switch:\n%s", rawList)
	}
	if *listed != *shown {
		t.Errorf("list intended_switch %+v != show %+v", *listed, *shown)
	}
	if shown.Account != account || shown.Model != model || shown.Reason != reason {
		t.Errorf("intended_switch = %+v, want account %q model %q reason %q", *shown, account, model, reason)
	}
	at := parseRFC3339(t, "at", shown.At)
	exp := parseRFC3339(t, "expires_at", shown.ExpiresAt)
	if got := exp.Sub(at); got != dur {
		t.Errorf("expires_at - at = %v, want %v", got, dur)
	}
	if d := time.Since(at); d < -time.Minute || d > time.Minute {
		t.Errorf("at %v is not now", at)
	}
}

func expectNoIntended(t *testing.T, home, id, when string) {
	t.Helper()
	if shown, raw := showIntendedSwitch(t, home, id); shown != nil {
		t.Errorf("%s: show --json still has intended_switch:\n%s", when, raw)
	}
	if listed, raw := listIntendedSwitch(t, home, id); listed != nil {
		t.Errorf("%s: list --json still has intended_switch:\n%s", when, raw)
	}
}

func TestSessionSetIntended_AccountSwitchRecorded(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	expectNoIntended(t, home, id, "fresh session")

	// Flags after the positionals, the way a conductor writes it.
	runSetOK(t, home, id, "account", "work", "--intended", "acme blocked, moved on purpose", "--intended-for", "2h")
	expectIntended(t, home, id, "work", "", "acme blocked, moved on purpose", 2*time.Hour)

	// The record survives an unrelated full save (persisted in tool_data,
	// not dropped or reset by a later write).
	runSetOK(t, home, id, "title", "worker renamed")
	expectIntended(t, home, id, "work", "", "acme blocked, moved on purpose", 2*time.Hour)
}

func TestSessionSetIntended_ModelSwitchDefaultExpiry(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	runSetOK(t, home, id, "account", "personal")

	runSetOK(t, home, id, "model", "opus", "--intended", "Fable weekly cap")
	expectIntended(t, home, id, "personal", "opus", "Fable weekly cap", 24*time.Hour)
}

func TestSessionSetIntended_UnmarkedSwitchClears(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")

	runSetOK(t, home, id, "account", "work", "--intended", "planned")
	expectIntended(t, home, id, "work", "", "planned", 24*time.Hour)
	runSetOK(t, home, id, "account", "personal")
	expectNoIntended(t, home, id, "after unmarked account switch")

	runSetOK(t, home, id, "model", "sonnet", "--intended", "planned model")
	expectIntended(t, home, id, "personal", "sonnet", "planned model", 24*time.Hour)
	runSetOK(t, home, id, "model", "opus")
	expectNoIntended(t, home, id, "after unmarked model switch")
}

// TestSessionSetIntended_FieldMarksCurrent covers a switch made in the TUI:
// `intended-switch <reason>` marks the current account+model, "" clears.
func TestSessionSetIntended_FieldMarksCurrent(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	runSetOK(t, home, id, "account", "work")
	runSetOK(t, home, id, "model", "opus")
	expectNoIntended(t, home, id, "before marking")

	runSetOK(t, home, id, "intended-switch", "switched in the TUI")
	expectIntended(t, home, id, "work", "opus", "switched in the TUI", 24*time.Hour)

	runSetOK(t, home, id, "intended-switch", "")
	expectNoIntended(t, home, id, "after clearing")
}

func TestSessionSetIntended_ExpiredOmitted(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")
	runSetOK(t, home, id, "account", "work", "--intended", "short", "--intended-for", "1s")
	time.Sleep(2100 * time.Millisecond)
	expectNoIntended(t, home, id, "after expiry")
}

func TestSessionSetIntended_FlagValidation(t *testing.T) {
	home := intendedSwitchHome(t)
	id := addTestSession(t, home, filepath.Join(home, "proj"), "worker")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"other field", []string{id, "title", "x", "--intended", "r"}, "--intended"},
		{"intended-switch field", []string{id, "intended-switch", "r", "--intended", "r"}, "--intended"},
		{"for without intended", []string{id, "account", "work", "--intended-for", "1h"}, "--intended-for"},
		{"empty reason", []string{id, "account", "work", "--intended", ""}, "--intended"},
		{"zero duration", []string{id, "account", "work", "--intended", "r", "--intended-for", "0s"}, "--intended-for"},
		{"negative duration", []string{id, "account", "work", "--intended", "r", "--intended-for=-1h"}, "--intended-for"},
		{"over 7d", []string{id, "account", "work", "--intended", "r", "--intended-for", "169h"}, "--intended-for"},
		{"bad duration", []string{id, "account", "work", "--intended", "r", "--intended-for", "soon"}, "--intended-for"},
	}
	for _, tc := range cases {
		stdout, stderr, code := runAgentDeck(t, home, append([]string{"session", "set"}, tc.args...)...)
		if code == 0 {
			t.Errorf("%s: exit 0, want failure\nstdout: %s\nstderr: %s", tc.name, stdout, stderr)
			continue
		}
		if !strings.Contains(stdout+stderr, tc.want) {
			t.Errorf("%s: message lacks %q\nstdout: %s\nstderr: %s", tc.name, tc.want, stdout, stderr)
		}
	}
	// A rejected call changes nothing.
	expectNoIntended(t, home, id, "after rejected calls")
	stdout, _, _ := runAgentDeck(t, home, "session", "show", id, "--json")
	var resp struct {
		Title   string `json:"title"`
		Account string `json:"account"`
	}
	_ = json.Unmarshal([]byte(stdout), &resp)
	if resp.Title != "worker" || resp.Account != "" {
		t.Errorf("rejected calls mutated the session: title %q account %q", resp.Title, resp.Account)
	}

	// 7d exactly is the inclusive maximum.
	runSetOK(t, home, id, "account", "work", "--intended", "week", "--intended-for", "168h")
	expectIntended(t, home, id, "work", "", "week", 7*24*time.Hour)
}
