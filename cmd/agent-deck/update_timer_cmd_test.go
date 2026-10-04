package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedRunner is the cmd-level fake systemctl/launchctl (#2472): it
// records every argv and answers from canned replies; anything unlisted
// succeeds with no output. No test here reaches a real init system.
type cannedRunner struct {
	calls   []string
	replies map[string]cannedReply
}

type cannedReply struct {
	out string
	err error
}

func (r *cannedRunner) Run(argv ...string) (string, error) {
	key := strings.Join(argv, " ")
	r.calls = append(r.calls, key)
	reply := r.replies[key]
	return reply.out, reply.err
}

type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitStatus) ExitCode() int { return int(e) }

func linuxTestTimerConfig(t *testing.T) update.TimerConfig {
	t.Helper()
	cfg := testTimerConfig(t)
	cfg.GOOS = "linux"
	cfg.Exe = "/home/me/.local/bin/agent-deck"
	cfg.UID = 1000
	return cfg
}

// legacyHost lays out the hand-made pair the issue found on three remotes.
func legacyHost(t *testing.T, cfg update.TimerConfig) *cannedRunner {
	t.Helper()
	require.NoError(t, os.MkdirAll(cfg.SystemdUserDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.SystemdUserDir, update.LegacySystemdTimerTimer), []byte("[Timer]\nOnCalendar=daily\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.SystemdUserDir, update.LegacySystemdTimerService), []byte("[Service]\nExecStart=agent-deck update\n"), 0o644))
	return &cannedRunner{replies: map[string]cannedReply{
		"systemctl --user is-active " + update.LegacySystemdTimerTimer: {out: "active\n"},
		"systemctl --user is-active " + update.SystemdTimerTimer:       {out: "active\n"},
	}}
}

// update --timer-status --json on a legacy-only host: systemd-legacy, never
// "not installed" (the field report of #2472).
func TestTimerStatusJSON_LegacyUnitIsReported(t *testing.T) {
	cfg := linuxTestTimerConfig(t)
	r := legacyHost(t, cfg)
	var out bytes.Buffer
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "status", timerCommandOptions{JSON: true}, &out))
	var st update.TimerStatus
	require.NoError(t, json.Unmarshal(out.Bytes(), &st))
	assert.Equal(t, "systemd-legacy", st.Kind)
	assert.True(t, st.Installed)
	assert.True(t, st.Active)
	assert.Equal(t, update.LegacySystemdTimerTimer, st.LegacyUnit)

	out.Reset()
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "status", timerCommandOptions{}, &out))
	assert.Contains(t, out.String(), "Update timer: active (systemd-legacy)")
	assert.Contains(t, out.String(), "`agent-deck update --install-timer` migrates it to agent-deck-autoupdate.timer")
	assert.NotContains(t, out.String(), "not installed")
}

// --install-timer on a legacy host migrates and prints exactly one line;
// run again it leaves the now-canonical timer alone.
func TestInstallTimer_MigratesLegacyWithOneLine(t *testing.T) {
	cfg := linuxTestTimerConfig(t)
	r := legacyHost(t, cfg)
	var out bytes.Buffer
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "install", timerCommandOptions{}, &out))
	assert.Equal(t, "migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer\n", out.String())
	assert.Contains(t, r.calls, "systemctl --user disable --now "+update.LegacySystemdTimerTimer)
	assert.FileExists(t, cfg.TimerPath())
	assert.NoFileExists(t, filepath.Join(cfg.SystemdUserDir, update.LegacySystemdTimerTimer))
	backups, _ := filepath.Glob(filepath.Join(cfg.SystemdUserDir, "agentdeck-autoupdate.*.bak-agentdeck-*"))
	assert.Len(t, backups, 2, "both legacy units kept as backups")

	out.Reset()
	r.calls = nil
	r.replies["systemctl --user cat "+update.LegacySystemdTimerTimer] = cannedReply{out: "No files found\n", err: exitStatus(1)}
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "install", timerCommandOptions{JSON: true}, &out))
	var doc timerEnsureJSON
	require.NoError(t, json.Unmarshal(out.Bytes(), &doc))
	assert.Equal(t, update.TimerActionNone, doc.Action)
	for _, c := range r.calls {
		assert.NotContains(t, c, "enable", "an active canonical timer is left alone")
	}
}

// --ensure-timer honours manage_timer; --install-timer is explicit and does
// not. A host without a user bus: ensure is a quiet skip, install fails.
func TestEnsureTimer_ManageTimerAndNoBus(t *testing.T) {
	cfg := linuxTestTimerConfig(t)
	r := &cannedRunner{}
	var out bytes.Buffer
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "ensure", timerCommandOptions{ManageTimer: false}, &out))
	assert.Contains(t, out.String(), "update timer not managed: [updates] manage_timer = false")
	assert.Empty(t, r.calls)
	assert.NoFileExists(t, cfg.TimerPath())

	bus := cannedReply{out: "Failed to connect to bus: No medium found\n", err: exitStatus(1)}
	r = &cannedRunner{replies: map[string]cannedReply{"systemctl --user is-system-running": bus, "systemctl --user cat " + update.LegacySystemdTimerTimer: bus}}
	out.Reset()
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "ensure", timerCommandOptions{ManageTimer: true}, &out))
	assert.Contains(t, out.String(), "update timer not managed: no systemd user session")
	out.Reset()
	require.Equal(t, 1, runTimerCommandOpts(cfg, r, "install", timerCommandOptions{ManageTimer: true}, &out))
	assert.Contains(t, out.String(), "Error: update timer not installed: no systemd user session")
	assert.NoFileExists(t, cfg.TimerPath())
}

