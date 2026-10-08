package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T, conductor bool) (*Instance, string) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)
	home := os.Getenv("HOME")
	capture := filepath.Join(home, "input")
	script := filepath.Join(home, "claude")
	body := "#!/bin/sh\nprintf '\\033[2J\\033[H❯ '\nwhile IFS= read -r line; do printf '%s\\n' \"$line\" >> '" + capture + "'; printf '\\033[2J\\033[H❯ '; done\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("recovery-test", home, "claude")
	inst.Command = script
	inst.IsConductor = conductor
	t.Cleanup(func() {
		if inst.tmuxSession != nil {
			if t.Failed() {
				pane, err := inst.tmuxSession.CapturePaneFresh()
				t.Logf("pane=%q err=%v command=%s", pane, err, inst.Command)
			}
			_ = inst.tmuxSession.Kill()
		}
	})
	return inst, capture
}

func assertRecoveryTurns(t *testing.T, capture string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var text string
	for {
		b, _ := os.ReadFile(capture)
		text = string(b)
		if strings.Count(text, "[CONDUCTOR RECOVERY]") == want || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := strings.Count(text, "[CONDUCTOR RECOVERY]"); got != want {
		t.Fatalf("recovery turns = %d, want %d; captured %q", got, want, text)
	}
	if want > 0 {
		for _, term := range []string{"inbox drain self", "state.json", "live", "external", "already-authorized", "completed", "blockers"} {
			if !strings.Contains(text, term) {
				t.Errorf("recovery turn lacks %q: %s", term, text)
			}
		}
	}
}

func TestConductorRecoveryLifecycle(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 1)
	// A known conversation uses respawn-pane; a missing one recreates tmux.
	if err := inst.Restart(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 2)
	if err := inst.tmuxSession.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Restart(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 3)
}

func TestConductorRecoveryOrdinarySession(t *testing.T) {
	inst, capture := recoveryFixture(t, false)
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Restart(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 0)
}

func TestConductorRecoverySuppressedSpawn(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	old := instanceSpawnLockAcquireFn
	instanceSpawnLockAcquireFn = func(id string) (func(), error) {
		recordInstanceSpawn(id) // Another caller won while this one waited.
		return func() {}, nil
	}
	t.Cleanup(func() { instanceSpawnLockAcquireFn = old })
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Restart(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 0)
}

