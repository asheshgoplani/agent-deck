package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// Standing orders (`agent-deck orders`): ONE timer per host runs
// `agent-deck orders tick --all` every minute. The tick walks every
// conductor's ORDERS.md; a conductor without one costs a stat. The unit
// passes --source orders so `agent-deck triggers` shows the timer and every
// wake it sends on one row (systemd:agent-deck-orders).

const (
	ordersUnitBase     = "agent-deck-orders"
	ordersLaunchdLabel = "com.agentdeck.orders"
	// OrdersTickSource is the --source the timer's tick tags its sends with.
	OrdersTickSource = "orders"
)

const systemdOrdersTimerTemplate = `[Unit]
Description=Agent Deck standing orders tick (every minute)

[Timer]
OnBootSec=60s
OnUnitActiveSec=60s
AccuracySec=5s

[Install]
WantedBy=timers.target
`

const systemdOrdersServiceTemplate = `[Unit]
Description=Agent Deck standing orders tick

[Service]
Type=oneshot
ExecStart="__AGENT_DECK__" orders tick --all --source __SOURCE__
WorkingDirectory=__HOME__
Environment="PATH=__PATH__"
Environment="HOME=__HOME__"
`

const ordersLaunchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>__LABEL__</string>
    <key>ProgramArguments</key>
    <array>
        <string>__AGENT_DECK__</string>
        <string>orders</string>
        <string>tick</string>
        <string>--all</string>
        <string>--source</string>
        <string>__SOURCE__</string>
    </array>
    <key>StartInterval</key>
    <integer>60</integer>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>__PATH__</string>
        <key>HOME</key>
        <string>__HOME__</string>
    </dict>
</dict>
</plist>
`

// SystemdOrdersTimerName is the host's standing-orders timer unit.
func SystemdOrdersTimerName() string { return ordersUnitBase + ".timer" }

// SystemdOrdersServiceName is the oneshot service the timer starts.
func SystemdOrdersServiceName() string { return ordersUnitBase + ".service" }

func ordersAgentDeckPath() string {
	if p := FindAgentDeck(); p != "" {
		return p
	}
	return "agent-deck"
}

// GenerateSystemdOrdersService returns the oneshot service unit.
func GenerateSystemdOrdersService() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	agentDeck := ordersAgentDeckPath()
	unit := strings.ReplaceAll(systemdOrdersServiceTemplate, "__AGENT_DECK__", agentDeck)
	unit = strings.ReplaceAll(unit, "__SOURCE__", OrdersTickSource)
	unit = strings.ReplaceAll(unit, "__HOME__", home)
	unit = strings.ReplaceAll(unit, "__PATH__", buildDaemonPath(FindAgentDeck()))
	return unit, nil
}

// GenerateSystemdOrdersTimer returns the one-minute timer unit.
func GenerateSystemdOrdersTimer() string { return systemdOrdersTimerTemplate }

// GenerateOrdersLaunchdPlist returns the macOS equivalent (StartInterval 60).
func GenerateOrdersLaunchdPlist() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	plist := strings.ReplaceAll(ordersLaunchdTemplate, "__LABEL__", ordersLaunchdLabel)
	plist = strings.ReplaceAll(plist, "__AGENT_DECK__", ordersAgentDeckPath())
	plist = strings.ReplaceAll(plist, "__SOURCE__", OrdersTickSource)
	plist = strings.ReplaceAll(plist, "__HOME__", home)
	plist = strings.ReplaceAll(plist, "__PATH__", buildDaemonPath(FindAgentDeck()))
	return plist, nil
}

func ordersLaunchdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", ordersLaunchdLabel+".plist"), nil
}

// InstallOrdersDaemon writes and starts the host's standing-orders timer
// (systemd user timer on Linux, launchd job on macOS). It is idempotent and
// returns the timer/plist path. Without a systemd user session nothing is
// written and the error says how to run the tick from cron instead.
func InstallOrdersDaemon() (string, error) {
	switch platform.Detect() {
	case platform.PlatformMacOS:
		return installOrdersLaunchd()
	case platform.PlatformLinux, platform.PlatformWSL2:
		return installOrdersSystemd()
	default:
		return "", fmt.Errorf("no timer support on this platform; run 'agent-deck orders tick --all' every minute from cron")
	}
}

func installOrdersSystemd() (string, error) {
	if !systemdUserAvailable() {
		return "", fmt.Errorf("systemd user session not available; run 'agent-deck orders tick --all' every minute from cron")
	}
	dir, err := SystemdUserDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating systemd user dir: %w", err)
	}
	svc, err := GenerateSystemdOrdersService()
	if err != nil {
		return "", fmt.Errorf("generating orders service: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SystemdOrdersServiceName()), []byte(svc), 0o644); err != nil {
		return "", fmt.Errorf("writing orders service: %w", err)
	}
	timerPath := filepath.Join(dir, SystemdOrdersTimerName())
	if err := os.WriteFile(timerPath, []byte(GenerateSystemdOrdersTimer()), 0o644); err != nil {
		return "", fmt.Errorf("writing orders timer: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", SystemdOrdersTimerName()).Run(); err != nil { //nolint:gosec // G204: fixed argv
		return "", fmt.Errorf("enabling orders timer: %w", err)
	}
	return timerPath, nil
}

func installOrdersLaunchd() (string, error) {
	plist, err := GenerateOrdersLaunchdPlist()
	if err != nil {
		return "", err
	}
	path, err := ordersLaunchdPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return "", fmt.Errorf("writing orders plist: %w", err)
	}
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		return "", fmt.Errorf("plist written but failed to load: %w", err)
	}
	return path, nil
}

// UninstallOrdersDaemon stops and removes the host's standing-orders timer.
// A timer that is not installed is not an error.
func UninstallOrdersDaemon() error {
	switch platform.Detect() {
	case platform.PlatformMacOS:
		path, err := ordersLaunchdPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		_ = exec.Command("launchctl", "unload", path).Run()
		return os.Remove(path)
	case platform.PlatformLinux, platform.PlatformWSL2:
		dir, err := SystemdUserDir()
		if err != nil {
			return err
		}
		timerPath := filepath.Join(dir, SystemdOrdersTimerName())
		if _, err := os.Stat(timerPath); os.IsNotExist(err) {
			return nil
		}
		if err := exec.Command("systemctl", "--user", "disable", "--now", SystemdOrdersTimerName()).Run(); err != nil { //nolint:gosec // G204: fixed argv
			return fmt.Errorf("stopping orders timer: %w", err)
		}
		if err := os.Remove(timerPath); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, SystemdOrdersServiceName())); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	return nil
}

// OrdersDaemonInstalled reports whether the timer (or plist) file exists.
func OrdersDaemonInstalled() bool {
	if platform.Detect() == platform.PlatformMacOS {
		path, err := ordersLaunchdPath()
		if err != nil {
			return false
		}
		_, err = os.Stat(path)
		return err == nil
	}
	dir, err := SystemdUserDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, SystemdOrdersTimerName()))
	return err == nil
}
