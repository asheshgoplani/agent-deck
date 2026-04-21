package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRevive_CLI_AllFlag_TriggersReviver(t *testing.T) {
	// Build 3 instances: 1 errored-alive, 1 alive, 1 dead-server.
	// Inject a reviver with test hooks and run the CLI path that --all triggers.
	erroredTitle := "errored-alive-1"
	aliveTitle := "alive-1"
	deadTitle := "dead-1"

	errored := session.NewInstance(erroredTitle, "/tmp")
	errored.Status = session.StatusError
	alive := session.NewInstance(aliveTitle, "/tmp")
	alive.Status = session.StatusRunning
	dead := session.NewInstance(deadTitle, "/tmp")
	dead.Status = session.StatusError

	erroredTmux := errored.GetTmuxSession().Name
	aliveTmux := alive.GetTmuxSession().Name
	deadTmux := dead.GetTmuxSession().Name

	reviveCalls := 0
	rev := &session.Reviver{
		TmuxExists:   func(name, _ string) bool { return name != deadTmux },
		PipeAlive:    func(name string) bool { return name == aliveTmux },
		ReviveAction: func(i *session.Instance) error { reviveCalls++; return nil },
		Stagger:      0,
	}
	_ = erroredTmux

	summary := runReviveAll([]*session.Instance{errored, alive, dead}, rev)

	if summary.Revived != 1 {
		t.Errorf("expected 1 revived, got %d", summary.Revived)
	}
	if summary.Errored != 1 {
		t.Errorf("expected 1 errored classification, got %d", summary.Errored)
	}
	if summary.Alive != 1 {
		t.Errorf("expected 1 alive classification, got %d", summary.Alive)
	}
	if summary.Dead != 1 {
		t.Errorf("expected 1 dead classification, got %d", summary.Dead)
	}
	if reviveCalls != 1 {
		t.Errorf("expected ReviveAction called 1×, got %d", reviveCalls)
	}

	line := summary.Format()
	for _, sub := range []string{"revived=1", "errored=1", "alive=1", "dead=1"} {
		if !strings.Contains(line, sub) {
			t.Errorf("summary format %q missing %q", line, sub)
		}
	}
}

func TestRevive_CLI_EmptyStorage_NoCalls(t *testing.T) {
	reviveCalls := 0
	rev := &session.Reviver{
		TmuxExists:   func(name, _ string) bool { return false },
		PipeAlive:    func(name string) bool { return false },
		ReviveAction: func(i *session.Instance) error { reviveCalls++; return nil },
		Stagger:      0,
	}
	summary := runReviveAll(nil, rev)
	if summary.Revived != 0 || summary.Alive != 0 || summary.Errored != 0 || summary.Dead != 0 {
		t.Errorf("empty input must produce zero counts: %+v", summary)
	}
	if reviveCalls != 0 {
		t.Errorf("empty input must not trigger ReviveAction")
	}
}