func TestInstallTimer_DryRunMigrationExecutesNothing(t *testing.T) {
	cfg := linuxTestTimerConfig(t)
	r := legacyHost(t, cfg)
	var out bytes.Buffer
	require.Equal(t, 0, runTimerCommandOpts(cfg, r, "install", timerCommandOptions{DryRun: true}, &out))
	assert.Contains(t, out.String(), "Dry run: would migrate the update timer")
	for _, c := range r.calls {
		assert.False(t, strings.Contains(c, "enable") || strings.Contains(c, "disable") || strings.Contains(c, "daemon-reload"), "dry run ran %q", c)
	}
	assert.FileExists(t, filepath.Join(cfg.SystemdUserDir, update.LegacySystemdTimerTimer))
}

// Install on first unattended use: the run heals the timer before anything
// else and says so in one line; a failed heal never fails the run.
func TestRunUnattendedUpdate_EnsuresTimerFirst(t *testing.T) {
	h := newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5"})
	h.deps.ensureTimer = func() (update.TimerEnsureResult, error) {
		h.calls = append(h.calls, "timer")
		return update.TimerEnsureResult{Action: update.TimerActionMigrated, Migrated: update.LegacySystemdTimerTimer}, nil
	}
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, "timer", h.calls[0])
	assert.Contains(t, h.out.String(), "migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer\n")

	h = newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5"})
	h.deps.ensureTimer = func() (update.TimerEnsureResult, error) {
		return update.TimerEnsureResult{Action: update.TimerActionInstalled}, errors.New("enable timer: exit status 1")
	}
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps), "a failed heal is a warning, not the run's failure")
	assert.Contains(t, h.out.String(), "Warning:")

	h = newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5"})
	h.deps.ensureTimer = func() (update.TimerEnsureResult, error) {
		return update.TimerEnsureResult{Action: update.TimerActionNone}, nil
	}
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.NotContains(t, h.out.String(), "timer", "an unchanged timer prints nothing")
}

func TestHealUpdateTimerAtDaemonStart_OnceThroughTheSeam(t *testing.T) {
	calls := 0
	prev := daemonEnsureUpdateTimer
	daemonEnsureUpdateTimer = func(*slog.Logger) (update.TimerEnsureResult, error) {
		calls++
		return update.TimerEnsureResult{Action: update.TimerActionInstalled, Status: update.TimerStatus{Kind: "systemd"}}, nil
	}
	t.Cleanup(func() { daemonEnsureUpdateTimer = prev })
	var buf bytes.Buffer
	res := healUpdateTimerAtDaemonStart(slog.New(slog.NewTextHandler(&buf, nil)))
	assert.Equal(t, 1, calls)
	assert.Equal(t, update.TimerActionInstalled, res.Action)
	assert.Contains(t, buf.String(), "update_timer_healed")
}

// failingNudger fails the nudge the way the issue's log shows.
type failingNudger struct{ err error }

func (f failingNudger) NudgeCheckNow(context.Context) error { return f.err }
func (f failingNudger) FallbackUpdate(context.Context) ([]byte, error) {
	return nil, f.err
}

// A failed nudge is visible in update --check --json (remote_nudges), not
// only in auto-update.log.
func TestNudgeOutcomeLandsInCheckJSON(t *testing.T) {
	setupRemoteListJSONTest(t)
	remotes := map[string]session.RemoteConfig{"slow": {Host: "u@slow"}, "fine": {Host: "u@fine"}}
	opts := session.NudgeRemoteOptions{
		NewRunner: func(name string, _ session.RemoteConfig) session.RemoteNudger {
			if name == "slow" {
				return failingNudger{err: errors.New("nudge failed: exit status 255: ssh: connect to host 100.64.0.9 port 22: Operation timed out")}
			}
			return failingNudger{}
		},
		Versions: map[string]session.RemoteVersionState{"slow": {Version: "1.16.24", Found: true}, "fine": {Version: "1.16.24", Found: true}},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var out bytes.Buffer
	nudgeRemotesAndRecord(context.Background(), remotes, "1.16.25", log, opts, &out)
	assert.Contains(t, out.String(), "slow: nudge failed")

	nudges := session.LoadRemoteNudges()
	require.Len(t, nudges, 2)
	doc := buildUpdateCheckJSON(&update.UpdateInfo{}, session.UpdateSettings{}, update.TimerStatus{}, "", nil, nil, nudges...)
	var buf bytes.Buffer
	require.NoError(t, printUpdateCheckJSON(&buf, doc))
	var got struct {
		RemoteNudges []map[string]any `json:"remote_nudges"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got.RemoteNudges, 2)
	assert.Equal(t, "fine", got.RemoteNudges[0]["remote"])
	assert.Equal(t, true, got.RemoteNudges[0]["ok"])
	slow := got.RemoteNudges[1]
	assert.Equal(t, "slow", slow["remote"])
	assert.Equal(t, "1.16.25", slow["asked_version"])
	assert.Equal(t, false, slow["ok"])
	assert.Equal(t, "failed", slow["outcome"])
	assert.Contains(t, slow["error"], "Operation timed out")
	_, err := time.Parse(time.RFC3339, slow["at"].(string))
	assert.NoError(t, err)

	var text bytes.Buffer
	printFailedRemoteNudges(&text, nudges)
	assert.Contains(t, text.String(), "slow: asked for v1.16.25")
	assert.NotContains(t, text.String(), "fine:")

	// A host that never nudged keeps the old document shape.
	buf.Reset()
	require.NoError(t, printUpdateCheckJSON(&buf, buildUpdateCheckJSON(&update.UpdateInfo{}, session.UpdateSettings{}, update.TimerStatus{}, "", nil, nil)))
	assert.NotContains(t, buf.String(), "remote_nudges")
}
