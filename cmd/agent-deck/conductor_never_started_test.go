package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A conductor that was only set up (its session stored as idle, no tmux pane
// ever created) must not be reported as running by `conductor status` or
// `conductor list`, in text or in --json.
func setupNeverStartedConductor(t *testing.T) {
	t.Helper()
	cliInboxTestHome(t)
	if err := session.SaveConductorMeta(&session.ConductorMeta{
		Name: "ghost", Profile: "default", HeartbeatEnabled: false, CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("meta: %v", err)
	}
	storage, err := session.NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	inst := session.NewInstance(session.ConductorSessionTitle("ghost"), t.TempDir())
	inst.Status = session.StatusIdle
	inst.IsConductor = true
	instances := []*session.Instance{inst}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func TestConductorStatus_NeverStartedIsNotRunning(t *testing.T) {
	setupNeverStartedConductor(t)

	out := captureStdout(t, func() { handleConductorStatus("default", []string{"ghost", "--json"}) })
	var parsed struct {
		Conductors []struct {
			Running bool `json:"running"`
		} `json:"conductors"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("bad json %q: %v", out, err)
	}
	if len(parsed.Conductors) != 1 || parsed.Conductors[0].Running {
		t.Fatalf("never-started conductor reported running: %s", out)
	}

	text := captureStdout(t, func() { handleConductorStatus("default", []string{"ghost"}) })
	if strings.Contains(text, "(running)") {
		t.Fatalf("text status says running for a never-started conductor:\n%s", text)
	}
}

func TestConductorList_NeverStartedIsNotRunning(t *testing.T) {
	setupNeverStartedConductor(t)
	text := captureStdout(t, func() { handleConductorList("default", nil) })
	if !strings.Contains(text, "ghost") || strings.Contains(text, "running") {
		t.Fatalf("list says running for a never-started conductor:\n%s", text)
	}
}
