// Package main — `agent-deck telegram-doctor` subcommand (issue #1138).
//
// Surfaces silent Telegram channel-plugin drops at any time. For each
// session in the active profile that owns a `plugin:telegram@…`
// channel, the command checks:
//
//  1. The EFFECTIVE settings.json (scratch when present, ambient
//     otherwise) has the channel plugin enabled. This is the necessary
//     condition for `--channels` to wire onto a live MCP transport.
//  2. A `bun telegram` poller process is currently running on the host.
//     If a session is configured to own the channel but no poller is
//     visible, Telegram inbound is dropping.
//
// The check uses the same VerifyTelegramChannelEnabled helper that the
// session prepare path uses, so the diagnostic is the same in both
// directions: what we set up at spawn time is what we report at audit
// time.
//
// Exit codes:
//
//	0 — every channel-owning session has a healthy poller, settings.json
//	    has telegram=true.
//	1 — at least one channel-owning session is unhealthy (settings drift,
//	    missing poller, or both). Stderr names the offending session and
//	    prints the heal command to run.
//
// The user can run this ad-hoc or cron it (every 5–10 minutes) for
// periodic monitoring. See PR fix(telegram): force-correct scratch
// settings on every spawn + post-spawn health check + telegram-doctor CLI.

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleTelegramDoctor is the entry point dispatched from main.go.
func handleTelegramDoctor(profile string, args []string) {
	fs := flag.NewFlagSet("telegram-doctor", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	quiet := fs.Bool("quiet", false, "suppress healthy lines; only print drift")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	out := NewCLIOutput(*jsonOutput, *quiet)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(fmt.Sprintf("failed to initialize storage: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		out.Error(fmt.Sprintf("failed to load sessions: %v", err), ErrCodeNotFound)
		os.Exit(1)
	}

	runningPollers := scanBunTelegramProcesses()

	type report struct {
		Title          string `json:"title"`
		InstanceID     string `json:"instance_id"`
		ConfigDir      string `json:"config_dir"`
		EffectiveValue string `json:"effective_value"`
		Reason         string `json:"reason,omitempty"`
		PollerPIDs     []int  `json:"poller_pids,omitempty"`
		Healthy        bool   `json:"healthy"`
	}

	reports := make([]report, 0)
	anyUnhealthy := false
	channelOwners := 0

	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if !hasTelegramChannel(inst.Channels) {
			continue
		}
		channelOwners++

		// Resolve the EFFECTIVE config dir — scratch wins when present
		// (matches the spawn path's applyWorkerScratchOverride logic).
		effectiveDir := inst.WorkerScratchConfigDir
		if effectiveDir == "" {
			effectiveDir = session.GetClaudeConfigDirForInstance(inst)
		}

		result := session.VerifyTelegramChannelEnabled(effectiveDir, inst.Channels)
		pollerPIDs := pollersForInstance(inst, runningPollers)
		healthy := result.OK && len(pollerPIDs) > 0
		reason := result.Reason
		if result.OK && len(pollerPIDs) == 0 {
			reason = "no `bun telegram` poller process is running for this conductor; --channels has nothing to deliver to"
		}
		reports = append(reports, report{
			Title:          inst.Title,
			InstanceID:     inst.ID,
			ConfigDir:      effectiveDir,
			EffectiveValue: result.EffectiveValue,
			Reason:         reason,
			PollerPIDs:     pollerPIDs,
			Healthy:        healthy,
		})
		if !healthy {
			anyUnhealthy = true
		}
	}

	if *jsonOutput {
		out.Print("", map[string]interface{}{
			"profile":        profile,
			"channel_owners": channelOwners,
			"reports":        reports,
		})
	} else {
		if channelOwners == 0 {
			out.Print("no telegram channel-owning sessions in this profile", nil)
		}
		for _, r := range reports {
			if r.Healthy {
				if !*quiet {
					fmt.Printf("OK     %-32s  pids=%v  settings=%s\n", r.Title, r.PollerPIDs, r.EffectiveValue)
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "DRIFT  %-32s  reason=%s\n", r.Title, r.Reason)
			fmt.Fprintf(os.Stderr, "       heal:  agent-deck -p %s session restart %s\n", profile, r.InstanceID)
			fmt.Fprintf(os.Stderr, "       (the scratch CLAUDE_CONFIG_DIR is rewritten on every restart and should\n")
			fmt.Fprintf(os.Stderr, "        force-correct enabledPlugins.%q=true in %s)\n",
				"telegram@claude-plugins-official", r.ConfigDir)
		}
	}

	if anyUnhealthy {
		os.Exit(1)
	}
}

// hasTelegramChannel mirrors session.sessionHasTelegramChannel but is
// reachable from the cmd/ package. Channel ids start with
// "plugin:telegram@" for the official plugin and any fork.
func hasTelegramChannel(channels []string) bool {
	for _, ch := range channels {
		if strings.HasPrefix(ch, "plugin:telegram@") {
			return true
		}
	}
	return false
}

// scanBunTelegramProcesses returns the PIDs of every running `bun
// telegram` process on the host. We use `pgrep -af` because it matches
// the maintainer's documented diagnostic command (CLAUDE.md notes
// `pgrep -af 'bun.*telegram'`). Falling back silently to an empty list
// on platforms without pgrep keeps the doctor usable on macOS too.
func scanBunTelegramProcesses() []procInfo {
	out, err := exec.Command("pgrep", "-af", "bun.*telegram").Output()
	if err != nil {
		return nil
	}
	var procs []procInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) < 2 {
			continue
		}
		pid := 0
		if _, err := fmt.Sscanf(fields[0], "%d", &pid); err != nil {
			continue
		}
		procs = append(procs, procInfo{PID: pid, Cmdline: fields[1]})
	}
	return procs
}

