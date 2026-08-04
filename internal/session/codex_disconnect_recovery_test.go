package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testCodexDisconnectPane = "\n■ stream disconnected before completion: error sending request for url (https://chatgpt.com/backend-api/codex/responses)\n"

func TestIsCodexStreamDisconnectedPane(t *testing.T) {
	tests := []struct {
		name string
		pane string
		want bool
	}{
		{"exact rendered Codex error", testCodexDisconnectPane, true},
		{"tmux-padded rendered Codex error", testCodexDisconnectPane + strings.Repeat("\n", 40), true},
		{"ANSI rendered Codex error", "\x1b[31m■ stream disconnected before completion\x1b[0m\n(https://chatgpt.com/backend-api/codex/responses)", true},
		{"ordinary network message", "■ connection lost; retry later\n", false},
		{"quoted user text", "› ■ stream disconnected before completion: https://chatgpt.com/backend-api/codex/responses", false},
		{"missing endpoint", "■ stream disconnected before completion", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCodexStreamDisconnectedPane(tt.pane); got != tt.want {
				t.Fatalf("IsCodexStreamDisconnectedPane() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexDisconnectRecovery_ConfirmsThenRestarts(t *testing.T) {
	inst := newResumableCodexDisconnectInstance(t)
	pane := testCodexDisconnectPane
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	restarts := 0
	recovery := NewCodexDisconnectRecovery()
	recovery.now = func() time.Time { return now }
	recovery.capture = func(*Instance) (string, error) { return pane, nil }
	recovery.restart = func(*Instance) error { restarts++; return nil }

	first := recovery.Scan([]*Instance{inst})
	if len(first) != 1 || first[0].Action != CodexDisconnectRecoveryPending {
		t.Fatalf("first scan = %#v, want one pending outcome", first)
	}
	if restarts != 0 {
		t.Fatalf("restart before confirmation = %d, want 0", restarts)
	}

	second := recovery.Scan([]*Instance{inst})
	if len(second) != 1 || second[0].Action != CodexDisconnectRecoveryRestarted {
		t.Fatalf("second scan = %#v, want one restarted outcome", second)
	}
	if restarts != 1 {
		t.Fatalf("restart count = %d, want 1", restarts)
	}
}

func TestCodexDisconnectRecovery_EnforcesRollingAttemptCap(t *testing.T) {
	inst := newResumableCodexDisconnectInstance(t)
	pane := testCodexDisconnectPane
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	restarts := 0
	recovery := NewCodexDisconnectRecovery()
	recovery.now = func() time.Time { return now }
	recovery.capture = func(*Instance) (string, error) { return pane, nil }
	recovery.restart = func(*Instance) error { restarts++; return nil }

	for attempt := 0; attempt < 2; attempt++ {
		recovery.Scan([]*Instance{inst})
		outcomes := recovery.Scan([]*Instance{inst})
		if len(outcomes) != 1 || outcomes[0].Action != CodexDisconnectRecoveryRestarted {
			t.Fatalf("attempt %d outcome = %#v, want restarted", attempt+1, outcomes)
		}
		pane = "Codex is ready"
		recovery.Scan([]*Instance{inst})
		pane = testCodexDisconnectPane
	}

	recovery.Scan([]*Instance{inst})
	capped := recovery.Scan([]*Instance{inst})
	if len(capped) != 1 || capped[0].Action != CodexDisconnectRecoveryCapped {
		t.Fatalf("third incident = %#v, want capped outcome", capped)
	}
	if restarts != 2 {
		t.Fatalf("restart count = %d, want cap of 2", restarts)
	}
}

func TestCodexDisconnectRecovery_SkipsActiveSession(t *testing.T) {
	inst := newResumableCodexDisconnectInstance(t)
	inst.Status = StatusRunning
	captures := 0
	recovery := NewCodexDisconnectRecovery()
	recovery.capture = func(*Instance) (string, error) {
		captures++
		return testCodexDisconnectPane, nil
	}

	if outcomes := recovery.Scan([]*Instance{inst}); len(outcomes) != 0 {
		t.Fatalf("active session outcomes = %#v, want none", outcomes)
	}
	if captures != 0 {
		t.Fatalf("active session captures = %d, want 0", captures)
	}
}

func TestCodexDisconnectRecovery_E2EResumesExistingCodexSession(t *testing.T) {
	skipIfNoTmuxBinary(t)

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sid := "22222222-3333-4444-5555-666666666666"
	seedCodexDisconnectRollout(t, codexHome, sid)

	statePath := filepath.Join(t.TempDir(), "started")
	fakeBin := t.TempDir()
	fakeCodex := filepath.Join(fakeBin, "codex")
	script := fmt.Sprintf(`#!/bin/sh
if [ -f %q ]; then
  printf 'RESUMED:%%s\n' "$*"
else
  : > %q
  printf '■ stream disconnected before completion: error sending request for url (https://chatgpt.com/backend-api/codex/responses)\n'
fi
while :; do sleep 1; done
`, statePath, statePath)
	if err := os.WriteFile(fakeCodex, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := NewInstanceWithTool("disconnect recovery e2e", t.TempDir(), "codex")
	inst.Command = "codex"
	inst.CodexSessionID = sid
	if err := inst.Start(); err != nil {
		t.Fatalf("start fake Codex: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	waitForCodexDisconnectPane(t, inst, "■ stream disconnected before completion")
	inst.Status = StatusError

	recovery := NewCodexDisconnectRecovery()
	if outcomes := recovery.Scan([]*Instance{inst}); len(outcomes) != 1 || outcomes[0].Action != CodexDisconnectRecoveryPending {
		t.Fatalf("first recovery scan = %#v, want pending confirmation", outcomes)
	}
	if outcomes := recovery.Scan([]*Instance{inst}); len(outcomes) != 1 || outcomes[0].Action != CodexDisconnectRecoveryRestarted {
		t.Fatalf("second recovery scan = %#v, want restarted", outcomes)
	}
	waitForCodexDisconnectPane(t, inst, "RESUMED:resume "+sid)
}

func newResumableCodexDisconnectInstance(t *testing.T) *Instance {
	t.Helper()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sid := "11111111-2222-3333-4444-555555555555"
	seedCodexDisconnectRollout(t, codexHome, sid)
	inst := NewInstanceWithTool("recoverable Codex", t.TempDir(), "codex")
	inst.CodexSessionID = sid
	inst.Status = StatusError
	return inst
}

func seedCodexDisconnectRollout(t *testing.T, codexHome, sid string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "08", "04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout directory: %v", err)
	}
	rollout := filepath.Join(dir, fmt.Sprintf("rollout-20260804T000000-%s.jsonl", sid))
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}

func waitForCodexDisconnectPane(t *testing.T, inst *Instance, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pane, err := inst.GetTmuxSession().CapturePaneFresh()
		if err == nil && strings.Contains(pane, want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	pane, err := inst.GetTmuxSession().CapturePaneFresh()
	t.Fatalf("pane never contained %q; pane=%q err=%v", want, pane, err)
}
