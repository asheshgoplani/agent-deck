package update

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// Names of the scheduled-update units on each platform.
const (
	LaunchdTimerPlistName = AutoupdateLabel + ".plist"
	SystemdTimerUnitBase  = "agent-deck-autoupdate"
	SystemdTimerService   = SystemdTimerUnitBase + ".service"
	SystemdTimerTimer     = SystemdTimerUnitBase + ".timer"
	// TimerLogFileName is the file the timer's stdout/stderr go to under the
	// agent-deck log dir (launchd only; systemd keeps it in the journal).
	TimerLogFileName = "auto-update.log"
	// TimerHour is the local hour the daily run is scheduled at.
	TimerHour = 7
	// TriggerEnv is the environment variable the timer sets so the run logs
	// itself as timer-triggered even when the flag is missing.
	TriggerEnv = "AGENTDECK_UPDATE_TRIGGER"
)

// TimerConfig is everything the timer plans depend on. DefaultTimerConfig
// resolves it from the host; tests build it by hand.
type TimerConfig struct {
	GOOS string
	// Exe is the absolute path of the agent-deck binary the timer runs.
	Exe  string
	UID  int
	Home string
	// LogDir receives auto-update.log (launchd).
	LogDir string
	// LaunchAgentsDir is ~/Library/LaunchAgents unless overridden.
	LaunchAgentsDir string
	// SystemdUserDir is ~/.config/systemd/user unless overridden.
	SystemdUserDir string
	// Minute is the launchd StartCalendarInterval minute (0-59), drawn at
	// random at install time. launchd has no RandomizedDelaySec, so spreading
	// installs across the hour is the closest equivalent.
	Minute int
}

// DefaultTimerConfig resolves the timer configuration for this host.
func DefaultTimerConfig() (TimerConfig, error) {
	exe, err := os.Executable()
	if err != nil {
		return TimerConfig{}, fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return TimerConfig{}, fmt.Errorf("resolve home: %w", err)
	}
	logDir, err := agentpaths.EffectiveDataPath("logs", "logs")
	if err != nil {
		return TimerConfig{}, fmt.Errorf("resolve log dir: %w", err)
	}
	return TimerConfig{
		GOOS:            runtime.GOOS,
		Exe:             exe,
		UID:             os.Getuid(),
		Home:            home,
		LogDir:          logDir,
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		SystemdUserDir:  filepath.Join(home, ".config", "systemd", "user"),
		Minute:          rand.Intn(60), // #nosec G404 -- schedule jitter, not security
	}, nil
}

// PlistPath is where the launchd timer plist lives.
func (c TimerConfig) PlistPath() string {
	return filepath.Join(c.LaunchAgentsDir, LaunchdTimerPlistName)
}

// ServicePath and TimerPath are the systemd user unit files.
func (c TimerConfig) ServicePath() string {
	return filepath.Join(c.SystemdUserDir, SystemdTimerService)
}

func (c TimerConfig) TimerPath() string {
	return filepath.Join(c.SystemdUserDir, SystemdTimerTimer)
}