func TestConductorRecoveryDraftDoesNotFailSpawn(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	b, err := os.ReadFile(inst.Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.Command, []byte(strings.ReplaceAll(string(b), "❯ ", "❯ operator draft")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatalf("successful process spawn must survive recovery refusal: %v", err)
	}
	if inst.LastStartedAt.IsZero() || !inst.Exists() {
		t.Fatal("successful spawn lost its lifecycle state")
	}
	assertRecoveryTurns(t, capture, 0)
	if warning := inst.ConductorRecoveryWarning(); !strings.Contains(warning, "not sent") {
		t.Fatalf("missing refusal warning: %q", warning)
	}
}

func TestConductorRecoveryDroppedEnterIsUncertain(t *testing.T) {
	inst, capture := recoveryFixture(t, false)
	body := "#!/bin/sh\nprintf '\\033[2J\\033[H❯ '\nwhile IFS= read -r line; do printf 'enter\\n' >> '" + capture + "'; printf '\\033[2J\\033[H❯ %s' \"$line\"; done\n"
	if err := os.WriteFile(inst.Command, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	inst.IsConductor = true
	if err := inst.wakeConductorAfterSpawn(); err == nil {
		t.Fatal("unconsumed recovery draft was falsely accepted")
	}
	b, err := os.ReadFile(capture)
	if err != nil || string(b) != "enter\n" {
		t.Fatalf("expected one Enter without retries, got %q (%v)", b, err)
	}
}

func TestConductorRecoveryMenuNeverReceivesInput(t *testing.T) {
	inst, capture := recoveryFixture(t, false)
	b, err := os.ReadFile(inst.Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.Command, []byte(strings.ReplaceAll(string(b), "❯ ", "❯ \\nEnter to select")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	inst.IsConductor = true
	if err := inst.wakeConductorAfterSpawn(); err == nil {
		t.Error("interactive menu authorized recovery input")
	}
	assertRecoveryTurns(t, capture, 0)
}

func TestConductorRecoveryExplicitMessage(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	// A regular explicit initial message owns the one initial turn. Put its
	// prompt in the existing launch helper's bottom-of-pane scan window.
	script, err := os.ReadFile(inst.Command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inst.Command, []byte(strings.ReplaceAll(string(script), "\\033[H", "\\033[40;1H")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.StartWithMessage("operator task"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "operator task" {
		t.Fatalf("explicit message changed or duplicated: %q", b)
	}
	assertRecoveryTurns(t, capture, 0)
}

func TestConductorRecoverySlowStartup(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	b, err := os.ReadFile(inst.Command)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), "#!/bin/sh\n", "#!/bin/sh\nsleep 21\n", 1))
	if err := os.WriteFile(inst.Command, b, 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 1)
}

func TestConductorRecoveryFreshFrameGuards(t *testing.T) {
	for _, tc := range []struct {
		name, tool, pane string
		safe             bool
	}{
		{"claude empty", "claude", "❯ ", true},
		{"claude draft", "claude", "❯ operator draft", false},
		{"claude busy overrides prompt", "claude", "✳ Working… (esc to interrupt)\n❯ ", false},
		{"claude menu", "claude", "❯ \nEnter to select", false},
		{"codex empty", "codex", "› ", true},
		{"codex draft", "codex", "› operator draft", false},
		{"codex legacy unscopable", "codex", "codex> operator draft", false},
		{"codex approval", "codex", "Continue?", false},
		{"codex busy", "codex", "• Working (2s • esc to interrupt)\n› ", false},
		{"blank", "claude", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := conductorRecoveryPromptSafe(tc.tool, tc.pane); got != tc.safe {
				t.Fatalf("safe=%v, want %v", got, tc.safe)
			}
		})
	}
}

func TestConductorRecoveryEmptyMessageAndDurableState(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	state := filepath.Join(inst.ProjectPath, "state.json")
	saved := []byte(`{"completed":["already-delivered"],"running":["existing-child"],"pending":["authorized-task"]}`)
	if err := os.WriteFile(state, saved, 0600); err != nil {
		t.Fatal(err)
	}
	if err := inst.StartWithMessage(""); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 1)
	got, err := os.ReadFile(state)
	if err != nil || string(got) != string(saved) {
		t.Fatalf("lifecycle rewrote durable work: %q, %v", got, err)
	}
	if err := inst.RestartWithEnv(map[string]string{"RECOVERY_FIXTURE": "1"}); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 2)
}

func TestConductorRecoverySerializesWithOtherSenders(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	lock, err := AcquireSendLock(inst.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(3 * time.Second); lock.Release(); close(released) }()
	t.Cleanup(func() { <-released })
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 1)
}

func TestConductorRecoveryTurnConfig(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name string
		s    *ConductorSettings
		want bool
	}{
		{"nil settings", nil, true},
		{"key absent", &ConductorSettings{}, true},
		{"explicit true", &ConductorSettings{RecoveryTurn: &yes}, true},
		{"explicit false", &ConductorSettings{RecoveryTurn: &no}, false},
	} {
		if got := tc.s.RecoveryTurnEnabled(); got != tc.want {
			t.Errorf("%s: RecoveryTurnEnabled=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestConductorRecoveryOptOut(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[conductor]\nrecovery_turn = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Restart(); err != nil {
		t.Fatal(err)
	}
	// Give a wrongly enabled wake time to land before asserting zero.
	time.Sleep(2 * time.Second)
	assertRecoveryTurns(t, capture, 0)
	if warning := inst.ConductorRecoveryWarning(); warning != "" {
		t.Fatalf("opt-out must not warn: %q", warning)
	}
}
