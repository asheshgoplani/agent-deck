package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateTrigger(t *testing.T) {
	t.Setenv(update.TriggerEnv, "")
	assert.Equal(t, "manual", updateTrigger(""))
	assert.Equal(t, "tui", updateTrigger(" tui "))
	t.Setenv(update.TriggerEnv, "timer")
	assert.Equal(t, "timer", updateTrigger(""))
	assert.Equal(t, "manual", updateTrigger("manual"), "flag wins over the env var")
}

func TestBuildUpdateCheckJSON(t *testing.T) {
	off := false
	doc := buildUpdateCheckJSON(
		&update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.17.0", Available: true, PublishingVersion: "1.17.1"},
		session.UpdateSettings{AutoInstall: &off},
		update.TimerStatus{Installed: true, Kind: "launchd", Path: "/x/com.agentdeck.autoupdate.plist", Active: true},
	)
	var buf bytes.Buffer
	require.NoError(t, printUpdateCheckJSON(&buf, doc))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "1.16.5", got["current"])
	assert.Equal(t, "1.17.0", got["latest"])
	assert.Equal(t, true, got["available"])
	assert.Equal(t, "1.17.1", got["publishing"])
	assert.Equal(t, false, got["auto_install"])
	assert.Equal(t, true, got["auto_restart"], "unset auto_restart defaults to true")
	timer := got["timer"].(map[string]any)
	assert.Equal(t, true, timer["installed"])
	assert.Equal(t, "launchd", timer["kind"])
	assert.Equal(t, "/x/com.agentdeck.autoupdate.plist", timer["path"])
}

// unattendedHarness records which collaborators ran.
type unattendedHarness struct {
	deps  unattendedDeps
	out   bytes.Buffer
	calls []string
}

func newUnattendedHarness(t *testing.T, info *update.UpdateInfo) *unattendedHarness {
	t.Helper()
	h := &unattendedHarness{}
	h.deps = unattendedDeps{
		version:     "1.16.5",
		trigger:     "timer",
		autoInstall: true,
		lockDir:     t.TempDir(),
		out:         &h.out,
		check: func() (*update.UpdateInfo, error) {
			h.calls = append(h.calls, "check")
			return info, nil
		},
		detectHomebrew: func() (string, string, bool, error) {
			h.calls = append(h.calls, "homebrew")
			return "/usr/local/bin/agent-deck", "", false, nil
		},
		preflight: func() error { h.calls = append(h.calls, "preflight"); return nil },
		install: func(latest string) error {
			h.calls = append(h.calls, "install "+latest)
			return nil
		},
		updateBridge: func() error { h.calls = append(h.calls, "bridge"); return nil },
		hygiene:      func() error { h.calls = append(h.calls, "hygiene"); return nil },
	}
	return h
}

func availableInfo() *update.UpdateInfo {
	return &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.17.0", Available: true}
}

func TestRunUnattendedUpdate_HappyPath(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	code := runUnattendedUpdate(h.deps)
	assert.Equal(t, exitUpdateOK, code)
	assert.Equal(t, []string{"check", "homebrew", "preflight", "install 1.17.0", "bridge", "hygiene"}, h.calls)
	assert.Contains(t, h.out.String(), "✓ Updated to v1.17.0 (unattended); running agent-deck processes restart themselves")
	assert.NoFileExists(t, filepath.Join(h.deps.lockDir, update.UpdateLockFileName), "lock released")
}

func TestRunUnattendedUpdate_NothingToDo(t *testing.T) {
	h := newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5"})
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check"}, h.calls)
	assert.Contains(t, h.out.String(), "v1.16.5 is current; nothing to do")

	h = newUnattendedHarness(t, &update.UpdateInfo{CurrentVersion: "1.16.5", LatestVersion: "1.16.5", PublishingVersion: "1.17.0"})
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check"}, h.calls)
	assert.Contains(t, h.out.String(), "v1.17.0 is still publishing")
}

func TestRunUnattendedUpdate_HonoursAutoInstallOff(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	h.deps.autoInstall = false
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check"}, h.calls)
	assert.Contains(t, h.out.String(), "auto_install is off in config.toml, nothing installed")
}

