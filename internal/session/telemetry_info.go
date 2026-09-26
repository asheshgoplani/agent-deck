package session

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// This file turns local state into the counts and enum values that opt-in
// telemetry records. Paths, names and titles are read here only to count or
// classify them; none of them is passed on.

// toolBinaries maps built-in tools to the binary looked up on PATH.
var toolBinaries = map[string]string{
	"claude": "claude", "codex": "codex", "gemini": "gemini", "opencode": "opencode",
	"pi": "pi", "copilot": "copilot", "crush": "crush", "cursor": "cursor-agent",
	"hermes": "hermes", "deepseek": "deepseek", "aider": "aider",
}

// TelemetrySummary is what telemetry needs from the loaded sessions: counts
// and tool names only. Build it where the instances are owned (the TUI
// goroutine); the rest of this file then does its I/O anywhere.
type TelemetrySummary struct {
	Sessions    int
	Groups      int
	Tools       []string
	HasWorktree bool
}

// SummarizeForTelemetry reads the fields telemetry counts.
func SummarizeForTelemetry(instances []*Instance, groups int) TelemetrySummary {
	sum := TelemetrySummary{Sessions: len(instances), Groups: groups}
	for _, inst := range instances {
		sum.Tools = append(sum.Tools, inst.GetToolThreadSafe())
		if inst.WorktreePath != "" {
			sum.HasWorktree = true
		}
	}
	return sum
}

// TelemetryFleet counts the configured fleet for app.start.
func TelemetryFleet(sum TelemetrySummary) telemetry.FleetCounts {
	fc := telemetry.FleetCounts{Sessions: sum.Sessions, Groups: sum.Groups}
	if profiles, err := ListProfiles(); err == nil {
		fc.Profiles = len(profiles)
	}
	if conductors, err := ListConductors(); err == nil {
		fc.Conductors = len(conductors)
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil {
		fc.Remotes = len(cfg.Remotes)
	}
	return fc
}

// TelemetryBaseline describes this install at consent time.
func TelemetryBaseline(sum TelemetrySummary) telemetry.Baseline {
	b := telemetry.Baseline{
		InstallMethod: telemetryInstallMethod(),
		ToolsFound:    telemetryToolsOnPath(),
		Sessions:      sum.Sessions,
		ToolsUsed:     sum.Tools,
		HasWorktree:   sum.HasWorktree,
		HasGroups:     sum.Groups > 0,
	}
	_, err := exec.LookPath("tmux")
	b.TmuxOK = err == nil
	if p, err := GetUserConfigPath(); err == nil {
		if _, err := os.Stat(p); err == nil {
			b.HadConfig = true
		}
	}
	if conductors, err := ListConductors(); err == nil && len(conductors) > 0 {
		b.HasConductor = true
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil && len(cfg.Remotes) > 0 {
		b.HasRemote = true
	}
	return b
}

// TelemetrySummaryFromStorage loads the current profile's sessions, for a
// baseline computed outside the TUI (`telemetry on`).
func TelemetrySummaryFromStorage() TelemetrySummary {
	st, err := NewStorageWithProfile(GetEffectiveProfile(""))
	if err != nil {
		return TelemetrySummary{}
	}
	defer st.Close()
	instances, groups, err := st.LoadWithGroups()
	if err != nil {
		return TelemetrySummary{}
	}
	return SummarizeForTelemetry(instances, len(groups))
}

func telemetryToolsOnPath() uint32 {
	var m uint32
	for tool, bin := range toolBinaries {
		if _, err := exec.LookPath(bin); err == nil {
			m |= telemetry.ToolBit(tool)
		}
	}
	return m
}

// telemetryInstallMethod classifies the running binary's location.
func telemetryInstallMethod() string {
	exe, err := os.Executable()
	if err != nil {
		return "other"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	switch {
	case strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/") || strings.Contains(exe, "/linuxbrew/"):
		return "brew"
	case strings.Contains(exe, "/go/bin/") || os.Getenv("GOBIN") != "" && strings.HasPrefix(exe, os.Getenv("GOBIN")):
		return "go_install"
	case home != "" && strings.HasPrefix(exe, filepath.Join(home, ".local", "bin")):
		return "script"
	case strings.HasPrefix(exe, "/usr/local/bin/"):
		return "release_tarball"
	}
	return "other"
}

var tmuxMinorRe = regexp.MustCompile(`([0-9]+)\.([0-9]+)`)

// TelemetryEnv classifies the terminal environment for env.snapshot.
func TelemetryEnv() telemetry.EnvInfo {
	e := telemetry.EnvInfo{
		Terminal:       telemetryTerminal(),
		TmuxMinor:      "other",
		Shell:          filepath.Base(os.Getenv("SHELL")),
		InstallMethod:  telemetryInstallMethod(),
		Color:          telemetryColor(),
		ToolsInstalled: telemetryToolsOnPath(),
		ConfigSections: telemetryConfigSections(),
	}
	if out, err := exec.Command("tmux", "-V").Output(); err == nil {
		if m := tmuxMinorRe.FindStringSubmatch(string(out)); m != nil {
			e.TmuxMinor = m[1] + "." + m[2]
		}
	}
	return e
}

func telemetryTerminal() string {
	if os.Getenv("TMUX") != "" {
		return "tmux_nested"
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "iterm.app":
		return "iterm"
	case "ghostty":
		return "ghostty"
	case "wezterm":
		return "wezterm"
	case "apple_terminal":
		return "apple"
	case "vscode":
		return "vscode"
	case "warpterminal":
		return "warp"
	}
	term := os.Getenv("TERM")
	switch {
	case strings.Contains(term, "kitty"):
		return "kitty"
	case strings.Contains(term, "alacritty"):
		return "alacritty"
	case strings.Contains(term, "ghostty"):
		return "ghostty"
	}
	return "other"
}

func telemetryColor() string {
	switch strings.ToLower(os.Getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return "truecolor"
	}
	if strings.Contains(os.Getenv("TERM"), "256") {
		return "256"
	}
	return "ascii"
}

var tomlHeaderRe = regexp.MustCompile(`^\s*\[\[?([A-Za-z0-9_-]+)`)

// telemetryConfigSections sets the bit of each known top-level section
// present in config.toml (telemetry.ConfigSections); values are never read.
func telemetryConfigSections() uint32 {
	p, err := GetUserConfigPath()
	if err != nil {
		return 0
	}
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := tomlHeaderRe.FindStringSubmatch(sc.Text()); m != nil {
			names = append(names, m[1])
		}
	}
	return telemetry.ConfigSectionMask(names...)
}