type procInfo struct {
	PID     int
	Cmdline string
}

// pollersForInstance correlates a session with the `bun telegram`
// processes that belong to it via TELEGRAM_STATE_DIR. The conductor's
// state dir is captured in /proc/<pid>/environ on Linux — we read that
// directly because the conductor host is Linux in every reported
// instance of #1138.
//
// Resolution chain for the expected state dir:
//  1. The conductor env_file declared in ~/.agent-deck/config.toml
//     under [conductors.<name>.claude].env_file. The file contains
//     a line like `export TELEGRAM_STATE_DIR=<dir>`.
//  2. Any `TELEGRAM_STATE_DIR=` line found in the file is taken as
//     authoritative.
//
// On macOS /proc isn't available, so this function falls back to
// "any running bun-telegram counts" — coarser, but still useful as a
// pulse check.
func pollersForInstance(inst *session.Instance, all []procInfo) []int {
	if inst == nil {
		return nil
	}
	wantStateDir := readConductorTelegramStateDir(inst)

	var matched []int
	for _, p := range all {
		if wantStateDir == "" {
			matched = append(matched, p.PID)
			continue
		}
		envData, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", p.PID))
		if err != nil {
			// Non-Linux or process gone — keep the PID as a coarse match
			// so we don't false-alarm.
			matched = append(matched, p.PID)
			continue
		}
		if strings.Contains(string(envData), "TELEGRAM_STATE_DIR="+wantStateDir) {
			matched = append(matched, p.PID)
		}
	}
	return matched
}

// readConductorTelegramStateDir looks up the conductor's env_file via
// [conductors.<name>.claude].env_file in config.toml and extracts the
// TELEGRAM_STATE_DIR value if present. Returns "" if the session isn't
// a conductor, the env_file isn't declared, or the file is missing.
//
// Best-effort: any failure degrades to coarse PID matching (all bun
// pollers counted as candidates) rather than a hard error.
func readConductorTelegramStateDir(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	name := strings.TrimPrefix(inst.Title, "conductor-")
	if name == "" || name == inst.Title {
		return ""
	}
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return ""
	}
	cond, ok := cfg.Conductors[name]
	if !ok {
		return ""
	}
	envFile := cond.Claude.EnvFile
	if envFile == "" {
		return ""
	}
	if strings.HasPrefix(envFile, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			envFile = home + envFile[1:]
		}
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "export ")
		if !strings.HasPrefix(line, "TELEGRAM_STATE_DIR=") {
			continue
		}
		v := strings.TrimPrefix(line, "TELEGRAM_STATE_DIR=")
		v = strings.Trim(v, "\"'")
		if strings.HasPrefix(v, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				v = home + v[1:]
			}
		}
		return v
	}
	return ""
}