func TestRunUnattendedUpdate_HomebrewIsNeverRun(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	h.deps.detectHomebrew = func() (string, string, bool, error) {
		return "/opt/homebrew/Cellar/agent-deck/1.16.5/bin/agent-deck", "brew upgrade asheshgoplani/tap/agent-deck", true, nil
	}
	assert.Equal(t, exitUpdateHomebrew, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check"}, h.calls)
	assert.Contains(t, h.out.String(), "Run: brew update && brew upgrade asheshgoplani/tap/agent-deck")
}

func TestRunUnattendedUpdate_LockBusy(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	release, busy, err := update.AcquireUpdateLock(h.deps.lockDir, update.UpdateLockStaleAfter)
	require.NoError(t, err)
	require.False(t, busy)
	defer release()

	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check", "homebrew"}, h.calls)
	assert.Contains(t, h.out.String(), "already running")
}

func TestRunUnattendedUpdate_PreflightRefusesBeforeInstall(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	h.deps.preflight = func() error { return errors.New("`launchctl print gui/501` failed") }
	assert.Equal(t, exitUpdateFailed, runUnattendedUpdate(h.deps))
	assert.Equal(t, []string{"check", "homebrew"}, h.calls)
	assert.Contains(t, h.out.String(), "Refusing to install v1.17.0")
	assert.Contains(t, h.out.String(), "launchctl print gui/501")
	assert.NoFileExists(t, filepath.Join(h.deps.lockDir, update.UpdateLockFileName))
}

func TestRunUnattendedUpdate_HygieneFailureIsReported(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	h.deps.hygiene = func() error {
		return &update.AgentRestartError{
			Label:  "com.agentdeck.web",
			Cause:  errors.New("not running after 10s (state = waiting)"),
			Repair: []string{"launchctl bootout gui/501/com.agentdeck.web", "launchctl bootstrap gui/501 /x/com.agentdeck.web.plist"},
		}
	}
	assert.Equal(t, exitUpdateFailed, runUnattendedUpdate(h.deps))
	assert.Contains(t, h.out.String(), "Installed v1.17.0 but com.agentdeck.web did not come back: not running after 10s (state = waiting); run: launchctl bootout gui/501/com.agentdeck.web; launchctl bootstrap gui/501 /x/com.agentdeck.web.plist")
}

func TestRunUnattendedUpdate_BridgeFailureIsOnlyAWarning(t *testing.T) {
	h := newUnattendedHarness(t, availableInfo())
	h.deps.updateBridge = func() error { return errors.New("no conductor") }
	assert.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))
	assert.Contains(t, h.out.String(), "Warning: failed to update bridge.py: no conductor")
	assert.Contains(t, h.out.String(), "✓ Updated to v1.17.0")
}

// recordingRunner is the cmd-level fake for launchctl/systemctl.
type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(argv ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(argv, " "))
	return "", nil
}

func testTimerConfig(t *testing.T) update.TimerConfig {
	t.Helper()
	home := t.TempDir()
	return update.TimerConfig{
		GOOS:            "darwin",
		Exe:             "/Users/me/.local/bin/agent-deck",
		UID:             501,
		Home:            home,
		LogDir:          filepath.Join(home, "logs"),
		LaunchAgentsDir: filepath.Join(home, "LaunchAgents"),
		SystemdUserDir:  filepath.Join(home, "systemd"),
		Minute:          11,
	}
}

func TestRunTimerCommand_DryRunWritesNothing(t *testing.T) {
	cfg := testTimerConfig(t)
	r := &recordingRunner{}
	var out bytes.Buffer
	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "install", true, &out))
	assert.Empty(t, r.calls)
	assert.NoFileExists(t, cfg.PlistPath())
	assert.Contains(t, out.String(), "Dry run: would install")
	assert.Contains(t, out.String(), "launchctl bootstrap gui/501 "+cfg.PlistPath())
	assert.Contains(t, out.String(), "<string>/bin/sh</string>")
}

func TestRunTimerCommand_InstallStatusUninstall(t *testing.T) {
	cfg := testTimerConfig(t)
	r := &recordingRunner{}
	var out bytes.Buffer

	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "status", false, &out))
	assert.Contains(t, out.String(), "not installed")
	out.Reset()

	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "install", false, &out))
	assert.FileExists(t, cfg.PlistPath())
	assert.Contains(t, out.String(), "✓ Update timer installed (launchd)")
	assert.Contains(t, out.String(), "daily at 07:11 local time")
	assert.Equal(t, []string{
		"launchctl bootout gui/501/com.agentdeck.autoupdate",
		"launchctl bootstrap gui/501 " + cfg.PlistPath(),
		"launchctl print gui/501/com.agentdeck.autoupdate",
	}, r.calls)
	out.Reset()

	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "status", false, &out))
	assert.Contains(t, out.String(), "Update timer: active (launchd)")
	out.Reset()

	r.calls = nil
	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "uninstall", false, &out))
	assert.NoFileExists(t, cfg.PlistPath())
	assert.Equal(t, []string{"launchctl bootout gui/501/com.agentdeck.autoupdate"}, r.calls)
	assert.Contains(t, out.String(), "✓ Update timer removed")
	out.Reset()

	assert.Equal(t, 0, runTimerCommandWith(cfg, r, "uninstall", false, &out))
	assert.Contains(t, out.String(), "not installed; nothing to remove")
}

func TestRunTimerCommand_UnsupportedOS(t *testing.T) {
	cfg := testTimerConfig(t)
	cfg.GOOS = "windows"
	var out bytes.Buffer
	assert.Equal(t, 1, runTimerCommandWith(cfg, &recordingRunner{}, "install", false, &out))
	assert.Contains(t, out.String(), "not supported on windows")
}
