package main

import (
	"encoding/json"
	"path/filepath"
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