// timerPATH is the PATH the timer run sees: the binary's own directory first
// (so a re-exec or helper lookup finds the same install), then the usual
// system and Homebrew locations. launchd's default PATH lacks all of them.
func (c TimerConfig) timerPATH() string {
	return strings.Join([]string{
		filepath.Dir(c.Exe),
		"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}, ":")
}

// LaunchdPlist renders ~/Library/LaunchAgents/com.agentdeck.autoupdate.plist.
//
// The program launchd registers is /bin/sh, not agent-deck: macOS BTM binds a
// user agent's identity to the file at its program path, and an agent whose
// program is the agent-deck binary crash-loops with EX_CONFIG after that
// binary is replaced (2026-07-30 incident). Wrapping the call in
// `sh -c 'exec "$0" ...' <exe>` keeps the identity on /bin/sh, which the
// update never touches, so the timer survives every install without being
// re-bootstrapped.
func (c TimerConfig) LaunchdPlist() []byte {
	var b bytes.Buffer
	esc := func(s string) string {
		var sb strings.Builder
		_ = xml.EscapeText(&sb, []byte(s))
		return sb.String()
	}
	logPath := filepath.Join(c.LogDir, TimerLogFileName)
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + AutoupdateLabel + `</string>

    <!-- Program identity stays /bin/sh so macOS BTM never ties this agent to
         the agent-deck binary that the run itself replaces. -->
    <key>ProgramArguments</key>
    <array>
        <string>/bin/sh</string>
        <string>-c</string>
        <string>exec "$0" update --unattended --trigger timer</string>
        <string>` + esc(c.Exe) + `</string>
    </array>

    <!-- Daily at 07:` + fmt.Sprintf("%02d", c.Minute) + ` local time. launchd has no RandomizedDelaySec; the
         minute is drawn at random when the timer is installed instead. -->
    <key>StartCalendarInterval</key>
    <dict>
        <key>Hour</key>
        <integer>` + fmt.Sprint(TimerHour) + `</integer>
        <key>Minute</key>
        <integer>` + fmt.Sprint(c.Minute) + `</integer>
    </dict>

    <key>RunAtLoad</key>
    <false/>

    <key>StandardOutPath</key>
    <string>` + esc(logPath) + `</string>
    <key>StandardErrorPath</key>
    <string>` + esc(logPath) + `</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>` + esc(c.timerPATH()) + `</string>
        <key>HOME</key>
        <string>` + esc(c.Home) + `</string>
        <key>` + TriggerEnv + `</key>
        <string>timer</string>
    </dict>
</dict>
</plist>
`)
	return b.Bytes()
}

// SystemdService renders the oneshot service the timer fires.
func (c TimerConfig) SystemdService() []byte {
	return []byte(`[Unit]
Description=agent-deck unattended update
After=network-online.target

[Service]
Type=oneshot
Environment=` + TriggerEnv + `=timer
ExecStart=` + systemdQuote(c.Exe) + ` update --unattended --trigger timer
`)
}

// SystemdTimer renders the daily timer with a randomized delay.
func (c TimerConfig) SystemdTimer() []byte {
	return []byte(`[Unit]
Description=Daily agent-deck unattended update

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`)
}

// systemdQuote quotes a path for an ExecStart line.
func systemdQuote(p string) string {
	if strings.ContainsAny(p, " \t\"'\\") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
	}
	return p
}

// InstallTimerPlan returns the steps that install (or replace) the timer.
func InstallTimerPlan(c TimerConfig) (Plan, error) {
	switch c.GOOS {
	case "darwin":
		domain := launchctlDomain(c.UID)
		return Plan{Steps: []Step{
			{Desc: "write launchd plist", WritePath: c.PlistPath(), Content: c.LaunchdPlist(), Mode: 0o644},
			{Desc: "unload previous timer", Argv: []string{"launchctl", "bootout", domain + "/" + AutoupdateLabel}, Tolerate: launchctlNotLoaded},
			{Desc: "load timer", Argv: []string{"launchctl", "bootstrap", domain, c.PlistPath()}},
			{Desc: "verify timer", Argv: []string{"launchctl", "print", domain + "/" + AutoupdateLabel}},
		}}, nil
	case "linux":
		return Plan{Steps: []Step{
			{Desc: "write systemd service", WritePath: c.ServicePath(), Content: c.SystemdService(), Mode: 0o644},
			{Desc: "write systemd timer", WritePath: c.TimerPath(), Content: c.SystemdTimer(), Mode: 0o644},
			{Desc: "reload systemd", Argv: []string{"systemctl", "--user", "daemon-reload"}},
			{Desc: "enable timer", Argv: []string{"systemctl", "--user", "enable", "--now", SystemdTimerTimer}},
			{Desc: "verify timer", Argv: []string{"systemctl", "--user", "is-active", SystemdTimerTimer}},
		}}, nil
	}
	return Plan{}, fmt.Errorf("update timer is not supported on %s", c.GOOS)
}

