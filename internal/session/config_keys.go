package session

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ConfigKey is one config.toml setting a client may read and write through
// `agent-deck config get/set/schema`. The table covers every value the TUI
// Settings panel edits (same labels and defaults as internal/ui/settings_panel.go)
// plus the switches the Mac app needs; writes go through SaveUserConfig, the
// same writer the TUI uses, so both surfaces produce the same file.
type ConfigKey struct {
	Key             string   `json:"key"`
	Section         string   `json:"section"`
	Label           string   `json:"label"`
	Help            string   `json:"help"`
	Type            string   `json:"type"` // bool, int, float, string, enum, list
	Values          []string `json:"values,omitempty"`
	Default         any      `json:"default"`
	RestartRequired bool     `json:"restart_required"`
	Aliases         []string `json:"aliases,omitempty"`

	get func(*UserConfig) any
	set func(*UserConfig, any)
}

func boolRef(b bool) *bool { return &b }

var configKeys = []ConfigKey{
	{Key: "theme", Section: "General", Label: "Theme", Help: "Color theme: dark, light, or follow the system appearance", Type: "enum", Values: []string{"dark", "light", "system"}, Aliases: []string{"ui.theme"},
		get: func(c *UserConfig) any {
			if c.Theme == "light" || c.Theme == "system" {
				return c.Theme
			}
			return "dark"
		},
		set: func(c *UserConfig, v any) { c.Theme = v.(string) }},
	{Key: "default_tool", Section: "General", Label: "Default tool", Help: "Tool preselected for new sessions (empty = none)", Type: "string",
		get: func(c *UserConfig) any { return c.DefaultTool },
		set: func(c *UserConfig, v any) { c.DefaultTool = v.(string) }},
	{Key: "claude.dangerous_mode", Section: "Claude", Label: "Dangerous mode", Help: "Start Claude with --dangerously-skip-permissions", Type: "bool",
		get: func(c *UserConfig) any { return c.Claude.GetDangerousMode() },
		set: func(c *UserConfig, v any) { c.Claude.DangerousMode = boolRef(v.(bool)) }},
	{Key: "claude.config_dir", Section: "Claude", Label: "Config dir", Help: "Global Claude config directory (CLAUDE_CONFIG_DIR); per-account dirs live under [profiles.<name>.claude]", Type: "string",
		get: func(c *UserConfig) any { return c.Claude.ConfigDir },
		set: func(c *UserConfig, v any) { c.Claude.ConfigDir = v.(string) }},
	{Key: "gemini.yolo_mode", Section: "Gemini", Label: "YOLO mode", Help: "Auto-approve all actions", Type: "bool",
		get: func(c *UserConfig) any { return c.Gemini.YoloMode },
		set: func(c *UserConfig, v any) { c.Gemini.YoloMode = v.(bool) }},
	{Key: "codex.yolo_mode", Section: "Codex", Label: "YOLO mode", Help: "Bypass approvals and sandbox", Type: "bool",
		get: func(c *UserConfig) any { return c.Codex.YoloMode },
		set: func(c *UserConfig, v any) { c.Codex.YoloMode = v.(bool) }},
	{Key: "hermes.yolo_mode", Section: "Hermes", Label: "YOLO mode", Help: "Auto-approve all tool calls", Type: "bool",
		get: func(c *UserConfig) any { return c.Hermes.YoloMode },
		set: func(c *UserConfig, v any) { c.Hermes.YoloMode = v.(bool) }},
	{Key: "updates.check_enabled", Section: "Updates", Label: "Check for updates on startup", Help: "Check for a newer agent-deck on startup", Type: "bool",
		get: func(c *UserConfig) any { return c.Updates.GetCheckEnabled() },
		set: func(c *UserConfig, v any) { c.Updates.CheckEnabled = boolRef(v.(bool)) }},
	{Key: "updates.auto_update", Section: "Updates", Label: "Offer to install on startup", Help: "Offer to install an available update on startup", Type: "bool",
		get: func(c *UserConfig) any { return c.Updates.AutoUpdate },
		set: func(c *UserConfig, v any) { c.Updates.AutoUpdate = v.(bool) }},
	{Key: "updates.auto_install", Section: "Updates", Label: "Install updates automatically", Help: "Install updates without asking", Type: "bool",
		get: func(c *UserConfig) any { return c.Updates.GetAutoInstall() },
		set: func(c *UserConfig, v any) { c.Updates.AutoInstall = boolRef(v.(bool)) }},
	{Key: "updates.auto_restart", Section: "Updates", Label: "Restart automatically after update", Help: "Restart agent-deck after an update is installed", Type: "bool",
		get: func(c *UserConfig) any { return c.Updates.GetAutoRestart() },
		set: func(c *UserConfig, v any) { c.Updates.AutoRestart = boolRef(v.(bool)) }},
	{Key: "logs.max_size_mb", Section: "Logs", Label: "Max file size", Help: "Session log size in MB before it is trimmed", Type: "int",
		get: func(c *UserConfig) any { return positiveOr(c.Logs.MaxSizeMB, 10) },
		set: func(c *UserConfig, v any) { c.Logs.MaxSizeMB = v.(int) }},
	{Key: "logs.max_lines", Section: "Logs", Label: "Lines to keep", Help: "Lines kept when a session log is trimmed", Type: "int",
		get: func(c *UserConfig) any { return positiveOr(c.Logs.MaxLines, 10000) },
		set: func(c *UserConfig, v any) { c.Logs.MaxLines = v.(int) }},
	{Key: "logs.remove_orphans", Section: "Logs", Label: "Remove orphan logs", Help: "Delete logs of sessions that no longer exist", Type: "bool",
		get: func(c *UserConfig) any { return c.Logs.GetRemoveOrphans() },
		set: func(c *UserConfig, v any) { c.Logs.RemoveOrphans = boolRef(v.(bool)) }},
	{Key: "global_search.enabled", Section: "Global search", Label: "Enabled", Help: "Search message content across Claude sessions", Type: "bool", RestartRequired: true,
		get: func(c *UserConfig) any { return c.GlobalSearch.GetEnabled() },
		set: func(c *UserConfig, v any) { c.GlobalSearch.Enabled = boolRef(v.(bool)) }},
	{Key: "global_search.tier", Section: "Global search", Label: "Tier", Help: "Search index tier", Type: "enum", Values: []string{"auto", "instant", "balanced"}, RestartRequired: true,
		get: func(c *UserConfig) any {
			if c.GlobalSearch.Tier == "instant" || c.GlobalSearch.Tier == "balanced" {
				return c.GlobalSearch.Tier
			}
			return "auto"
		},
		set: func(c *UserConfig, v any) { c.GlobalSearch.Tier = v.(string) }},
	{Key: "global_search.recent_days", Section: "Global search", Label: "Recent days", Help: "Only index conversations this many days old (0 = all)", Type: "int", RestartRequired: true,
		get: func(c *UserConfig) any { return c.GlobalSearch.RecentDays },
		set: func(c *UserConfig, v any) { c.GlobalSearch.RecentDays = v.(int) }},
	{Key: "preview.show_output", Section: "Preview", Label: "Show Output", Help: "Terminal output in preview", Type: "bool",
		get: func(c *UserConfig) any { return c.GetShowOutput() },
		set: func(c *UserConfig, v any) { c.Preview.ShowOutput = boolRef(v.(bool)) }},
	{Key: "preview.show_analytics", Section: "Preview", Label: "Show Analytics", Help: "Claude analytics panel", Type: "bool",
		get: func(c *UserConfig) any { return c.GetShowAnalytics() },
		set: func(c *UserConfig, v any) { c.Preview.ShowAnalytics = boolRef(v.(bool)) }},
	{Key: "preview.show_notes", Section: "Preview", Label: "Show Notes", Help: "Session notes in preview", Type: "bool",
		get: func(c *UserConfig) any { return c.GetShowNotes() },
		set: func(c *UserConfig, v any) { c.Preview.ShowNotes = boolRef(v.(bool)) }},
	{Key: "preview.notes_output_split", Section: "Preview", Label: "Notes/Output split", Help: "Share of the preview given to notes, 0.10 to 0.90", Type: "float",
		get: func(c *UserConfig) any { return c.Preview.GetNotesOutputSplit() },
		set: func(c *UserConfig, v any) { c.Preview.NotesOutputSplit = v.(float64) }},
	{Key: "sync_title", Section: "Sessions", Label: "Sync title", Help: "Let the agent rename the session (off = keep your title)", Type: "bool",
		get: func(c *UserConfig) any { return c.GetSyncTitle() },
		set: func(c *UserConfig, v any) { c.SyncTitle = boolRef(v.(bool)) }},
	{Key: "maintenance.enabled", Section: "Maintenance", Label: "Enabled", Help: "Run the automatic maintenance worker", Type: "bool",
		get: func(c *UserConfig) any { return c.Maintenance.Enabled },
		set: func(c *UserConfig, v any) { c.Maintenance.Enabled = v.(bool) }},
	{Key: "system_stats.enabled", Section: "System stats", Label: "Enabled", Help: "Show CPU, RAM, etc. in status bar", Type: "bool",
		get: func(c *UserConfig) any { return c.SystemStats.GetEnabled() },
		set: func(c *UserConfig, v any) { c.SystemStats.Enabled = boolRef(v.(bool)) }},
	{Key: "system_stats.refresh_seconds", Section: "System stats", Label: "Refresh interval", Help: "Seconds between stats refreshes", Type: "int",
		get: func(c *UserConfig) any { return c.SystemStats.GetRefreshSeconds() },
		set: func(c *UserConfig, v any) { c.SystemStats.RefreshSeconds = v.(int) }},
	{Key: "system_stats.format", Section: "System stats", Label: "Format", Help: "Status bar stats format", Type: "enum", Values: []string{"compact", "full", "minimal"},
		get: func(c *UserConfig) any { return c.SystemStats.GetFormat() },
		set: func(c *UserConfig, v any) { c.SystemStats.Format = v.(string) }},
	{Key: "system_stats.show", Section: "System stats", Label: "Visible stats", Help: "Stats shown: any of cpu, ram, disk, network, gpu, load (comma-separated)", Type: "list", Values: []string{"cpu", "ram", "disk", "network", "gpu", "load"},
		get: func(c *UserConfig) any { return c.SystemStats.GetShow() },
		set: func(c *UserConfig, v any) { c.SystemStats.Show = v.([]string) }},
	{Key: "display.show_session_timestamps", Section: "Display", Label: "Show session timestamps", Help: "Show created/active times on session rows", Type: "bool",
		get: func(c *UserConfig) any { return c.Display.ShowSessionTimestamps },
		set: func(c *UserConfig, v any) { c.Display.ShowSessionTimestamps = v.(bool) }},
	{Key: "display.show_pane_titles", Section: "Display", Label: "Show pane titles", Help: "Show the live pane title on session rows", Type: "bool",
		get: func(c *UserConfig) any { return c.Display.ShowPaneTitles },
		set: func(c *UserConfig, v any) { c.Display.ShowPaneTitles = v.(bool) }},
	{Key: "ui.show_only_installed_tools", Section: "Tool picker", Label: "Show only installed tools", Help: "Hide tools that are not on PATH from the new-session picker", Type: "bool",
		get: func(c *UserConfig) any { return c.UI.ShowOnlyInstalledTools },
		set: func(c *UserConfig, v any) { c.UI.ShowOnlyInstalledTools = v.(bool) }},
	{Key: "ui.embedded_terminal", Section: "Interface", Label: "Embedded terminal", Help: "Show the session terminal embedded next to the list", Type: "bool", RestartRequired: true,
		get: func(c *UserConfig) any { return c.UI.GetEmbeddedTerminal() },
		set: func(c *UserConfig, v any) { c.UI.EmbeddedTerminal = boolRef(v.(bool)) }},
	{Key: "ui.sidebar_density", Section: "Interface", Label: "Sidebar density", Help: "Embedded sidebar density", Type: "enum", Values: []string{SidebarDensityFull, SidebarDensityCompact, SidebarDensityMinimal, SidebarDensityAuto},
		get: func(c *UserConfig) any { return c.UI.GetSidebarDensity() },
		set: func(c *UserConfig, v any) { c.UI.SidebarDensity = v.(string) }},
	// Not in the TUI panel: the switches the Mac app surface reads.
	{Key: "recall.enabled", Section: "Advanced", Label: "Recall", Help: "Cross-harness conversation store; gates recall timeline/follow (docs/recall.md)", Type: "bool",
		get: func(c *UserConfig) any { return c.Recall.GetEnabled() },
		set: func(c *UserConfig, v any) { c.Recall.Enabled = boolRef(v.(bool)) }},
	{Key: "macapp.plugins", Section: "Advanced", Label: "Mac app plugins", Help: "Enable limits --json and events publish for macapp.* (docs/macapp-core.md)", Type: "bool",
		get: func(c *UserConfig) any { return c.Macapp.Plugins },
		set: func(c *UserConfig, v any) { c.Macapp.Plugins = v.(bool) }},
	{Key: "macapp.transcript_events", Section: "Advanced", Label: "Transcript events", Help: "Notify daemon publishes session.transcript frames when a transcript grows", Type: "bool", RestartRequired: true,
		get: func(c *UserConfig) any { return c.Macapp.TranscriptEvents },
		set: func(c *UserConfig, v any) { c.Macapp.TranscriptEvents = v.(bool) }},
	{Key: "core.daemon", Section: "Advanced", Label: "Core daemon", Help: "Route --json=envelope registry commands through agent-deck daemon serve (docs/daemon-protocol.md)", Type: "bool",
		get: func(c *UserConfig) any { return c.Core.Daemon },
		set: func(c *UserConfig, v any) { c.Core.Daemon = v.(bool) }},
}

func positiveOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// ConfigKeys returns the schema of every settable key. Default is what the
// getter answers for an empty config, so it can never drift from the code.
func ConfigKeys() []ConfigKey {
	out := append([]ConfigKey(nil), configKeys...)
	for i := range out {
		out[i].Default = out[i].get(&UserConfig{})
	}
	return out
}

// LookupConfigKey finds a key by name or alias.
func LookupConfigKey(name string) (ConfigKey, bool) {
	for _, k := range configKeys {
		if k.Key == name {
			return k, true
		}
		for _, a := range k.Aliases {
			if a == name {
				return k, true
			}
		}
	}
	return ConfigKey{}, false
}

// Get returns the effective value (the default when unset).
func (k ConfigKey) Get(c *UserConfig) any {
	if c == nil {
		c = &UserConfig{}
	}
	return k.get(c)
}

// Parse converts a CLI string into the key's typed value.
func (k ConfigKey) Parse(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	switch k.Type {
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			switch strings.ToLower(raw) {
			case "on", "yes":
				return true, nil
			case "off", "no":
				return false, nil
			}
			return nil, fmt.Errorf("%s: %q is not a boolean", k.Key, raw)
		}
		return b, nil
	case "int":
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%s: %q is not a non-negative integer", k.Key, raw)
		}
		return n, nil
	case "float":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", k.Key, raw)
		}
		if k.Key == "preview.notes_output_split" && (f < 0.1 || f > 0.9) {
			return nil, fmt.Errorf("%s: must be between 0.10 and 0.90", k.Key)
		}
		return f, nil
	case "enum":
		for _, v := range k.Values {
			if v == raw {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("%s: %q is not one of %s", k.Key, raw, strings.Join(k.Values, ", "))
	case "list":
		var out []string
		allowed := map[string]bool{}
		for _, v := range k.Values {
			allowed[v] = true
		}
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			if len(allowed) > 0 && !allowed[part] {
				return nil, fmt.Errorf("%s: %q is not one of %s", k.Key, part, strings.Join(k.Values, ", "))
			}
			out = append(out, part)
		}
		sort.SliceStable(out, func(i, j int) bool { return configValueIndex(k.Values, out[i]) < configValueIndex(k.Values, out[j]) })
		return out, nil
	}
	return raw, nil
}

func configValueIndex(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return len(list)
}

// Set writes a typed value into the config.
func (k ConfigKey) Set(c *UserConfig, v any) { k.set(c, v) }

// SetConfigValue loads config.toml, sets one key and saves it through the
// TUI's writer. It returns the value read back after the save.
func SetConfigValue(name, raw string) (ConfigKey, any, error) {
	k, ok := LookupConfigKey(name)
	if !ok {
		return ConfigKey{}, nil, fmt.Errorf("unknown config key %q (see agent-deck config schema)", name)
	}
	v, err := k.Parse(raw)
	if err != nil {
		return k, nil, err
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		return k, nil, err
	}
	if cfg == nil {
		cfg = &UserConfig{}
	}
	k.Set(cfg, v)
	if err := SaveUserConfig(cfg); err != nil {
		return k, nil, err
	}
	ClearUserConfigCache()
	fresh, err := LoadUserConfig()
	if err != nil {
		return k, nil, err
	}
	return k, k.Get(fresh), nil
}
