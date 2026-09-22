package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestConductorTeardownReportsHeartbeatOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := session.SaveConductorMeta(&session.ConductorMeta{
		Name: "ops", Profile: "default", HeartbeatEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if out, stderr, code := runAgentDeck(t, home, "conductor", "teardown", "ops", "--json"); code != 0 {
		t.Fatalf("teardown exited %d: %s %s", code, out, stderr)
	}
	out, stderr, code := runAgentDeck(t, home, "conductor", "status", "ops", "--json")
	if code != 0 {
		t.Fatalf("status exited %d: %s %s", code, out, stderr)
	}
	var status struct {
		Conductors []struct {
			Heartbeat bool `json:"heartbeat"`
		} `json:"conductors"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, out)
	}
	if len(status.Conductors) != 1 || status.Conductors[0].Heartbeat {
		t.Fatalf("status after teardown must report heartbeat off: %s", out)
	}
	meta, err := session.LoadConductorMeta("ops")
	if err != nil || meta.HeartbeatEnabled {
		t.Fatalf("persisted heartbeat flag after teardown: meta=%+v err=%v", meta, err)
	}
}

func TestConductorHeartbeatTickCLIReadsInboxAndRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runAgentDeck(t, home, "add", "-t", "conductor-ops", "-c", "claude", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add conductor: %d %s %s", code, out, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add response: %v %s", err, out)
	}
	inbox := session.InboxPathFor(added.ID)
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(home, "rules.md")
	if err := os.WriteFile(rules, []byte("rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || !strings.Contains(out, "Inbox: 1 pending") || !strings.Contains(out, "Read heartbeat rules from "+rules) {
		t.Fatalf("first tick: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops", "--rules", rules)
	if code != 0 || out != "" {
		t.Fatalf("unchanged tick: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