// UninstallTimerPlan returns the steps that remove the timer. An empty plan
// means nothing is installed.
func UninstallTimerPlan(c TimerConfig) (Plan, error) {
	switch c.GOOS {
	case "darwin":
		if !fileExists(c.PlistPath()) {
			return Plan{}, nil
		}
		domain := launchctlDomain(c.UID)
		return Plan{Steps: []Step{
			{Desc: "unload timer", Argv: []string{"launchctl", "bootout", domain + "/" + AutoupdateLabel}, Tolerate: launchctlNotLoaded},
			{Desc: "remove launchd plist", RemovePath: c.PlistPath()},
		}}, nil
	case "linux":
		if !fileExists(c.ServicePath()) && !fileExists(c.TimerPath()) {
			return Plan{}, nil
		}
		return Plan{Steps: []Step{
			{Desc: "disable timer", Argv: []string{"systemctl", "--user", "disable", "--now", SystemdTimerTimer}, Tolerate: func(string, error) bool { return true }},
			{Desc: "remove systemd timer", RemovePath: c.TimerPath()},
			{Desc: "remove systemd service", RemovePath: c.ServicePath()},
			{Desc: "reload systemd", Argv: []string{"systemctl", "--user", "daemon-reload"}},
		}}, nil
	}
	return Plan{}, fmt.Errorf("update timer is not supported on %s", c.GOOS)
}

// TimerStatus describes whether the scheduled update is installed.
type TimerStatus struct {
	Installed bool   `json:"installed"`
	Kind      string `json:"kind"` // launchd | systemd | none
	Path      string `json:"path,omitempty"`
	// Active reports whether the init system currently has the unit loaded
	// (launchctl print / systemctl is-active). Only queried when a Runner is
	// given and the unit file exists.
	Active bool `json:"active"`
	// Detail is the schedule line for humans (e.g. "daily at 07:23").
	Detail string `json:"detail,omitempty"`
}

// QueryTimerStatus reports the timer's install state. r may be nil to skip
// the init-system query.
func QueryTimerStatus(c TimerConfig, r Runner) TimerStatus {
	switch c.GOOS {
	case "darwin":
		st := TimerStatus{Kind: "launchd", Path: c.PlistPath()}
		if !fileExists(st.Path) {
			st.Kind = "none"
			return st
		}
		st.Installed = true
		if data, err := os.ReadFile(st.Path); err == nil {
			st.Detail = launchdScheduleDetail(data)
		}
		if r != nil {
			_, err := r.Run("launchctl", "print", launchctlDomain(c.UID)+"/"+AutoupdateLabel)
			st.Active = err == nil
		}
		return st
	case "linux":
		st := TimerStatus{Kind: "systemd", Path: c.TimerPath()}
		if !fileExists(st.Path) {
			st.Kind = "none"
			return st
		}
		st.Installed = true
		st.Detail = "daily, randomized delay up to 1h"
		if r != nil {
			out, err := r.Run("systemctl", "--user", "is-active", SystemdTimerTimer)
			st.Active = err == nil && strings.TrimSpace(out) == "active"
		}
		return st
	}
	return TimerStatus{Kind: "none"}
}

// launchdScheduleDetail pulls Hour/Minute out of an installed plist so the
// status line can say when the run happens.
func launchdScheduleDetail(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root, err := plistRootDict(dec)
	if err != nil {
		return ""
	}
	cal, ok := root["StartCalendarInterval"].(map[string]any)
	if !ok {
		return ""
	}
	hour, _ := cal["Hour"].(int64)
	minute, _ := cal["Minute"].(int64)
	return fmt.Sprintf("daily at %02d:%02d local time", hour, minute)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
