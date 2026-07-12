package session

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"

	dark "github.com/thiagokokada/dark-mode-go"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/platform"
	"github.com/asheshgoplani/agent-deck/internal/safeio"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// UserConfigFileName is the TOML config file for user preferences
const UserConfigFileName = "config.toml"

// ErrRefusingConfigSectionDrop is returned by SaveUserConfig when the config it
// is asked to write would empty an entire top-level section ([mcps] or [groups])
// that currently has entries on disk. These are the exact sections lost in the
// 2026-06-04 data-loss incident: a partially-constructed config saved over the
// live file silently dropped the whole MCP catalog and group overrides.
//
// S3 data-loss safeguard: a save that zeroes a populated section is almost
// always a bug in the caller (it built a config without loading the existing
// one), not a deliberate "clear everything". Refuse it. A caller that genuinely
// means to clear all MCPs/groups must go through SaveUserConfigWithIntent with
// allowSectionDrop=true so the destructive intent is explicit and greppable.
var ErrRefusingConfigSectionDrop = fmt.Errorf("session: refusing to save config.toml that would drop a populated [mcps] or [groups] section to empty (use SaveUserConfigWithIntent to intentionally clear)")

// UserConfig represents user-facing configuration in TOML format.
//
// TOML serialization: every field must use omitempty (string/bool/slice/map/pointer)
// or omitzero (int/struct) so zero-value fields are not written to disk. Without
// this, SaveUserConfig bloats the file with sections the user never configured.
// TestSaveUserConfig_ZeroValueConfigProducesNoSections enforces this invariant.
type UserConfig struct {
	// DefaultTool is the pre-selected AI tool when creating new sessions
	// Valid values: "claude", "gemini", "opencode", "codex", "pi", or any custom tool name
	// If empty or invalid, defaults to "shell" (no pre-selection)
	DefaultTool string `toml:"default_tool,omitempty"`

	// DefaultPath is the global fallback project directory for `agent-deck add`
	// when no explicit path or group default_path is provided.
	DefaultPath string `toml:"default_path,omitempty"`

	// Hotkeys overrides default keyboard shortcuts in the TUI.
	// Keys are action names, values are key bindings (e.g., "delete" = "backspace").
	// Set an action to "" to explicitly unbind it.
	Hotkeys map[string]string `toml:"hotkeys,omitempty"`

	// Theme sets the color scheme: "dark" (default), "light", or "system"
	Theme string `toml:"theme,omitempty"`

	// Tools defines custom AI tool configurations
	Tools map[string]ToolDef `toml:"tools,omitempty"`

	// MCPDefaultScope sets the default scope for MCP operations
	// Valid values: "local" (default), "global", "user"
	MCPDefaultScope string `toml:"mcp_default_scope,omitempty"`

	// ManageMCPJson controls whether agent-deck writes to .mcp.json in project directories.
	// Set to false to prevent agent-deck from touching any .mcp.json files, which is useful
	// when you manage that file manually or via another tool.
	// Default: true (nil = true)
	ManageMCPJson *bool `toml:"manage_mcp_json,omitempty"`

	// SyncTitle controls whether agent-deck overwrites a session's Title with the
	// agent's own session-name (e.g. Claude's `--name` / `/rename`, issues #572/#697).
	// Tool-agnostic, global switch. Set false to keep the title you gave the session.
	// The per-session TitleLocked flag remains available as a finer-grained override.
	// Default: true (nil = true)
	SyncTitle *bool `toml:"sync_title,omitempty"`

	// GroupSort controls the order of sessions within a group.
	//   "creation"   (default) — fixed creation order; honors K/J manual reorder.
	//   "actionable"           — issue #857 status→recency→Order surfacing.
	// Empty or unrecognized values normalize to "creation".
	GroupSort string `toml:"group_sort,omitempty"`

	// MCPs defines available MCP servers for the MCP Manager
	// These can be attached/detached per-project via the MCP Manager (M key)
	MCPs map[string]MCPDef `toml:"mcps,omitempty"`

	// Plugins defines available Claude Code plugins for per-session attach
	// (RFC docs/rfc/PLUGIN_ATTACH.md). Catalog-only in v1: every name passed
	// via `--plugin <name>` must resolve to an entry here. Each entry maps a
	// short catalog name (e.g. "octopus") to a Claude Code plugin id
	// (`<name>@<source>`) plus per-plugin policy (auto-install, channel link).
	Plugins map[string]PluginDef `toml:"plugins,omitempty"`

	// Claude defines Claude Code integration settings
	Claude ClaudeSettings `toml:"claude,omitempty"`

	// Profiles defines optional per-profile overrides.
	// Example:
	// [profiles.work.claude]
	// config_dir = "~/.claude-work"
	Profiles map[string]ProfileSettings `toml:"profiles,omitempty"`

	// Groups defines optional per-group overrides.
	// Example:
	// [groups."my-group".claude]
	// config_dir = "~/.claude-my-group"
	Groups map[string]GroupSettings `toml:"groups,omitempty"`

	// GroupDefaults holds defaults applied to NEWLY-created groups only.
	// Existing groups (loaded from state.db) are never affected.
	GroupDefaults GroupDefaultsSettings `toml:"group_defaults,omitempty"`

	// Conductors defines optional per-conductor overrides.
	// Keyed by conductor name (matches Instance.Title minus "conductor-" prefix).
	// Mirrors Groups — see ConductorOverrides for the sub-table shape.
	// Closes issue #602.
	// Example:
	// [conductors.gsd-v154.claude]
	// config_dir = "~/.claude-work"
	// env_file   = "~/git/work/.envrc"
	Conductors map[string]ConductorOverrides `toml:"conductors,omitempty"`

	// Gemini defines Gemini CLI integration settings
	Gemini GeminiSettings `toml:"gemini,omitempty"`

	// OpenCode defines OpenCode CLI integration settings
	OpenCode OpenCodeSettings `toml:"opencode,omitempty"`

	// Codex defines Codex CLI integration settings
	Codex CodexSettings `toml:"codex,omitempty"`

	// Copilot defines GitHub Copilot CLI integration settings (Issue #556)
	Copilot CopilotSettings `toml:"copilot,omitempty"`

	// Crush defines charmbracelet/crush CLI integration settings (Issue #940)
	Crush CrushSettings `toml:"crush,omitempty"`

	// Hermes defines Hermes Agent CLI integration settings
	Hermes HermesSettings `toml:"hermes,omitempty"`

	// Worktree defines git worktree preferences
	Worktree WorktreeSettings `toml:"worktree,omitempty"`

	// GlobalSearch defines global conversation search settings
	GlobalSearch GlobalSearchSettings `toml:"global_search,omitempty"`

	// Logs defines session log management settings
	Logs LogSettings `toml:"logs,omitempty"`

	// MCPPool defines HTTP MCP pool settings for shared MCP servers
	MCPPool MCPPoolSettings `toml:"mcp_pool,omitempty"`

	// Updates defines auto-update settings
	Updates UpdateSettings `toml:"updates,omitempty"`

	// Preview defines preview pane display settings
	Preview PreviewSettings `toml:"preview,omitempty"`

	// Experiments defines experiment folder settings for 'try' command
	Experiments ExperimentsSettings `toml:"experiments,omitempty"`

	// Notifications defines waiting session notification bar settings
	Notifications NotificationsConfig `toml:"notifications,omitempty"`

	// Instances defines multiple instance behavior settings
	Instances InstanceSettings `toml:"instances,omitempty"`

	// Shell defines global shell environment settings for sessions
	Shell ShellSettings `toml:"shell,omitempty"`

	// Maintenance defines automatic maintenance worker settings
	Maintenance MaintenanceSettings `toml:"maintenance,omitempty"`

	// Status defines session status detection settings
	Status StatusSettings `toml:"status,omitempty"`

	// Conductor defines conductor (meta-agent orchestration) settings
	Conductor ConductorSettings `toml:"conductor,omitempty"`

	// Tmux defines tmux option overrides applied to every session
	Tmux TmuxSettings `toml:"tmux,omitempty"`

	// Docker defines Docker sandbox settings for containerized sessions
	Docker DockerSettings `toml:"docker,omitempty"`

	// Fork defines quick-fork (f) and fork-dialog (Shift+F) default behavior.
	Fork ForkSettings `toml:"fork,omitempty"`

	// Remotes defines named SSH remote agent-deck instances
	Remotes map[string]RemoteConfig `toml:"remotes,omitempty"`

	// OpenClaw defines OpenClaw gateway integration settings
	OpenClaw OpenClawSettings `toml:"openclaw,omitempty"`

	// Display defines rendering and display settings
	Display DisplaySettings `toml:"display,omitempty"`

	// Costs defines cost tracking and budget settings
	Costs CostsSettings `toml:"costs,omitempty"`

	// SystemStats defines system stats display settings (CPU, RAM, etc.)
	SystemStats SystemStatsSettings `toml:"system_stats,omitempty"`

	// Watcher defines event watcher settings
	Watcher WatcherSettings `toml:"watcher,omitempty"`

	// Feedback defines in-product feedback prompt settings (v1.7.38+).
	// Mirrors the opt-out in ~/.agent-deck/feedback-state.json so it is visible
	// to the user and editable without running `agent-deck feedback`.
	Feedback FeedbackSettings `toml:"feedback,omitempty"`

	// Terminal defines outer-terminal chrome settings — sequences agent-deck
	// writes directly to the host terminal (iTerm2 badge, etc), distinct
	// from anything tmux draws. Empty/absent uses defaults; see TerminalSettings.
	Terminal TerminalSettings `toml:"terminal,omitempty"`

	// Web defines `agent-deck web` HTTP server settings.
	Web WebSettings `toml:"web,omitempty"`

	// UI defines TUI layout settings (split ratios, etc).
	UI UISettings `toml:"ui,omitempty"`

	// SelfHeal defines self-heal supervision settings (SELF-HEAL-DESIGN.md).
	// Stage 1 (v1.9.67) is observe-only: it logs what it WOULD do, takes no
	// action. See SelfHealSettings.
	SelfHeal SelfHealSettings `toml:"selfheal,omitempty"`
}

// SelfHealSettings controls the self-heal supervision policy (SELF-HEAL-DESIGN.md
// §3.7, §6). The shipped default is fully observe-only: it detects truly-stuck
// sessions, exercises the safety state-machine, and LOGS what it would do —
// taking ZERO recovery action. Modes single_action / full are DEFINED but GUARDED
// (they refuse to act) until Stages 2-3 are re-approved by Ashesh + the three §9
// gap-fixes land.
type SelfHealSettings struct {
	// Enabled is the global kill switch (§3.7). When false (the default),
	// self-heal does nothing at all — not even observe-mode logging. Set true to
	// run the observe-only Stage 1.
	Enabled bool `toml:"enabled,omitempty"`

	// Mode is the authority level: "observe" (default, the only acting mode in
	// v1.9.67 — logs would_have, takes no action), "single_action" / "full"
	// (Stages 2-3, DEFINED but GUARDED, refuse to act). An unknown/empty value
	// is normalized to "observe".
	Mode string `toml:"mode,omitempty"`

	// AuditPath overrides where the durable NDJSON audit log lands. Empty uses
	// the per-profile default under the agent-deck data dir (see
	// SelfHealAuditPath). The audit is the dataset reviewed over the ≥1-week
	// observe window before any Stage-2 re-approval.
	AuditPath string `toml:"audit_path,omitempty"`

	// PerSessionPerWindow overrides the per-session recovery cap (default 2 / 6h;
	// auth_401 is always 1). 0 uses the default. Starting dial; tuned from
	// observe data.
	PerSessionPerWindow int `toml:"per_session_per_window,omitzero"`

	// GlobalPerHour overrides the fleet-wide hourly recovery cap (default 5 =
	// TriageMaxPerHour). 0 uses the default.
	GlobalPerHour int `toml:"global_per_hour,omitzero"`

	// OptOutGroups lists group paths that opt OUT of self-heal entirely
	// (deliberate long-waiting stream leads, sensitive scopes — §3.7). Checked
	// in the stuck predicate as a quick disqualifier.
	OptOutGroups []string `toml:"opt_out_groups,omitempty"`

	// OptOutSessions lists session ids/titles that opt OUT of self-heal.
	OptOutSessions []string `toml:"opt_out_sessions,omitempty"`
}

// SelfHealMode normalizes the configured mode to a known value. Empty / unknown
// → "observe" (the safe default). Used by the daemon when constructing the
// engine. The string return matches selfheal.Mode values.
func (s SelfHealSettings) SelfHealMode() string {
	switch s.Mode {
	case "single_action", "full":
		return s.Mode
	default:
		return "observe"
	}
}

// IsGroupOptedOut reports whether a group path opts out of self-heal.
func (s SelfHealSettings) IsGroupOptedOut(groupPath string) bool {
	for _, g := range s.OptOutGroups {
		if g != "" && g == groupPath {
			return true
		}
	}
	return false
}

// IsSessionOptedOut reports whether a session (by id or title) opts out.
func (s SelfHealSettings) IsSessionOptedOut(id, title string) bool {
	for _, sn := range s.OptOutSessions {
		if sn == "" {
			continue
		}
		if sn == id || sn == title {
			return true
		}
	}
	return false
}

// UISettings controls TUI layout proportions.
// See issue #1092.
type UISettings struct {
	// PreviewPct is the percentage of horizontal width allocated to the
	// preview pane (sessions list gets the remainder). Valid range: 10-90.
	// Default: 65 (current behavior — sessions 35 / preview 65).
	// Adjustable at runtime via < and > keybindings (5% step).
	PreviewPct int `toml:"preview_pct,omitzero"`

	// ITermOpenAs controls whether Shift+Enter pops the focused session
	// into a new iTerm2 *tab* or a new iTerm2 *window* on macOS. Valid
	// values: "tab", "window". Empty defaults to "tab" (iTerm's natural
	// UX). Issue #1100, follow-up to #1098 — credit @ddorman-dn.
	ITermOpenAs string `toml:"iterm_open_as,omitempty"`
	// RemoteLatencyRefreshSecs sets how often the TUI re-measures the
	// round-trip latency to each configured remote (issue #1103). Valid
	// range: 2-300. Default: matches [system_stats].refresh_seconds (5s)
	// so the latency marker ticks alongside CPU/RAM/load.
	RemoteLatencyRefreshSecs int `toml:"remote_latency_refresh_secs,omitzero"`
	// RemoteSessionRefreshSecs sets how often the TUI re-fetches the remote
	// session list over SSH (issue #1170). Remote sessions created after the
	// TUI launched were invisible until quit+relaunch; this is the poll
	// cadence that reconciles the list. Valid range: 5-300. Default: 15s,
	// tightening the visibility latency reported on v1.9.30.
	RemoteSessionRefreshSecs int `toml:"remote_session_refresh_secs,omitzero"`

	// ShowOnlyInstalledTools, when true, hides tools from the new-session
	// dialogs (TUI + web) whose command does not resolve on the host PATH
	// (issue #1259). Default false: no PATH probing happens and the dialogs are
	// byte-identical to before. shell is always shown; if nothing else resolves
	// the dialog falls back to showing all tools plus a one-line hint. This is a
	// display filter only — `agent-deck launch -c <tool>` still spawns a hidden
	// tool.
	ShowOnlyInstalledTools bool `toml:"show_only_installed_tools,omitempty"`

	// HiddenTools lists tool names to hide from the new-session picker (TUI + web).
	// Denylist: absent or empty shows every tool (subject to show_only_installed_tools).
	// shell is always shown and cannot be hidden.
	HiddenTools []string `toml:"hidden_tools,omitempty"`

	// Footer controls the style of the bottom hint bar. Valid values:
	//   "full" (default)    — the historic verbose bar: filled key chips,
	//                         width-adaptive, advertising every action. This is
	//                         today's behavior and stays the default so the look
	//                         never changes without an explicit opt-in.
	//   "curated"           — lighter, dim inline text advertising only the
	//                         actions relevant to the selected row, with the
	//                         settings and help keys always last (opt-in).
	//   "compact"           — force the abbreviated chip tier regardless of width.
	//   "minimal"           — force the keys-only tier regardless of width.
	// Empty or unknown values fall back to "full". This is purely a
	// rendering preference (TUI UX initiative, item 1): no keybinding is
	// added, removed, or changed — only what the footer advertises. Every
	// action remains reachable by its key and is fully listed under help (?).
	Footer string `toml:"footer,omitempty"`

	// NewSessionEnterAdvances controls what Enter does on the free-text
	// Name/Branch fields of the new-session dialog. As of the UX top-3 pass this
	// is ON BY DEFAULT (the mechanism shipped opt-in in PR #1295): Enter advances
	// focus to the next field, so typing a name and pressing Enter no longer
	// silently creates a session with all defaults — the #1 reported new-session
	// trap. Ctrl+S is the explicit submit shortcut and submits in BOTH modes;
	// Enter still submits from non-text rows (tool/checkboxes). The pointer lets
	// us distinguish "unset" (nil → default true) from an explicit opt-OUT
	// (`new_session_enter_advances = false` → restores the legacy Enter-submits
	// behavior). Set `= true` (or leave unset) to keep the new default.
	NewSessionEnterAdvances *bool `toml:"new_session_enter_advances"`
}

// normalizeUIHiddenTools lowercases, dedupes, and drops unknown entries from
// [ui].hidden_tools. shell cannot be hidden. Unknown names log a warning.
func normalizeUIHiddenTools(ui *UISettings, customTools map[string]ToolDef) {
	if ui == nil || len(ui.HiddenTools) == 0 {
		return
	}
	known := make(map[string]bool, len(builtinTools())+len(customTools))
	for _, bt := range builtinTools() {
		known[strings.ToLower(strings.TrimSpace(bt.Name))] = true
	}
	for name := range customTools {
		n := strings.ToLower(strings.TrimSpace(name))
		if n != "" {
			known[n] = true
		}
	}

	seen := make(map[string]bool, len(ui.HiddenTools))
	out := make([]string, 0, len(ui.HiddenTools))
	for _, raw := range ui.HiddenTools {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || name == "shell" {
			continue
		}
		if !known[name] {
			registryLog.Warn("ignored unknown hidden_tools entry",
				"name", raw,
				"hint", "use a built-in or custom tool name from config.toml")
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	ui.HiddenTools = out
}

// DefaultPreviewPct is the default preview-pane width percentage.
// Matches the historical hardcoded 0.35 sessions / 0.65 preview split.
const DefaultPreviewPct = 65

// MinPreviewPct and MaxPreviewPct bound the preview width to keep both
// panes usable.
const (
	MinPreviewPct = 10
	MaxPreviewPct = 90
)

// iTerm "open as" modes for Shift+Enter dispatch.
const (
	ITermOpenAsTab     = "tab"
	ITermOpenAsWindow  = "window"
	DefaultITermOpenAs = ITermOpenAsTab
)

// Footer hint-bar styles. See UISettings.Footer.
const (
	FooterCurated = "curated"
	FooterFull    = "full"
	FooterCompact = "compact"
	FooterMinimal = "minimal"
	// DefaultFooter is the historic verbose bar ("full"). Keeping it as the
	// default preserves today's look; curated/compact/minimal are opt-in via
	// config.toml [ui] footer.
	DefaultFooter = FooterFull
)

// GetFooter returns the configured footer style, normalized to one of the
// known values. Empty or unknown input falls back to DefaultFooter
// ("full"). Matching is case-insensitive so users may write "Full" or
// "MINIMAL" in TOML.
func (u UISettings) GetFooter() string {
	switch strings.ToLower(strings.TrimSpace(u.Footer)) {
	case FooterFull:
		return FooterFull
	case FooterCompact:
		return FooterCompact
	case FooterMinimal:
		return FooterMinimal
	case FooterCurated:
		return FooterCurated
	}
	return DefaultFooter
}

// GetPreviewPct returns the configured preview percentage, clamped to
// [MinPreviewPct, MaxPreviewPct]. Falls back to DefaultPreviewPct when
// unset or out of range.
func (u UISettings) GetPreviewPct() int {
	if u.PreviewPct <= 0 {
		return DefaultPreviewPct
	}
	if u.PreviewPct < MinPreviewPct {
		return MinPreviewPct
	}
	if u.PreviewPct > MaxPreviewPct {
		return MaxPreviewPct
	}
	return u.PreviewPct
}

// GetITermOpenAs returns the configured iTerm open mode. Unknown or
// empty values fall through to the default ("tab"). Matching is
// case-insensitive so users can write "Tab" or "WINDOW" in TOML.
func (u UISettings) GetITermOpenAs() string {
	switch strings.ToLower(strings.TrimSpace(u.ITermOpenAs)) {
	case ITermOpenAsWindow:
		return ITermOpenAsWindow
	case ITermOpenAsTab:
		return ITermOpenAsTab
	}
	return DefaultITermOpenAs
}

// Remote session-list poll cadence bounds (issue #1170). The default is
// deliberately tighter than the historical hardcoded 30s so new remote
// sessions surface promptly; the min keeps a floor on SSH frequency.
const (
	DefaultRemoteSessionRefreshSecs = 15
	MinRemoteSessionRefreshSecs     = 5
	MaxRemoteSessionRefreshSecs     = 300
)

// GetRemoteSessionRefreshSecs returns the remote session-list poll interval
// in seconds, clamped to [MinRemoteSessionRefreshSecs,
// MaxRemoteSessionRefreshSecs]. Unset (<= 0) falls back to
// DefaultRemoteSessionRefreshSecs. See issue #1170.
func (u UISettings) GetRemoteSessionRefreshSecs() int {
	val := u.RemoteSessionRefreshSecs
	if val <= 0 {
		return DefaultRemoteSessionRefreshSecs
	}
	if val < MinRemoteSessionRefreshSecs {
		return MinRemoteSessionRefreshSecs
	}
	if val > MaxRemoteSessionRefreshSecs {
		return MaxRemoteSessionRefreshSecs
	}
	return val
}

// GetNewSessionEnterAdvances reports whether Enter on the new-session dialog's
// free-text Name/Branch fields should advance focus (true) instead of
// submitting the form (false). Defaults to true when unset: Enter-advances is
// the default so typing a name + Enter no longer silently submits with all
// defaults. A literal `new_session_enter_advances = false` opts out and
// restores the legacy Enter-submits behavior. Ctrl+S submits in both modes.
func (u UISettings) GetNewSessionEnterAdvances() bool {
	if u.NewSessionEnterAdvances == nil {
		return true // Default: ON (Enter advances; Ctrl+S submits).
	}
	return *u.NewSessionEnterAdvances
}

// GetRemoteLatencyRefreshSecs returns the remote latency refresh interval
// in seconds, clamped to [2, 300]. When the user has not set this value
// it falls back to fallbackSecs (typically the system_stats refresh
// interval, so the latency marker ticks at the same cadence as CPU/RAM
// per #1103). fallbackSecs <= 0 maps to 5.
func (u UISettings) GetRemoteLatencyRefreshSecs(fallbackSecs int) int {
	val := u.RemoteLatencyRefreshSecs
	if val <= 0 {
		val = fallbackSecs
	}
	if val < 2 {
		val = 5
	}
	if val > 300 {
		val = 300
	}
	return val
}

// WebSettings configures the `agent-deck web` HTTP server.
type WebSettings struct {
	// MutationsEnabled controls whether POST/PATCH/DELETE endpoints accept
	// requests. nil (omitted) defaults to true. Forced off by --read-only.
	MutationsEnabled *bool `toml:"mutations_enabled,omitempty"`
}

// FeedbackSettings controls the in-product feedback prompts.
// When Disabled is true, neither the auto-prompt (TUI) nor the post-launch
// auto-trigger (CLI, if any) will fire. Explicit `agent-deck feedback`
// invocations still run but show a re-enable prompt first. v1.7.38+.
type FeedbackSettings struct {
	// Disabled suppresses all passive feedback prompts when true.
	// Defaults to false. Set by RecordOptOut paths; cleared on re-enable.
	Disabled bool `toml:"disabled,omitempty"`
}

// OpenClawSettings configures the OpenClaw gateway connection.
type OpenClawSettings struct {
	// GatewayURL is the WebSocket URL of the OpenClaw gateway (default: "ws://127.0.0.1:31337")
	GatewayURL string `toml:"gateway_url,omitempty"`

	// Password is the gateway authentication password.
	// Supports env var references (e.g. "$OPENCLAW_PASSWORD" or "${OPENCLAW_PASSWORD}").
	// Falls back to OPENCLAW_PASSWORD env var if not set.
	Password string `toml:"password,omitempty"`

	// AutoSync syncs OpenClaw agents as agent-deck sessions on TUI startup
	AutoSync bool `toml:"auto_sync,omitempty"`

	// GroupName is the agent-deck group name for OpenClaw sessions (default: "openclaw")
	GroupName string `toml:"group_name,omitempty"`
}

// RemoteConfig defines a remote agent-deck instance accessible via SSH.
type RemoteConfig struct {
	// Host is the SSH destination (e.g., "user@host" or "user@host:port")
	Host string `toml:"host,omitempty"`

	// AgentDeckPath is the path to agent-deck binary on the remote (default: "agent-deck")
	AgentDeckPath string `toml:"agent_deck_path,omitempty"`

	// Profile is the remote profile to use (default: "default")
	Profile string `toml:"profile,omitempty"`
}

// GetAgentDeckPath returns the agent-deck binary path, defaulting to "agent-deck".
func (rc RemoteConfig) GetAgentDeckPath() string {
	if rc.AgentDeckPath != "" {
		return rc.AgentDeckPath
	}
	return "agent-deck"
}

// GetProfile returns the remote profile, defaulting to "default".
func (rc RemoteConfig) GetProfile() string {
	if rc.Profile != "" {
		return rc.Profile
	}
	return "default"
}

// ProfileSettings defines per-profile configuration overrides.
type ProfileSettings struct {
	// Claude defines Claude Code overrides for a specific profile.
	Claude ProfileClaudeSettings `toml:"claude,omitempty"`
	// Codex defines Codex CLI overrides for a specific profile.
	Codex ProfileCodexSettings `toml:"codex,omitempty"`
	// Costs defines profile-specific cost-tracking overrides.
	// Nil pointer means "no [profiles.<name>.costs] block in TOML"; the
	// resolver falls through to global [costs] settings.
	Costs *ProfileCosts `toml:"costs,omitempty"`
}

// ProfileClaudeSettings defines profile-specific Claude overrides.
type ProfileClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for this profile only.
	ConfigDir string `toml:"config_dir,omitempty"`
}

// ProfileCodexSettings defines profile-specific Codex overrides.
type ProfileCodexSettings struct {
	// ConfigDir overrides [codex].config_dir for this profile only.
	ConfigDir string `toml:"config_dir,omitempty"`
}

// GroupSettings defines per-group configuration overrides.
type GroupSettings struct {
	// Create ensures the group exists on startup.
	Create bool `toml:"create,omitempty"`
	// DefaultPath sets the default working directory for new sessions in this group.
	DefaultPath string `toml:"default_path,omitempty"`
	// Claude defines Claude Code overrides for a specific group.
	Claude GroupClaudeSettings `toml:"claude,omitempty"`
	// Hermes defines Hermes overrides for a specific group.
	Hermes GroupHermesSettings `toml:"hermes,omitempty"`
}

// GroupDefaultsSettings carries [group_defaults] — defaults stamped onto new
// groups at creation time. Distinct from per-group [groups."<path>"] overrides.
type GroupDefaultsSettings struct {
	// MaxConcurrent is the max_concurrent value assigned to new groups created
	// via `group create`, the TUI dialog, the web API, and the launch/session
	// auto-create paths. Pointer to distinguish:
	//   nil       → unset → built-in serial default (1)  [byte-for-byte v1.9.1]
	//   *0        → new groups are unlimited
	//   *N (N>0)  → new groups capped at N
	// An explicit `group create --max-concurrent` flag overrides this.
	MaxConcurrent *int `toml:"max_concurrent,omitempty"`
}

// GroupClaudeSettings defines group-specific Claude overrides.
//
// The key surface deliberately mirrors ConductorClaudeSettings (CFG-08
// established the two blocks as mirrors); keep them in sync when adding
// keys. New keys use omitempty so SaveUserConfig does not emit zero-value
// fields into every group stanza (see issue #1360).
type GroupClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for sessions in this group.
	ConfigDir string `toml:"config_dir,omitempty"`

	// EnvFile overrides [claude].env_file for sessions in this group.
	EnvFile string `toml:"env_file,omitempty"`

	// Command overrides [claude].command for sessions in this group
	// (e.g. a wrapper like "claude-vertex"). Same parity Hermes already
	// has via GroupHermesSettings.Command. Resolution:
	// conductor > group (ancestor-walking) > global [claude].command > "claude".
	Command string `toml:"command,omitempty"`

	// Model is the model default for sessions in this group (e.g.
	// "claude-sonnet-4-6" or an alias like "sonnet"). An explicit
	// per-session model (CLI --model, new-session dialog) wins; empty
	// falls through (#1172 semantics).
	Model string `toml:"model,omitempty"`

	// Env is an inline env map exported in the spawn command AFTER the
	// env_file source, so an inline key deterministically wins over the
	// same key from the file. Precedent: [tools.X].env.
	Env map[string]string `toml:"env,omitempty"`

	// Skills lists declarative skill-loadout entries ("<source>/<name>")
	// to attach to sessions in this group. Reserved schema home for the
	// loadout follow-up; surfaced by `group show --resolved`.
	Skills []string `toml:"skills,omitempty"`

	// MCPs lists [mcps.X] catalog names to attach to sessions in this
	// group. Reserved schema home for the loadout follow-up.
	MCPs []string `toml:"mcps,omitempty"`
}

// GroupHermesSettings defines group-specific Hermes overrides.
type GroupHermesSettings struct {
	Command      string `toml:"command,omitempty"`
	EnvFile      string `toml:"env_file,omitempty"`
	YoloMode     bool   `toml:"yolo_mode,omitempty"`
	GatewayURL   string `toml:"gateway_url,omitempty"`
	DashboardURL string `toml:"dashboard_url,omitempty"`
	APITokenEnv  string `toml:"api_token_env,omitempty"`
}

// ConductorOverrides defines per-conductor configuration overrides.
// Mirrors GroupSettings — conductors are first-class entities keyed by
// conductor name (derived from Instance.Title via strings.TrimPrefix at the
// call site, same pattern as env.go getConductorEnv).
//
// Named ConductorOverrides (not ConductorSettings) to avoid collision with
// the pre-existing global [conductor] meta-agent orchestration block
// declared in conductor.go:49 (heartbeat, telegram, slack, discord).
// Closes issue #602.
type ConductorOverrides struct {
	// Claude defines Claude Code overrides for a specific conductor.
	Claude ConductorClaudeSettings `toml:"claude,omitempty"`
	// Hermes defines Hermes overrides for a specific conductor.
	Hermes ConductorHermesSettings `toml:"hermes,omitempty"`
}

// ConductorClaudeSettings defines conductor-specific Claude overrides.
// Semantics mirror GroupClaudeSettings — ExpandPath is applied on read via
// GetConductorClaudeConfigDir; env_file resolution is deferred to the spawn
// builder (resolvePath handles path expansion at use).
type ConductorClaudeSettings struct {
	// ConfigDir overrides [claude].config_dir for this conductor only.
	ConfigDir string `toml:"config_dir,omitempty"`

	// EnvFile is sourced before claude exec for this conductor.
	// Matches CFG-03 semantics — missing file logs a warning, does not block.
	EnvFile string `toml:"env_file,omitempty"`

	// Command overrides [claude].command for this conductor only.
	// Mirrors GroupClaudeSettings.Command; conductor beats group.
	Command string `toml:"command,omitempty"`

	// Model is the model default for this conductor's sessions. An
	// explicit per-session model wins; empty falls through (#1172).
	Model string `toml:"model,omitempty"`

	// Env is an inline env map exported AFTER the env_file source and
	// AFTER the group env map (conductor wins per key on conflict).
	Env map[string]string `toml:"env,omitempty"`

	// Skills lists declarative skill-loadout entries ("<source>/<name>").
	// Reserved schema home for the loadout follow-up.
	Skills []string `toml:"skills,omitempty"`

	// MCPs lists [mcps.X] catalog names. Reserved schema home for the
	// loadout follow-up.
	MCPs []string `toml:"mcps,omitempty"`
}

// ConductorHermesSettings defines conductor-specific Hermes overrides.
type ConductorHermesSettings struct {
	Command      string `toml:"command,omitempty"`
	EnvFile      string `toml:"env_file,omitempty"`
	YoloMode     bool   `toml:"yolo_mode,omitempty"`
	GatewayURL   string `toml:"gateway_url,omitempty"`
	DashboardURL string `toml:"dashboard_url,omitempty"`
	APITokenEnv  string `toml:"api_token_env,omitempty"`
}

// MCPPoolSettings defines HTTP MCP pool configuration
type MCPPoolSettings struct {
	// Enabled enables HTTP pool mode (default: false)
	Enabled bool `toml:"enabled,omitempty"`

	// AutoStart starts pool when agent-deck launches (default: true)
	AutoStart *bool `toml:"auto_start,omitempty"`

	// PortStart is the first port in the pool range (default: 8001)
	PortStart int `toml:"port_start,omitzero"`

	// PortEnd is the last port in the pool range (default: 8050)
	PortEnd int `toml:"port_end,omitzero"`

	// StartOnDemand starts MCPs lazily on first attach (default: false)
	StartOnDemand bool `toml:"start_on_demand,omitempty"`

	// ShutdownOnExit stops HTTP servers when agent-deck quits (default: true)
	ShutdownOnExit *bool `toml:"shutdown_on_exit,omitempty"`

	// PoolMCPs is the list of MCPs to run in pool mode
	// Empty = auto-detect common MCPs (memory, exa, firecrawl, etc.)
	PoolMCPs []string `toml:"pool_mcps,omitempty"`

	// FallbackStdio uses stdio for MCPs without socket support (default: true)
	FallbackStdio *bool `toml:"fallback_to_stdio,omitempty"`

	// ShowStatus shows pool status in TUI (default: true)
	ShowStatus *bool `toml:"show_pool_status,omitempty"`

	// PoolAll pools all MCPs by default (default: false)
	PoolAll bool `toml:"pool_all,omitempty"`

	// ExcludeMCPs excludes specific MCPs from pool when pool_all = true
	ExcludeMCPs []string `toml:"exclude_mcps,omitempty"`

	// SocketWaitTimeout is seconds to wait for socket to become ready (default: 5)
	SocketWaitTimeout int `toml:"socket_wait_timeout,omitzero"`
}

func (p MCPPoolSettings) GetAutoStart() bool {
	if p.AutoStart == nil {
		return true
	}
	return *p.AutoStart
}

func (p MCPPoolSettings) GetShutdownOnExit() bool {
	if p.ShutdownOnExit == nil {
		return true
	}
	return *p.ShutdownOnExit
}

func (p MCPPoolSettings) GetFallbackStdio() bool {
	if p.FallbackStdio == nil {
		return true
	}
	return *p.FallbackStdio
}

func (p MCPPoolSettings) GetShowStatus() bool {
	if p.ShowStatus == nil {
		return true
	}
	return *p.ShowStatus
}

// LogSettings defines log file management configuration
type LogSettings struct {
	// MaxSizeMB is the maximum size in MB before a log file is truncated
	// When a log exceeds this size, it keeps only the last MaxLines lines
	// Default: 10 (10MB)
	MaxSizeMB int `toml:"max_size_mb,omitzero"`

	// MaxLines is the number of lines to keep when truncating
	// Default: 10000
	MaxLines int `toml:"max_lines,omitzero"`

	// RemoveOrphans removes log files for sessions that no longer exist
	// Default: true (nil = true)
	RemoveOrphans *bool `toml:"remove_orphans,omitempty"`

	// DebugLevel sets the minimum log level: "debug", "info", "warn", "error"
	// Default: "info"
	DebugLevel string `toml:"debug_level,omitempty"`

	// DebugFormat sets the log format: "json" (default) or "text"
	DebugFormat string `toml:"debug_format,omitempty"`

	// DebugMaxMB is the max size in MB for debug.log before rotation
	// Default: 10
	DebugMaxMB int `toml:"debug_max_mb,omitzero"`

	// DebugBackups is the number of rotated debug.log files to keep
	// Default: 5
	DebugBackups int `toml:"debug_backups,omitzero"`

	// DebugRetentionDays is the number of days to keep rotated debug logs
	// Default: 10
	DebugRetentionDays int `toml:"debug_retention_days,omitzero"`

	// DebugCompress enables gzip compression for rotated debug logs
	// Default: true
	DebugCompress *bool `toml:"debug_compress,omitempty"`

	// RingBufferMB is the in-memory ring buffer size in MB for crash dumps
	// Default: 10
	RingBufferMB int `toml:"ring_buffer_mb,omitzero"`

	// PprofEnabled starts a pprof server on localhost:6060 when debug mode is active
	// Default: false
	PprofEnabled bool `toml:"pprof_enabled,omitempty"`

	// AggregateIntervalS is the event aggregation flush interval in seconds
	// Default: 30
	AggregateIntervalS int `toml:"aggregate_interval_secs,omitzero"`
}

// UpdateSettings defines auto-update configuration
type UpdateSettings struct {
	// AutoUpdate automatically installs updates without prompting
	// Default: false
	AutoUpdate bool `toml:"auto_update,omitempty"`

	// CheckEnabled enables automatic update checks on startup
	// Default: true (nil = true)
	CheckEnabled *bool `toml:"check_enabled,omitempty"`

	// CheckIntervalHours is how often to check for updates (in hours)
	// Default: 24
	CheckIntervalHours int `toml:"check_interval_hours,omitzero"`

	// NotifyInCLI shows update notification in CLI commands (not just TUI)
	// Default: true (nil = true)
	NotifyInCLI *bool `toml:"notify_in_cli,omitempty"`
}

// GetCheckEnabled returns whether update checks are enabled (default: true).
func (u UpdateSettings) GetCheckEnabled() bool {
	if u.CheckEnabled == nil {
		return true
	}
	return *u.CheckEnabled
}

// GetNotifyInCLI returns whether CLI update notifications are enabled (default: true).
func (u UpdateSettings) GetNotifyInCLI() bool {
	if u.NotifyInCLI == nil {
		return true
	}
	return *u.NotifyInCLI
}

// PreviewSettings defines preview pane configuration
type PreviewSettings struct {
	// ShowOutput shows terminal output in preview pane (including launch animation)
	// Default: true (pointer to distinguish "not set" from "explicitly false")
	ShowOutput *bool `toml:"show_output,omitempty"`

	// ShowAnalytics shows session analytics panel for Claude sessions
	// Default: false (pointer to distinguish "not set" from "explicitly false")
	ShowAnalytics *bool `toml:"show_analytics,omitempty"`

	// ShowNotes shows session notes section in preview pane
	// Default: false (pointer to distinguish "not set" from "explicitly true")
	ShowNotes *bool `toml:"show_notes,omitempty"`

	// Analytics configures which sections to show in the analytics panel
	Analytics AnalyticsDisplaySettings `toml:"analytics,omitempty"`

	// NotesOutputSplit controls vertical space allocation between notes and output
	// in the preview pane when output is visible.
	// Range: 0.1 - 0.9 (fraction reserved for notes). Default: 0.33
	NotesOutputSplit float64 `toml:"notes_output_split,omitzero"`
}

// AnalyticsDisplaySettings configures which analytics sections to display
// All settings use pointers to distinguish "not set" from "explicitly false"
type AnalyticsDisplaySettings struct {
	// ShowContextBar shows the context window usage bar (default: true)
	ShowContextBar *bool `toml:"show_context_bar,omitempty"`

	// ShowTokens shows the token breakdown (In/Out/Cache/Total) (default: false)
	ShowTokens *bool `toml:"show_tokens,omitempty"`

	// ShowSessionInfo shows duration, turns, start time (default: false)
	ShowSessionInfo *bool `toml:"show_session_info,omitempty"`

	// ShowTools shows the top tool calls (default: false)
	ShowTools *bool `toml:"show_tools,omitempty"`

	// ShowCost shows the estimated cost (default: false)
	ShowCost *bool `toml:"show_cost,omitempty"`
}

// ExperimentsSettings defines experiment folder configuration
type ExperimentsSettings struct {
	// Directory is the base directory for experiments
	// Default: ~/src/tries
	Directory string `toml:"directory,omitempty"`

	// DatePrefix adds YYYY-MM-DD- prefix to new experiment folders
	// Default: true (nil = true)
	DatePrefix *bool `toml:"date_prefix,omitempty"`

	// DefaultTool is the AI tool to use for experiment sessions
	// Default: "claude"
	DefaultTool string `toml:"default_tool,omitempty"`
}

// NotificationsConfig configures the waiting session notification bar
type NotificationsConfig struct {
	// Enabled shows notification bar in tmux status (default: true, nil = true)
	Enabled *bool `toml:"enabled,omitempty"`

	// MaxShown is the maximum number of sessions shown in the bar (default: 6)
	MaxShown int `toml:"max_shown,omitzero"`

	// ShowAll displays all sessions (with status icons) instead of only waiting sessions (default: false)
	ShowAll bool `toml:"show_all,omitempty"`

	// Minimal shows a compact icon+count summary instead of session names: ● 2 │ ◐ 3 │ ○ 1
	// When true, key bindings (Ctrl+b 1-6) are disabled. ShowAll is ignored. (default: false)
	Minimal bool `toml:"minimal,omitempty"`

	// TransitionEvents controls whether the transition daemon sends tmux messages
	// to parent sessions when a child transitions (e.g., running → waiting).
	// Default: true (nil = true). Set to false to suppress dispatch globally.
	// Per-session override: Instance.NoTransitionNotify
	TransitionEvents *bool `toml:"transition_events,omitempty"`
}

// GetTransitionEventsEnabled returns whether transition event dispatch is enabled.
// Defaults to true when unset (nil).
func (n NotificationsConfig) GetTransitionEventsEnabled() bool {
	if n.TransitionEvents == nil {
		return true
	}
	return *n.TransitionEvents
}

// InstanceSettings configures multiple agent-deck instance behavior
type InstanceSettings struct {
	// AllowMultiple allows running multiple agent-deck TUI instances for the same profile.
	// When false (default), only one instance can run per profile — a safe default that
	// prevents concurrent reviver/restart loops from tearing down each other's live
	// sessions (issue #1246). When true (explicit opt-in), multiple instances can run,
	// but only the first (primary) manages the notification bar — useful for multi-pane
	// workflows (e.g. PC + phone-over-SSH).
	AllowMultiple *bool `toml:"allow_multiple,omitempty"`

	// FollowCwdOnAttach updates the session's ProjectPath from tmux pane_current_path
	// after returning from attach, and persists the new path.
	// Default: false
	FollowCwdOnAttach *bool `toml:"follow_cwd_on_attach,omitempty"`
}

// GetAllowMultiple returns whether multiple instances are allowed, defaulting to false.
// Single-instance-per-profile is the safe default: it engages the primary-election gate
// so a second instance is rejected, preventing concurrent reviver/restart loops from
// tearing down each other's live sessions (issue #1246). Multi-instance is an explicit
// opt-in via allow_multiple = true.
func (i *InstanceSettings) GetAllowMultiple() bool {
	if i.AllowMultiple == nil {
		return false // Default: single instance per profile (prevents concurrent tear-down)
	}
	return *i.AllowMultiple
}

// GetFollowCwdOnAttach returns whether attach-return CWD follow is enabled.
func (i *InstanceSettings) GetFollowCwdOnAttach() bool {
	if i.FollowCwdOnAttach == nil {
		return false
	}
	return *i.FollowCwdOnAttach
}

// ShellSettings defines shell environment configuration for sessions
type ShellSettings struct {
	// EnvFiles is a list of .env files to source for ALL sessions
	// Paths can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	// Files are sourced in order; later files override earlier ones
	EnvFiles []string `toml:"env_files,omitempty"`

	// InitScript is an optional shell script or command to run before each session
	// Useful for direnv, nvm, pyenv, etc.
	// Can be a file path (e.g., "~/.agent-deck/init.sh") or inline command
	// (e.g., 'eval "$(direnv hook bash)"')
	InitScript string `toml:"init_script,omitempty"`

	// IgnoreMissingEnvFiles silently ignores missing .env files (default: true)
	// When false, sessions will error if an env_file doesn't exist
	IgnoreMissingEnvFiles *bool `toml:"ignore_missing_env_files,omitempty"`

	// ExitToShell, when true, wraps built-in agent spawn commands so that
	// exiting the agent (e.g. `/exit` from Claude Code) drops the pane back to
	// an interactive shell at the same cwd instead of the pane dying / the TUI
	// auto-restarting. This restores the pre-#503 workflow: exit → do shell-only
	// work (aws-vault exec, direnv, …) → `claude --resume` the same session.
	// Default: false (opt-in). Issue #1161, design doc
	// docs/decisions/1161-exit-to-shell-then-resume.md.
	ExitToShell *bool `toml:"exit_to_shell,omitempty"`

	// LaunchShell, when true, wraps agent spawn commands with an interactive
	// shell invocation so that environment variables from ~/.zshrc, ~/.bashrc
	// etc. are available to the agent process. This solves the issue where
	// OpenCode MCP configs with {env:VAR} references fail when launched from
	// the TUI because the agent doesn't inherit the interactive shell's
	// environment.
	// Default: false (opt-in). Issue #1218.
	LaunchShell *bool `toml:"launch_shell,omitempty"`
}

// GetIgnoreMissingEnvFiles returns whether to ignore missing env files, defaulting to true
func (s *ShellSettings) GetIgnoreMissingEnvFiles() bool {
	if s.IgnoreMissingEnvFiles == nil {
		return true // Default: ignore missing files (fail-safe)
	}
	return *s.IgnoreMissingEnvFiles
}

// GetExitToShell returns whether agent sessions should fall back to an
// interactive shell on agent exit, defaulting to false (opt-in). Issue #1161.
func (s *ShellSettings) GetExitToShell() bool {
	if s.ExitToShell == nil {
		return false // Default: OFF (preserve current exit/resume behavior)
	}
	return *s.ExitToShell
}

// GetLaunchShell returns whether agent commands should be wrapped with a shell
// invocation that loads startup files before launch, defaulting to false
// (opt-in). Issue #1218.
func (s *ShellSettings) GetLaunchShell() bool {
	if s.LaunchShell == nil {
		return false // Default: OFF (preserve current direct spawn behavior)
	}
	return *s.LaunchShell
}

// GetShowAnalytics returns whether to show analytics, defaulting to false
func (p *PreviewSettings) GetShowAnalytics() bool {
	if p.ShowAnalytics == nil {
		return false // Default: analytics OFF (opt-in)
	}
	return *p.ShowAnalytics
}

// GetShowOutput returns whether to show terminal output, defaulting to true
func (p *PreviewSettings) GetShowOutput() bool {
	if p.ShowOutput == nil {
		return true // Default: output ON (shows launch animation)
	}
	return *p.ShowOutput
}

// GetAnalyticsSettings returns the analytics display settings with defaults applied
func (p *PreviewSettings) GetAnalyticsSettings() AnalyticsDisplaySettings {
	return p.Analytics
}

// GetShowNotes returns whether to show notes section, defaulting to false
func (p *PreviewSettings) GetShowNotes() bool {
	if p.ShowNotes == nil {
		return false // Default: notes OFF
	}
	return *p.ShowNotes
}

// GetNotesOutputSplit returns notes/output split ratio, clamped to sane bounds.
func (p *PreviewSettings) GetNotesOutputSplit() float64 {
	if p.NotesOutputSplit <= 0 {
		return 0.33
	}
	if p.NotesOutputSplit < 0.1 {
		return 0.1
	}
	if p.NotesOutputSplit > 0.9 {
		return 0.9
	}
	return p.NotesOutputSplit
}

// GetShowContextBar returns whether to show context bar, defaulting to true
func (a *AnalyticsDisplaySettings) GetShowContextBar() bool {
	if a.ShowContextBar == nil {
		return true // Default: ON - useful visual indicator
	}
	return *a.ShowContextBar
}

// GetShowTokens returns whether to show token breakdown, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowTokens() bool {
	if a.ShowTokens == nil {
		return false // Default: OFF - can be noisy
	}
	return *a.ShowTokens
}

// GetShowSessionInfo returns whether to show session info, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowSessionInfo() bool {
	if a.ShowSessionInfo == nil {
		return false // Default: OFF - less useful info
	}
	return *a.ShowSessionInfo
}

// GetShowTools returns whether to show tool calls, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowTools() bool {
	if a.ShowTools == nil {
		return false // Default: OFF - keeps display minimal
	}
	return *a.ShowTools
}

// GetShowCost returns whether to show cost estimate, defaulting to false
func (a *AnalyticsDisplaySettings) GetShowCost() bool {
	if a.ShowCost == nil {
		return false // Default: OFF - can be noisy
	}
	return *a.ShowCost
}

// GetShowOutput returns whether to show terminal output in preview
func (c *UserConfig) GetShowOutput() bool {
	return c.Preview.GetShowOutput()
}

// GetShowAnalytics returns whether to show analytics panel, defaulting to false
func (c *UserConfig) GetShowAnalytics() bool {
	return c.Preview.GetShowAnalytics()
}

// GetShowNotes returns whether to show notes section, defaulting to false
func (c *UserConfig) GetShowNotes() bool {
	return c.Preview.GetShowNotes()
}

// GetSyncTitle returns whether agent-deck may overwrite a session Title with the
// agent's own session-name. Tool-agnostic. Defaults to true (nil = true) so
// existing installs keep the current behavior; set sync_title = false to opt out.
func (c *UserConfig) GetSyncTitle() bool {
	if c.SyncTitle == nil {
		return true
	}
	return *c.SyncTitle
}

// GetGroupSort returns the normalized within-group sort mode: "actionable" only
// when explicitly set, otherwise "creation" (the default).
func (c *UserConfig) GetGroupSort() string {
	if c.GroupSort == "actionable" {
		return "actionable"
	}
	return "creation"
}

// ClaudeSettings defines Claude Code configuration
type ClaudeSettings struct {
	// Command is the Claude CLI command or alias to use (e.g., "claude", "cdw", "cdp")
	// Default: "claude"
	// This allows using shell aliases that set CLAUDE_CONFIG_DIR automatically
	Command string `toml:"command,omitempty"`

	// ConfigDir is the path to Claude's config directory
	// Default: ~/.claude (or CLAUDE_CONFIG_DIR env var)
	ConfigDir string `toml:"config_dir,omitempty"`

	// DangerousMode enables --dangerously-skip-permissions flag for Claude sessions
	// Default: true (nil = use default true, explicitly set false to disable)
	// Power users typically want this enabled for faster iteration
	DangerousMode *bool `toml:"dangerous_mode,omitempty"`

	// AllowDangerousMode enables --allow-dangerously-skip-permissions flag
	// This unlocks bypass as an option without activating it by default.
	// Ignored when dangerous_mode is true (the stronger flag takes precedence).
	// Default: false
	AllowDangerousMode bool `toml:"allow_dangerous_mode,omitempty"`

	// AutoMode enables --permission-mode auto flag for Claude sessions
	// A classifier model reviews commands before they run, blocking scope escalation
	// and hostile-content-driven actions while letting routine work proceed without prompts.
	// Ignored when dangerous_mode is true (the stronger flag takes precedence).
	// Default: false
	AutoMode bool `toml:"auto_mode,omitempty"`

	// ExtraArgs are user-supplied Claude CLI flags used as the New Session
	// dialog default. They are persisted as discrete TOML array entries and
	// copied to Instance.ExtraArgs when a Claude session is created.
	ExtraArgs []string `toml:"extra_args,omitempty"`

	// DefaultModel is the model to preselect for new Claude sessions
	// (e.g., "claude-opus-4-7"). Mirrors [gemini]/[opencode]/[copilot]
	// default_model. When empty, the dialog leaves the model unset and Claude
	// Code falls back to its own default (#1172).
	DefaultModel string `toml:"default_model,omitempty"`

	// UseChrome enables --chrome by default for Claude sessions.
	UseChrome bool `toml:"use_chrome,omitempty"`

	// UseTeammateMode enables --teammate-mode tmux by default for Claude sessions.
	UseTeammateMode bool `toml:"use_teammate_mode,omitempty"`

	// EnvFile is a .env file specific to Claude sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file,omitempty"`

	// HooksEnabled enables Claude Code hooks for real-time status detection.
	// When enabled, agent-deck uses lifecycle hooks (SessionStart, Stop, etc.)
	// for instant, deterministic status updates instead of polling tmux content.
	// Default: true (nil = use default true, set false to disable)
	HooksEnabled *bool `toml:"hooks_enabled,omitempty"`

	// AutoResumeSummary auto-presses Enter on Claude's "Resume from summary"
	// picker that appears after `claude --resume` on long-running sessions
	// (>~250k tokens). Critical for unattended conductors which would
	// otherwise sit frozen on the picker forever (closes #67).
	// Default: true (nil = use default true, set false to disable).
	AutoResumeSummary *bool `toml:"auto_resume_summary,omitempty"`

	// VimMode tells agent-deck the inner Claude Code prompt uses vim keybindings
	// ("editorMode": "vim"). When true, every message send guarantees the
	// composer is in insert mode (Escape + `i`) before delivering text/Enter, so
	// a message sent while the prompt sits in vim NORMAL mode (the default state
	// after a turn finishes) actually submits instead of being typed-but-unsent
	// (issue #1264). Off by default — only enable for sessions running Claude
	// Code with vim editor mode. Other tools and non-vim Claude are unaffected.
	VimMode bool `toml:"vim_mode,omitempty"`
}

// GetVimMode reports whether vim-mode insert-guard sends are enabled. Off by
// default (issue #1264).
func (c *ClaudeSettings) GetVimMode() bool {
	if c == nil {
		return false
	}
	return c.VimMode
}

// GetProfileClaudeConfigDir returns the profile-specific Claude config directory, if configured.
func (c *UserConfig) GetProfileClaudeConfigDir(profile string) string {
	if c == nil || profile == "" || c.Profiles == nil {
		return ""
	}
	profileCfg, ok := c.Profiles[profile]
	if !ok || profileCfg.Claude.ConfigDir == "" {
		return ""
	}
	return ExpandPath(profileCfg.Claude.ConfigDir)
}

// GetGroupClaudeConfigDir returns the group-specific Claude config directory,
// walking ancestor groups when the exact path has no override. A child group
// like "personal/foo" inherits the [groups."personal".claude].config_dir
// setting from its parent so per-group account isolation propagates through
// nested groups.
func (c *UserConfig) GetGroupClaudeConfigDir(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok && groupCfg.Claude.ConfigDir != "" {
			return ExpandPath(groupCfg.Claude.ConfigDir)
		}
	}
	return ""
}

// GetGroupClaudeEnvFile returns the group-specific Claude env file, walking
// ancestor groups when the exact path has no override. Mirrors
// GetGroupClaudeConfigDir's inheritance semantics so nested groups don't
// silently drop the parent's env_file.
func (c *UserConfig) GetGroupClaudeEnvFile(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok && groupCfg.Claude.EnvFile != "" {
			return groupCfg.Claude.EnvFile
		}
	}
	return ""
}

// findGroupClaudeSetting walks the group ancestor chain (exact path first,
// then each parent) and returns the first non-empty value the extractor
// yields, plus the group path it matched. Shared walk for the scalar
// [groups.X.claude] keys so the inheritance semantics established by
// GetGroupClaudeConfigDir/GetGroupClaudeEnvFile cannot drift per key.
func (c *UserConfig) findGroupClaudeSetting(groupPath string, get func(GroupClaudeSettings) string) (value, matchedGroup string) {
	if c == nil || groupPath == "" || c.Groups == nil {
		return "", ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok {
			if v := get(groupCfg.Claude); v != "" {
				return v, p
			}
		}
	}
	return "", ""
}

// GetGroupClaudeCommand returns the group-specific Claude command, walking
// ancestor groups when the exact path has no override. No path expansion —
// the value is a command/alias, not a filesystem path.
func (c *UserConfig) GetGroupClaudeCommand(groupPath string) string {
	v, _ := c.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.Command })
	return v
}

// GetGroupClaudeModel returns the group-specific Claude model default,
// walking ancestor groups when the exact path has no override.
func (c *UserConfig) GetGroupClaudeModel(groupPath string) string {
	v, _ := c.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.Model })
	return v
}

// GetGroupClaudeEnv returns the merged inline env map for a group. Unlike
// the scalar keys (nearest ancestor wins wholesale), env maps merge along
// the ancestor chain per key — applied root-first so the nearest group's
// value wins on conflict while parent-only keys persist. A child group
// adding one variable must not silently drop the parent's map.
// Returns a freshly allocated map (callers may overlay onto it), nil when
// no level defines env.
func (c *UserConfig) GetGroupClaudeEnv(groupPath string) map[string]string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return nil
	}
	// Collect leaf-first, then apply in reverse (root-first) so nearer
	// groups overwrite per key.
	var chain []map[string]string
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok && len(groupCfg.Claude.Env) > 0 {
			chain = append(chain, groupCfg.Claude.Env)
		}
	}
	if len(chain) == 0 {
		return nil
	}
	merged := make(map[string]string)
	for idx := len(chain) - 1; idx >= 0; idx-- {
		for k, v := range chain[idx] {
			merged[k] = v
		}
	}
	return merged
}

// GetGroupClaudeSkills returns the union of skill-loadout entries along the
// group ancestor chain, deduplicated, root-first. Union (not nearest-wins)
// because the loadout is an attach-only floor: a child group declaring its
// own skills adds to the parent's floor rather than replacing it.
func (c *UserConfig) GetGroupClaudeSkills(groupPath string) []string {
	return c.unionGroupClaudeList(groupPath, func(s GroupClaudeSettings) []string { return s.Skills })
}

// GetGroupClaudeMCPs returns the union of [mcps.X] catalog names along the
// group ancestor chain, deduplicated, root-first. Same floor semantics as
// GetGroupClaudeSkills.
func (c *UserConfig) GetGroupClaudeMCPs(groupPath string) []string {
	return c.unionGroupClaudeList(groupPath, func(s GroupClaudeSettings) []string { return s.MCPs })
}

func (c *UserConfig) unionGroupClaudeList(groupPath string, get func(GroupClaudeSettings) []string) []string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return nil
	}
	var chain [][]string
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok {
			if list := get(groupCfg.Claude); len(list) > 0 {
				chain = append(chain, list)
			}
		}
	}
	if len(chain) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var union []string
	for idx := len(chain) - 1; idx >= 0; idx-- {
		for _, entry := range chain[idx] {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			union = append(union, entry)
		}
	}
	return union
}

// GetGroupHermesEnvFile returns the group-specific Hermes env file, walking
// ancestor groups when the exact path has no override. Mirrors
// GetGroupClaudeEnvFile's inheritance semantics.
func (c *UserConfig) GetGroupHermesEnvFile(groupPath string) string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok && groupCfg.Hermes.EnvFile != "" {
			return groupCfg.Hermes.EnvFile
		}
	}
	return ""
}

// GetConductorClaudeConfigDir returns the conductor-specific Claude config
// directory, if configured. Keyed by conductor name (Instance.Title minus
// "conductor-" prefix — single source of truth is conductorNameFromInstance
// in claude.go). Path expansion matches GetGroupClaudeConfigDir. Returns ""
// when the conductor has no block or no config_dir — callers fall through
// to the group/profile/global chain.
func (c *UserConfig) GetConductorClaudeConfigDir(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	conductorCfg, ok := c.Conductors[name]
	if !ok || conductorCfg.Claude.ConfigDir == "" {
		return ""
	}
	return ExpandPath(conductorCfg.Claude.ConfigDir)
}

// GetConductorClaudeEnvFile returns the conductor-specific Claude env_file,
// if configured. Mirrors GetGroupClaudeEnvFile — no expansion here;
// resolvePath handles it at the spawn-command build site (env.go).
func (c *UserConfig) GetConductorClaudeEnvFile(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	conductorCfg, ok := c.Conductors[name]
	if !ok || conductorCfg.Claude.EnvFile == "" {
		return ""
	}
	return conductorCfg.Claude.EnvFile
}

// GetConductorClaudeCommand returns the conductor-specific Claude command,
// if configured. Mirrors GetGroupClaudeCommand; conductor beats group in
// the resolution chain (CFG-08 precedence).
func (c *UserConfig) GetConductorClaudeCommand(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	return c.Conductors[name].Claude.Command
}

// GetConductorClaudeModel returns the conductor-specific Claude model
// default, if configured. Mirrors GetGroupClaudeModel.
func (c *UserConfig) GetConductorClaudeModel(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	return c.Conductors[name].Claude.Model
}

// GetConductorClaudeEnv returns the conductor-specific inline env map, if
// configured. Applied over the group env map at spawn (conductor wins per
// key). Nil when the conductor has no block or no env.
func (c *UserConfig) GetConductorClaudeEnv(name string) map[string]string {
	if c == nil || name == "" || c.Conductors == nil {
		return nil
	}
	src := c.Conductors[name].Claude.Env
	if len(src) == 0 {
		return nil
	}
	// Defensive copy: never hand callers the live cached map. A caller
	// mutating it would silently corrupt the cached config and race
	// concurrent readers. Mirrors the fresh map GetGroupClaudeEnv returns.
	cp := make(map[string]string, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp
}

// GetConductorClaudeSkills returns the conductor-specific skill-loadout
// entries, if configured. The effective loadout for a conductor session is
// the union of its group chain's skills and this list (floor semantics).
func (c *UserConfig) GetConductorClaudeSkills(name string) []string {
	if c == nil || name == "" || c.Conductors == nil {
		return nil
	}
	src := c.Conductors[name].Claude.Skills
	if len(src) == 0 {
		return nil
	}
	// Defensive copy — see GetConductorClaudeEnv. Callers must not mutate the
	// cached slice; GetGroupClaudeSkills likewise returns a fresh union slice.
	return append([]string(nil), src...)
}

// GetConductorClaudeMCPs returns the conductor-specific [mcps.X] catalog
// names, if configured. Same floor semantics as GetConductorClaudeSkills.
func (c *UserConfig) GetConductorClaudeMCPs(name string) []string {
	if c == nil || name == "" || c.Conductors == nil {
		return nil
	}
	src := c.Conductors[name].Claude.MCPs
	if len(src) == 0 {
		return nil
	}
	// Defensive copy — see GetConductorClaudeEnv.
	return append([]string(nil), src...)
}

// GetConductorHermesEnvFile returns the conductor-specific Hermes env_file,
// if configured. Mirrors GetConductorClaudeEnvFile.
func (c *UserConfig) GetConductorHermesEnvFile(name string) string {
	if c == nil || name == "" || c.Conductors == nil {
		return ""
	}
	conductorCfg, ok := c.Conductors[name]
	if !ok || conductorCfg.Hermes.EnvFile == "" {
		return ""
	}
	return conductorCfg.Hermes.EnvFile
}

// GetDangerousMode returns whether dangerous mode is enabled, defaulting to true
// Power users (the primary audience) typically want this enabled for faster iteration
func (c *ClaudeSettings) GetDangerousMode() bool {
	if c.DangerousMode == nil {
		return true
	}
	return *c.DangerousMode
}

// GetHooksEnabled returns whether Claude Code hooks are enabled, defaulting to true
func (c *ClaudeSettings) GetHooksEnabled() bool {
	if c.HooksEnabled == nil {
		return true
	}
	return *c.HooksEnabled
}

// GetAutoResumeSummary returns whether the "Resume from summary" picker is
// auto-confirmed on session restart, defaulting to true. Conductors and any
// other unattended session runner depend on this — without it, a single
// claude --resume on a >250k-token session leaves the session frozen on the
// picker screen forever.
func (c *ClaudeSettings) GetAutoResumeSummary() bool {
	if c.AutoResumeSummary == nil {
		return true
	}
	return *c.AutoResumeSummary
}

// GeminiSettings defines Gemini CLI configuration
type GeminiSettings struct {
	// YoloMode enables --yolo flag for Gemini sessions (auto-approve all actions)
	// Default: false
	YoloMode bool `toml:"yolo_mode,omitempty"`

	// DefaultModel is the model to use for new Gemini sessions (e.g., "gemini-2.5-flash")
	// If empty, Gemini CLI uses its own default
	DefaultModel string `toml:"default_model,omitempty"`

	// EnvFile is a .env file specific to Gemini sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file,omitempty"`

	// Command overrides the default binary/invocation for Gemini sessions.
	// Supports flags (e.g., "gemini --custom-flag"). Default: "gemini"
	Command string `toml:"command,omitempty"`
}

// OpenCodeSettings defines OpenCode CLI configuration
type OpenCodeSettings struct {
	// DefaultModel is the model to use for new OpenCode sessions
	// Format: "provider/model" (e.g., "anthropic/claude-sonnet-4-5-20250929")
	// If empty, OpenCode uses its own default
	DefaultModel string `toml:"default_model,omitempty"`

	// DefaultAgent is the agent to use for new OpenCode sessions
	// If empty, OpenCode uses its own default
	DefaultAgent string `toml:"default_agent,omitempty"`

	// EnvFile is a .env file specific to OpenCode sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file,omitempty"`

	// Command overrides the default binary/invocation for OpenCode sessions.
	// Supports flags (e.g., "opencode --custom-flag"). Default: "opencode"
	Command string `toml:"command,omitempty"`
}

// CodexSettings defines Codex CLI configuration
type CodexSettings struct {
	// Command is the Codex CLI command or alias to use (e.g., "codex", "codex-v2")
	// Default: "codex"
	Command string `toml:"command,omitempty"`

	// ConfigDir is the path to Codex home directory.
	// Default: ~/.codex (or CODEX_HOME env var)
	ConfigDir string `toml:"config_dir,omitempty"`

	// YoloMode enables --yolo flag for Codex sessions (bypass approvals and sandbox)
	// Default: false
	YoloMode bool `toml:"yolo_mode,omitempty"`

	// EnvFile is a .env file specific to Codex sessions
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file,omitempty"`
}

// GetProfileCodexConfigDir returns the profile-specific Codex config directory, if configured.
func (c *UserConfig) GetProfileCodexConfigDir(profile string) string {
	if c == nil || profile == "" || c.Profiles == nil {
		return ""
	}
	profileCfg, ok := c.Profiles[profile]
	if !ok || profileCfg.Codex.ConfigDir == "" {
		return ""
	}
	return ExpandPath(profileCfg.Codex.ConfigDir)
}

// CopilotSettings defines GitHub Copilot CLI configuration (Issue #556).
// Binary: `copilot` from @github/copilot (GA 2026-02-25).
// Doc: https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
type CopilotSettings struct {
	// EnvFile is a .env file specific to Copilot sessions (sourced before
	// the `copilot` command runs, like [gemini].env_file). Optional.
	EnvFile string `toml:"env_file,omitempty"`

	// Command overrides the default binary/invocation for Copilot sessions.
	// Supports flags (e.g., "copilot --custom-flag"). Default: "copilot"
	Command string `toml:"command,omitempty"`

	// DefaultModel sets the Copilot model for new sessions (e.g., "claude-opus-4.6",
	// "gpt-5.2"). Passed as --model <value>. Can be overridden per-session.
	DefaultModel string `toml:"default_model,omitempty"`

	// AllowAll enables --allow-all by default for new sessions (equivalent to
	// --allow-all-tools --allow-all-paths --allow-all-urls). Can be overridden
	// per-session.
	AllowAll bool `toml:"allow_all,omitempty"`
}

// HermesSettings defines Hermes Agent CLI configuration.
// Binary: `hermes` from github.com/NousResearch/hermes-agent (MIT, v0.13.0+).
// Status detection: process-alive/dead only (content-sniffing deferred).
type HermesSettings struct {
	// Command is the Hermes CLI command or invocation to use.
	// Supports flags (e.g., "hermes --model gpt-5.5-pro --provider openai").
	// Default: "hermes"
	Command string `toml:"command,omitempty"`
	// EnvFile is a .env file specific to Hermes sessions (sourced before
	// the `hermes` command runs). Optional.
	EnvFile string `toml:"env_file,omitempty"`
	// YoloMode enables --yolo flag for Hermes sessions (auto-approve all tool calls).
	// Default: false
	YoloMode bool `toml:"yolo_mode,omitempty"`
	// GatewayURL is the WebSocket URL of the Hermes gateway for health checks.
	// Default: "" (no gateway health check)
	GatewayURL string `toml:"gateway_url,omitempty"`
	// DashboardURL is the Hermes dashboard API endpoint.
	// Default: "" (dashboard integration disabled)
	DashboardURL string `toml:"dashboard_url,omitempty"`
	// APITokenEnv is the environment variable name containing the Hermes API token.
	// Default: "" (uses HERMES_API_TOKEN if set)
	APITokenEnv string `toml:"api_token_env,omitempty"`
	// WorkspaceDir is the base directory for Hermes shared workspace sessions.
	// Default: "" (uses os.TempDir()/hermes-workspaces)
	WorkspaceDir string `toml:"workspace_dir,omitempty"`
}

// CrushSettings defines charmbracelet/crush CLI configuration (Issue #940).
// Binary: `crush` from github.com/charmbracelet/crush. Interactive TUI.
// Key flags: --yolo, --session/-s <id>, --continue/-C, --cwd, --debug.
type CrushSettings struct {
	// Command overrides the default binary/invocation for Crush sessions.
	// Supports flags (e.g., "crush --debug"). Default: "crush"
	Command string `toml:"command,omitempty"`

	// EnvFile is a .env file specific to Crush sessions (sourced before
	// the `crush` command runs, like [gemini].env_file). Optional.
	EnvFile string `toml:"env_file,omitempty"`

	// YoloMode enables --yolo flag for Crush sessions (auto-accept all
	// permission prompts). Default: false
	YoloMode bool `toml:"yolo_mode,omitempty"`
}

// WorktreeSettings contains git worktree preferences.
type WorktreeSettings struct {
	// AutoCleanup: remove worktree when session is deleted (default: true, nil = true)
	AutoCleanup *bool `toml:"auto_cleanup,omitempty"`

	// DefaultEnabled controls whether worktree creation is pre-selected in
	// new-session and fork dialogs by default.
	// Default: false
	DefaultEnabled bool `toml:"default_enabled,omitempty"`

	// DefaultLocation: "sibling" (next to repo), "subdirectory" (inside .worktrees/),
	// or a custom path (e.g., "~/worktrees") creating <path>/<repo_name>/<branch>
	DefaultLocation string `toml:"default_location,omitempty"`

	// PathTemplate: custom path template for worktree location.
	// Variables:
	//   {repo-name}, {repo-root}, {session-id}
	//   {branch}         -> sanitized (human-friendly, may collide)
	//   {branch-escaped} -> URL-escaped (collision-resistant, reversible)
	// Unknown variables like {foo} are left as-is in the path.
	// If set, overrides DefaultLocation.
	PathTemplate *string `toml:"path_template,omitempty"`

	// BranchPrefix is the prefix for auto-generated branch names when creating
	// worktree sessions. For example, "feature/" produces "feature/my-session".
	// Set to "" to disable auto-prefixing (just the session name).
	// Default: "feature/" when not set.
	BranchPrefix *string `toml:"branch_prefix,omitempty"`

	// SetupTimeoutSeconds caps how long .agent-deck/worktree-setup.sh may run.
	// Pointer (not plain int) so the loader can distinguish three cases:
	//   nil         → field unset → 60s default (backward compat, GH #724)
	//   *0          → explicit unlimited (no deadline) — #727 follow-up
	//   *N (N > 0)  → N seconds
	//   *N (N < 0)  → treated as unset (60s default)
	// The `*0 = unlimited` convention matches standard CLI tooling (curl,
	// systemd, docker). Reporter @Clindbergh flagged the v1.7.65 behaviour
	// (`0 = default`) as counter-convention in the PR review for #727.
	SetupTimeoutSeconds *int `toml:"setup_timeout_seconds,omitempty"`
}

// DefaultWorktreeSetupTimeout is the fallback used when no explicit value is
// configured. Kept small and visible so the git package can share it.
const DefaultWorktreeSetupTimeout = 60 * time.Second

// UnlimitedWorktreeSetupTimeout is the sentinel returned by SetupTimeout()
// when the user has configured `setup_timeout_seconds = 0`. The git layer
// interprets this as "no deadline" (context.Background() instead of
// context.WithTimeout). Value chosen as 0 so the config value flows straight
// through to the git layer unchanged.
const UnlimitedWorktreeSetupTimeout time.Duration = 0

// SetupTimeout returns the configured worktree-setup-script timeout.
// Semantics (post-#727 follow-up):
//   - field unset (nil) or negative → DefaultWorktreeSetupTimeout (60s)
//   - explicit 0                    → UnlimitedWorktreeSetupTimeout (no deadline)
//   - positive N                    → N seconds
func (w WorktreeSettings) SetupTimeout() time.Duration {
	if w.SetupTimeoutSeconds == nil {
		return DefaultWorktreeSetupTimeout
	}
	v := *w.SetupTimeoutSeconds
	if v < 0 {
		return DefaultWorktreeSetupTimeout
	}
	if v == 0 {
		return UnlimitedWorktreeSetupTimeout
	}
	return time.Duration(v) * time.Second
}

// Template returns the path template if set, or empty string if nil.
func (w *WorktreeSettings) Template() string {
	if w.PathTemplate == nil {
		return ""
	}
	return *w.PathTemplate
}

// Prefix returns the branch prefix if set, or "feature/" if nil.
// Environment variables (e.g., $USER) in the prefix are expanded.
func (w *WorktreeSettings) Prefix() string {
	if w.BranchPrefix == nil {
		return "feature/"
	}
	return os.ExpandEnv(*w.BranchPrefix)
}

// ApplyBranchPrefix prepends the configured prefix to a branch name.
// If the branch name already starts with the expanded prefix, it is returned unchanged.
func (w *WorktreeSettings) ApplyBranchPrefix(branch string) string {
	prefix := w.Prefix()
	if prefix == "" || strings.HasPrefix(branch, prefix) {
		return branch
	}
	return prefix + branch
}

// GlobalSearchSettings defines global conversation search configuration
type GlobalSearchSettings struct {
	// Enabled enables/disables global search feature (default: true)
	Enabled *bool `toml:"enabled,omitempty"`

	// Tier controls search strategy: "auto", "instant", "balanced", "disabled"
	// auto: Auto-detect based on data size (recommended)
	// instant: Force full in-memory (fast, uses more RAM)
	// balanced: Force LRU cache mode (slower, capped RAM)
	// disabled: Disable global search entirely
	Tier string `toml:"tier,omitempty"`

	// MemoryLimitMB caps memory usage for search index (default: 100)
	// Only applies to balanced tier
	MemoryLimitMB int `toml:"memory_limit_mb,omitzero"`

	// RecentDays limits search to sessions from last N days (0 = all)
	// Reduces index size for users with long history (default: 90)
	RecentDays int `toml:"recent_days,omitzero"`

	// IndexRateLimit limits files indexed per second during background indexing
	// Lower = less CPU impact (default: 20)
	IndexRateLimit int `toml:"index_rate_limit,omitzero"`
}

func (g GlobalSearchSettings) GetEnabled() bool {
	if g.Enabled == nil {
		return true
	}
	return *g.Enabled
}

// ToolDef defines a custom AI tool
type ToolDef struct {
	// Command is the shell command to run
	Command string `toml:"command,omitempty"`

	// CompatibleWith opts this tool into compatibility behavior for a built-in
	// tool even when the configured command is a wrapper script rather than the
	// literal executable name. Supported values currently include "claude" and
	// "codex".
	CompatibleWith string `toml:"compatible_with,omitempty"`

	// Wrapper is an optional command that wraps the tool command.
	// Use {command} placeholder to include the tool command, or omit it to replace the command.
	// Example: wrapper = "nvim +'terminal {command}' +'startinsert'"
	Wrapper string `toml:"wrapper,omitempty"`

	// Icon is the emoji/symbol to display
	Icon string `toml:"icon,omitempty"`

	// BusyPatterns are strings that indicate the tool is busy
	BusyPatterns []string `toml:"busy_patterns,omitempty"`

	// PromptPatterns are strings that indicate the tool is waiting for input
	PromptPatterns []string `toml:"prompt_patterns,omitempty"`

	// DetectPatterns are regex patterns to auto-detect this tool from terminal content
	DetectPatterns []string `toml:"detect_patterns,omitempty"`

	// ResumeFlag is the CLI flag to resume a session (e.g., "--resume")
	ResumeFlag string `toml:"resume_flag,omitempty"`

	// SessionIDEnv is the tmux environment variable name storing the session ID
	SessionIDEnv string `toml:"session_id_env,omitempty"`

	// DangerousMode enables dangerous mode flag for this tool
	DangerousMode bool `toml:"dangerous_mode,omitempty"`

	// DangerousFlag is the CLI flag for dangerous mode (e.g., "--dangerously-skip-permissions")
	DangerousFlag string `toml:"dangerous_flag,omitempty"`

	// OutputFormatFlag is the CLI flag for JSON output format (e.g., "--output-format json")
	OutputFormatFlag string `toml:"output_format_flag,omitempty"`

	// SessionIDJsonPath is the jq path to extract session ID from JSON output
	SessionIDJsonPath string `toml:"session_id_json_path,omitempty"`

	// EnvFile is a .env file specific to this tool
	// Sourced AFTER global [shell].env_files
	// Path can be absolute, ~ for home, $HOME/${VAR} for env vars, or relative to session working directory
	EnvFile string `toml:"env_file,omitempty"`

	// Env is inline environment variables for this tool
	// These are exported AFTER env_file (highest priority)
	// Example: env = { ANTHROPIC_BASE_URL = "https://...", API_KEY = "token" }
	Env map[string]string `toml:"env,omitempty"`

	// Pattern override fields (extend built-in defaults for claude/gemini/opencode/codex/pi)
	// Patterns prefixed with "re:" are compiled as regex; everything else uses strings.Contains.

	// BusyPatternsExtra appends additional busy patterns to the built-in defaults
	BusyPatternsExtra []string `toml:"busy_patterns_extra,omitempty"`

	// PromptPatternsExtra appends additional prompt patterns to the built-in defaults
	PromptPatternsExtra []string `toml:"prompt_patterns_extra,omitempty"`

	// SpinnerChars replaces the default spinner characters entirely (use with caution)
	SpinnerChars []string `toml:"spinner_chars,omitempty"`

	// SpinnerCharsExtra appends additional spinner characters to the built-in defaults
	SpinnerCharsExtra []string `toml:"spinner_chars_extra,omitempty"`
}

// HTTPServerConfig defines how to auto-start an HTTP MCP server
type HTTPServerConfig struct {
	// Command is the executable to run (e.g., "uvx", "python", "node")
	Command string `toml:"command,omitempty"`

	// Args are command-line arguments for the server
	Args []string `toml:"args,omitempty"`

	// Env is environment variables for the server process
	Env map[string]string `toml:"env,omitempty"`

	// StartupTimeout is milliseconds to wait for server to become ready (default: 5000)
	StartupTimeout int `toml:"startup_timeout,omitzero"`

	// HealthCheck is an optional health endpoint URL to poll (e.g., "http://localhost:30000/health")
	// If not set, the main URL is used for health checking
	HealthCheck string `toml:"health_check,omitempty"`
}

// MCPDef defines an MCP server configuration for the MCP Manager
type MCPDef struct {
	// Command is the executable to run (e.g., "npx", "docker", "node")
	// Required for stdio MCPs, optional for HTTP/SSE MCPs
	Command string `toml:"command,omitempty"`

	// Args are command-line arguments
	Args []string `toml:"args,omitempty"`

	// Env is optional environment variables
	Env map[string]string `toml:"env,omitempty"`

	// Description is optional help text shown in the MCP Manager
	Description string `toml:"description,omitempty"`

	// URL is the endpoint for HTTP/SSE MCPs (e.g., "http://localhost:8000/mcp")
	// If set, this MCP uses HTTP or SSE transport instead of stdio
	URL string `toml:"url,omitempty"`

	// Transport specifies the MCP transport type: "stdio" (default), "http", or "sse"
	// Only needed when URL is set; defaults to "http" if URL is present
	Transport string `toml:"transport,omitempty"`

	// Headers is optional HTTP headers for HTTP/SSE MCPs (e.g., for authentication)
	// Example: { Authorization = "Bearer token123" }
	Headers map[string]string `toml:"headers,omitempty"`

	// Server defines how to auto-start an HTTP MCP server process
	// When set, agent-deck will start the server before connecting via HTTP
	// This is optional - you can also connect to externally managed servers
	Server *HTTPServerConfig `toml:"server,omitempty"`
}

// GetStartupTimeout returns the startup timeout in milliseconds, defaulting to 5000ms
func (c *HTTPServerConfig) GetStartupTimeout() int {
	if c.StartupTimeout <= 0 {
		return 5000 // Default: 5 seconds
	}
	return c.StartupTimeout
}

// IsHTTP returns true if this MCP uses HTTP or SSE transport
func (m *MCPDef) IsHTTP() bool {
	return m.URL != ""
}

// GetTransport returns the transport type, defaulting to "http" if URL is set
func (m *MCPDef) GetTransport() string {
	if m.URL == "" {
		return "stdio"
	}
	if m.Transport == "" {
		return "http"
	}
	return m.Transport
}

// HasAutoStartServer returns true if this HTTP MCP has server auto-start configured
func (m *MCPDef) HasAutoStartServer() bool {
	return m.IsHTTP() && m.Server != nil && m.Server.Command != ""
}

// PluginDef defines a Claude Code plugin entry exposed via `agent-deck add
// --plugin <name>` and `agent-deck session set <id> plugins <csv>`.
//
// Plugin id at runtime is constructed as "<Name>@<Source>" and written to
// the per-session scratch settings.json under enabledPlugins (see
// internal/session/worker_scratch.go). v1 is catalog-only: only short names
// listed in [plugins.<name>] tables in ~/.agent-deck/config.toml are valid
// values for the --plugin flag.
//
// RFC: docs/rfc/PLUGIN_ATTACH.md.
type PluginDef struct {
	// Name is the short plugin name as exposed by the upstream marketplace's
	// plugin.json (e.g. "telegram", "octopus"). Required.
	Name string `toml:"name,omitempty"`

	// Source is the marketplace identifier the plugin lives in. Either a
	// curated marketplace name (e.g. "claude-plugins-official") or a github
	// "owner/repo" pair (e.g. "nyldn/claude-octopus"). Required.
	Source string `toml:"source,omitempty"`

	// EmitsChannel hints that this plugin participates in the inbound
	// `notifications/claude/channel` protocol — when true, attaching the
	// plugin via --plugin auto-populates Instance.Channels with
	// "plugin:<Name>@<Source>" so the harness registers the inbound handler.
	// Catalog hint only; agent-deck does not introspect the plugin source.
	EmitsChannel bool `toml:"emits_channel,omitempty"`

	// AutoInstall enables shell-out to `claude plugin install <Name>@<Source>`
	// at session spawn when the plugin code is not yet present under the
	// source profile's plugins/ directory. Best-effort: install failure is
	// logged but does not block session start.
	AutoInstall bool `toml:"auto_install,omitempty"`

	// Description is optional help text shown in the Edit Session dialog
	// pill list.
	Description string `toml:"description,omitempty"`
}

// ID returns the fully-qualified plugin identifier "<Name>@<Source>" used
// both as the enabledPlugins key in settings.json and as the channel id
// "plugin:<ID>" when EmitsChannel is true.
func (p *PluginDef) ID() string {
	return p.Name + "@" + p.Source
}

// ChannelID returns the channel id produced by the auto-link path when
// EmitsChannel is true. Format: "plugin:<Name>@<Source>".
func (p *PluginDef) ChannelID() string {
	return "plugin:" + p.ID()
}

// TmuxSettings allows users to override tmux options applied to every session.
// Options are applied AFTER agent-deck's defaults, so they take precedence.
//
// Example config.toml:
//
//	[tmux]
//	inject_status_line = false
//	options = { "allow-passthrough" = "all", "history-limit" = "50000" }
type TmuxSettings struct {
	// InjectStatusLine controls whether agent-deck injects a custom status line
	// into new tmux sessions. When false, the tmux status bar is not modified,
	// allowing users to use their own tmux status line configuration. This also
	// disables Agent Deck's global tmux notification bar and key bindings so the
	// runtime stops mutating global tmux options.
	// Default: true (nil = use default true)
	InjectStatusLine *bool `toml:"inject_status_line,omitempty"`

	// Mouse controls whether agent-deck enables tmux mouse mode on new
	// sessions. When false, tmux `mouse on` is never set, so the terminal
	// emulator keeps raw control of mouse events — required by the VS Code
	// Linux integrated terminal to let users click-drag to select text
	// (issue #730). Affects both the inline set-option during session
	// creation and the separate EnableMouseMode() path used on reconnect.
	// Default: true (nil = use default true, preserves pre-#730 behavior)
	Mouse *bool `toml:"mouse,omitempty"`

	// LaunchInUserScope starts new tmux servers via `systemd-run --user --scope`
	// so the tmux server lives under the user's systemd manager instead of the
	// current login session scope. This keeps tmux alive when an SSH session
	// scope is torn down.
	//
	// Default (when nil / field absent): true on Linux hosts where
	// `systemd-run --user --version` succeeds, false otherwise. Explicit
	// `launch_in_user_scope = true` or `launch_in_user_scope = false` in
	// config.toml is always honored. Pointer type is required to distinguish
	// "field absent" from "explicit false".
	LaunchInUserScope *bool `toml:"launch_in_user_scope,omitempty"`

	// LaunchAs selects the spawn form for new tmux servers (v1.7.21+).
	// Valid values (case-insensitive, whitespace-trimmed):
	//   "scope"   — systemd-run --user --scope (PR #467 legacy behavior)
	//   "service" — systemd-run --user --unit <NAME>.service with
	//               Type=forking + Restart=on-failure. Adds auto-restart
	//               if the tmux daemon dies unexpectedly (OOM, SIGKILL,
	//               kernel signal). Opt-in defense-in-depth.
	//   "direct"  — plain `tmux new-session` (no systemd isolation).
	//   "auto"    — service where systemd-user manager is available,
	//               else direct.
	//   ""        — unset (default): defer to LaunchInUserScope.
	//
	// LaunchAs, when non-empty and valid, takes precedence over
	// LaunchInUserScope. Unknown values are ignored (fall through to
	// LaunchInUserScope) so a config typo doesn't silently opt the user
	// onto an unintended spawn path.
	//
	// This is additive — v1.7.20 users get zero behavior change until
	// they explicitly set launch_as.
	LaunchAs *string `toml:"launch_as,omitempty"`

	// WindowStyleOverride sets the tmux window-style (and window-active-style) for
	// all sessions, overriding the theme default. Use "default" to let your terminal
	// emulator's background show through instead of agent-deck's theme color.
	// Empty string (default) means use the theme's built-in value.
	// Takes precedence over the same keys in Options if both are set.
	// Example: window_style_override = "default"
	WindowStyleOverride string `toml:"window_style_override,omitempty"`

	// ClearOnRestart clears the tmux scrollback buffer when a session is
	// restarted (respawn-pane). When false (default), the previous session's
	// output is preserved in scrollback. When true, scrollback is wiped so
	// the new session starts with a clean buffer.
	ClearOnRestart bool `toml:"clear_on_restart,omitempty"`

	// DetachKey overrides the PTY-attach detach key (issue #434). Accepts
	// the same lowercase "ctrl+<letter>" form as `[hotkeys].detach` (e.g.
	// "ctrl+d"). When set to a non-empty string, it becomes an alias for
	// `[hotkeys].detach`. Precedence: explicit `[hotkeys].detach` always
	// wins; `[tmux].detach_key` is used only when `[hotkeys].detach` is
	// absent. Empty string (default) preserves the built-in Ctrl+Q.
	//
	// Why the alias exists: #434 reporters asked for a `[tmux]` section
	// entry because they think of the detach as a tmux-attach concern.
	// Keeping `[hotkeys].detach` authoritative avoids two sources of truth.
	DetachKey string `toml:"detach_key,omitempty"`

	// Options is a map of tmux option names to values.
	// These are passed to `tmux set-option -t <session>` after defaults.
	Options map[string]string `toml:"options,omitempty"`

	// SocketName is the tmux `-L <name>` socket selector for every
	// agent-deck tmux spawn (v1.7.50+, issue #687). Empty string — the
	// default — keeps pre-v1.7.50 behavior byte-for-byte: agent-deck shares
	// the user's default tmux server at $TMUX_TMPDIR/tmux-<uid>/default.
	//
	// Set this to isolate agent-deck onto its own tmux server so:
	//   - `[tmux].inject_status_line`, bind-key, and global set-option
	//     mutations stay on the agent-deck server and never touch the
	//     user's interactive tmux config (the original #276 complaint);
	//   - a `tmux kill-server` in the user's shell can't take agent-deck's
	//     managed sessions down with it;
	//   - `tmux -L <name> ls` from the shell shows exactly agent-deck's
	//     sessions — no mixing with the user's own work sessions.
	//
	// Each Instance captures this value at creation time into
	// Instance.TmuxSocketName; changing socket_name later does NOT migrate
	// existing sessions (they remain reachable on their original socket
	// until explicitly re-created). See docs/SOCKET_ISOLATION.md for the
	// migration procedure.
	//
	// Precedence at Instance creation: CLI flag `--tmux-socket <name>`
	// wins, else this config value, else empty.
	SocketName string `toml:"socket_name,omitempty"`
}

// GetInjectStatusLine returns whether to inject status line, defaulting to true.
func (t TmuxSettings) GetInjectStatusLine() bool {
	if t.InjectStatusLine == nil {
		return true
	}
	return *t.InjectStatusLine
}

// GetSocketName returns the trimmed `[tmux].socket_name` value, or "" when
// unset, whitespace-only, or absent. Centralising the trim here means
// every caller — tmux.SetDefaultSocketName at startup, CLI flag merging,
// Instance creation — sees the same sanitised value.
func (t TmuxSettings) GetSocketName() string {
	return strings.TrimSpace(t.SocketName)
}

// GetMouse returns whether tmux mouse mode should be enabled, defaulting to
// true. Issue #730: users on VS Code's Linux integrated terminal need mouse
// OFF so the terminal can handle click-drag selection natively.
func (t TmuxSettings) GetMouse() bool {
	if t.Mouse == nil {
		return true
	}
	return *t.Mouse
}

// GetLaunchInUserScope returns whether new tmux servers should be launched
// under the user's systemd manager. If LaunchInUserScope is non-nil
// (explicit override in config.toml), its value is returned. Otherwise the
// default is determined by isSystemdUserScopeAvailable(): true on
// Linux+systemd hosts, false elsewhere. PERSIST-01..PERSIST-03.
func (t TmuxSettings) GetLaunchInUserScope() bool {
	if t.LaunchInUserScope != nil {
		return *t.LaunchInUserScope
	}
	return isSystemdUserScopeAvailable()
}

// GetLaunchAs returns the canonicalised launch mode string parsed from
// config.toml's [tmux].launch_as key. Returns "" if the field is unset
// or contains an unknown value (in which case downstream callers fall
// back to LaunchInUserScope). v1.7.21+.
func (t TmuxSettings) GetLaunchAs() string {
	if t.LaunchAs == nil {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(*t.LaunchAs))
	switch v {
	case "scope", "service", "direct", "auto":
		return v
	default:
		return ""
	}
}

// systemdUserScopeAvailable caches the result of probing whether
// `systemd-run --user --version` succeeds on this host. Populated exactly
// once per process via systemdUserScopeOnce. Tests can reset both vars via
// resetSystemdDetectionCacheForTest.
//
// systemdUserScopeProbeCount counts how many times the probe body has run;
// it is incremented inside the sync.Once.Do callback. Tests assert it equals
// 1 after consecutive calls (cache hit) and 2 after a reset+call cycle.
var (
	systemdUserScopeOnce       sync.Once
	systemdUserScopeAvailable  bool
	systemdUserScopeProbeCount int64
)

// isSystemdUserScopeAvailable returns true iff exec.LookPath("systemd-run")
// succeeds AND `systemd-run --user --version` exits zero. The result is
// cached for the lifetime of the process. The probe must mirror
// requireSystemdRun in internal/session/session_persistence_test.go so the
// production-code default and the test gate agree on what "Linux+systemd
// available" means. Side effects: none — no stdout/stderr writes, no panic
// on missing/broken systemd-run, errors are swallowed and treated as false.
func isSystemdUserScopeAvailable() bool {
	systemdUserScopeOnce.Do(func() {
		atomic.AddInt64(&systemdUserScopeProbeCount, 1)
		if _, err := exec.LookPath("systemd-run"); err != nil {
			systemdUserScopeAvailable = false
			return
		}
		if err := exec.Command("systemd-run", "--user", "--version").Run(); err != nil {
			systemdUserScopeAvailable = false
			return
		}
		systemdUserScopeAvailable = true
	})
	return systemdUserScopeAvailable
}

// resetSystemdDetectionCacheForTest discards the cached detection result
// so the next call to isSystemdUserScopeAvailable re-probes the host. Used
// only by tests in package session. Not safe for concurrent use with
// callers of isSystemdUserScopeAvailable.
func resetSystemdDetectionCacheForTest() {
	systemdUserScopeOnce = sync.Once{}
	systemdUserScopeAvailable = false
}

// systemdAvailableForLog is a swappable seam so unit tests can deterministically
// drive both branches of the OBS-01 log decision without manipulating PATH or
// the systemd user manager. Production callers always read
// isSystemdUserScopeAvailable.
var systemdAvailableForLog = isSystemdUserScopeAvailable

// cgroupIsolationLog is the slog handle used by LogCgroupIsolationDecision.
// It mirrors the migrationLog pattern at internal/session/migration.go:13 so
// the OBS-01 line is routed through the same dynamicHandler that lands records
// in lumberjack-rotated ~/.agent-deck/debug.log. Tests swap it via
// captureCgroupIsolationLog to capture the emitted record without going
// through disk.
var cgroupIsolationLog *slog.Logger = logging.ForComponent(logging.CompSession)

// cgroupIsolationOnce ensures LogCgroupIsolationDecision emits exactly once
// per process. Tests can reset it via resetCgroupIsolationLogOnceForTest.
var cgroupIsolationOnce sync.Once

// resetCgroupIsolationLogOnceForTest clears the once-guard so the next
// LogCgroupIsolationDecision call re-emits. Test-only — never call from
// production code.
func resetCgroupIsolationLogOnceForTest() {
	cgroupIsolationOnce = sync.Once{}
}

// LogCgroupIsolationDecision emits exactly one structured log line per
// process describing the cgroup isolation decision the runtime made. The
// emitted message is one of these four exact strings (pinned by
// TestLogCgroupIsolationDecision_*):
//
//   - "tmux cgroup isolation: enabled (systemd-run detected)"
//   - "tmux cgroup isolation: disabled (systemd-run not available)"
//   - "tmux cgroup isolation: enabled (config override)"
//   - "tmux cgroup isolation: disabled (config override)"
//
// Decision logic mirrors GetLaunchInUserScope: an explicit (non-nil)
// LaunchInUserScope wins, otherwise systemdAvailableForLog() decides. The
// sync.Once guarantees one-line-per-process even when called from multiple
// goroutines.
//
// Satisfies OBS-01. Intended to be called once from the application bootstrap
// (cmd/agent-deck/main.go) immediately after logging.Init so the line lands in
// ~/.agent-deck/debug.log via lumberjack.
func LogCgroupIsolationDecision() {
	cgroupIsolationOnce.Do(func() {
		settings := GetTmuxSettings()
		var msg string
		switch {
		case settings.LaunchInUserScope != nil && *settings.LaunchInUserScope:
			msg = "tmux cgroup isolation: enabled (config override)"
		case settings.LaunchInUserScope != nil && !*settings.LaunchInUserScope:
			msg = "tmux cgroup isolation: disabled (config override)"
		case systemdAvailableForLog():
			msg = "tmux cgroup isolation: enabled (systemd-run detected)"
		default:
			msg = "tmux cgroup isolation: disabled (systemd-run not available)"
		}
		cgroupIsolationLog.Info(msg)
	})
}

// DockerSettings defines Docker sandbox configuration.
type DockerSettings struct {
	// DefaultImage is the sandbox image to use when not specified per-session.
	DefaultImage string `toml:"default_image,omitempty"`

	// DefaultEnabled enables sandbox by default for new sessions.
	DefaultEnabled bool `toml:"default_enabled,omitempty"`

	// CPULimit is the default CPU limit for sandboxed containers (e.g. "2.0").
	CPULimit string `toml:"cpu_limit,omitempty"`

	// MemoryLimit is the default memory limit for sandboxed containers (e.g. "4g").
	MemoryLimit string `toml:"memory_limit,omitempty"`

	// VolumeIgnores is a list of directories to exclude from the project mount.
	VolumeIgnores []string `toml:"volume_ignores,omitempty"`

	// Environment lists host environment variable names whose values are forwarded to the
	// container at runtime via docker exec -e. The actual values are read from the host
	// on each command invocation, so changes take effect without recreating the container.
	Environment []string `toml:"environment,omitempty"`

	// ExtraVolumes maps host paths to container paths for additional bind mounts.
	ExtraVolumes map[string]string `toml:"extra_volumes,omitempty"`

	// EnvironmentValues are static key=value pairs baked into the container at creation
	// time via docker create -e. Unlike Environment (which forwards by name at runtime),
	// these are fixed when the container is created.
	EnvironmentValues map[string]string `toml:"environment_values,omitempty"`

	// MountSSH mounts ~/.ssh read-only inside the container.
	MountSSH bool `toml:"mount_ssh,omitempty"`

	// AutoCleanup removes sandbox containers on session kill (default: true).
	AutoCleanup *bool `toml:"auto_cleanup,omitempty"`
}

// GetAutoCleanup returns whether to auto-remove sandbox containers, defaulting to true.
func (d DockerSettings) GetAutoCleanup() bool {
	if d.AutoCleanup == nil {
		return true
	}
	return *d.AutoCleanup
}

// ForkSettings controls quick-fork (f) and fork-dialog (Shift+F) defaults.
// Unset structural toggles default to the comprehensive built-in (ON); these
// defaults are independent of [worktree]/[docker] default_enabled, which govern
// non-fork session creation. *bool is required so "absent" reads as ON.
type ForkSettings struct {
	// InheritFromParent, when true, makes the fork mirror the parent session and
	// ignores the structural keys below. See Resolve.
	InheritFromParent bool `toml:"inherit_from_parent,omitempty"`

	// Worktree creates a new worktree + branch. nil => true.
	Worktree *bool `toml:"worktree,omitempty"`
	// WithState carries the parent's tracked uncommitted changes. nil => true.
	WithState *bool `toml:"with_state,omitempty"`
	// WithIgnored also copies gitignored files (implies WithState). nil => false:
	// the gitignored tree is unbounded (data sets, virtual envs, node_modules)
	// and may carry secrets (.env), so copying it is opt-in. See GetWithIgnored.
	WithIgnored *bool `toml:"with_ignored,omitempty"`
	// Docker selects sandbox behavior: "auto" (match parent) | "on" | "off".
	// nil/unknown => "auto". Mirrors the [tmux].launch_as string-enum convention.
	Docker *string `toml:"docker,omitempty"`
	// BranchPrefix is the auto branch-name prefix. "" => "fork/".
	BranchPrefix string `toml:"branch_prefix,omitempty"`
}

// GetWorktree reports whether forks create a worktree (default ON).
func (f ForkSettings) GetWorktree() bool { return f.Worktree == nil || *f.Worktree }

// GetWithState reports whether forks carry tracked state (default ON).
func (f ForkSettings) GetWithState() bool { return f.WithState == nil || *f.WithState }

// GetWithIgnored reports whether forks copy gitignored files (default OFF).
// Off by default because the gitignored tree is unbounded (data sets, virtual
// envs, node_modules) and can carry secrets (.env); copying it silently blocks
// the fork with no size cap or progress. Opt in per fork via the Shift+F
// dialog, globally via [fork].with_ignored = true, or wholesale via
// inherit_from_parent.
func (f ForkSettings) GetWithIgnored() bool { return f.WithIgnored != nil && *f.WithIgnored }

// GetDocker returns the canonical docker mode: "auto" | "on" | "off".
// Mirrors GetLaunchAs: lowercase/trim, unknown/nil -> "auto".
func (f ForkSettings) GetDocker() string {
	if f.Docker == nil {
		return "auto"
	}
	switch v := strings.ToLower(strings.TrimSpace(*f.Docker)); v {
	case "auto", "on", "off":
		return v
	default:
		return "auto"
	}
}

// GetBranchPrefix returns the auto branch-name prefix (default "fork/").
func (f ForkSettings) GetBranchPrefix() string {
	prefix := strings.TrimSpace(f.BranchPrefix)
	if prefix == "" {
		return "fork/"
	}
	return prefix
}

// ResolvedForkPlan is the effective set of structural fork toggles after
// applying [fork] config + parent context.
type ResolvedForkPlan struct {
	Worktree    bool
	WithState   bool
	WithIgnored bool
	Sandbox     bool
}

// Resolve turns ForkSettings + the parent's Docker state into a concrete plan.
// parentSandboxed is source.IsSandboxed(). When InheritFromParent is set, the
// fork mirrors the parent: worktree+state+gitignored ON (the parent is a real
// working tree) and Sandbox matches the parent, ignoring the structural keys.
func (f ForkSettings) Resolve(parentSandboxed bool) ResolvedForkPlan {
	if f.InheritFromParent {
		return ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: parentSandboxed}
	}
	sandbox := parentSandboxed
	switch f.GetDocker() {
	case "on":
		sandbox = true
	case "off":
		sandbox = false
	}
	withIgnored := f.GetWithIgnored()
	withState := f.GetWithState() || withIgnored
	return ResolvedForkPlan{
		Worktree:    f.GetWorktree(),
		WithState:   withState,
		WithIgnored: withIgnored,
		Sandbox:     sandbox,
	}
}

type StatusSettings struct {
	// Reserved for future status detection settings.
	// Control mode pipes are always enabled (no longer configurable).

	// ShellRunningIndicator promotes "shell" tool sessions from idle to
	// running when the pane's foreground command is a genuine non-interactive
	// process (e.g. "node" from `yarn dev`, "java" from `mvn spring-boot:run`).
	// Opt-in (default false): the interactive-program denylist is necessarily
	// incomplete, so a shell sitting at a psql/REPL/fzf prompt would otherwise
	// read "running" while the user is idle. Users who want dev-server
	// detection accept that tradeoff explicitly:
	//
	//	[status]
	//	shell_running_indicator = true
	ShellRunningIndicator bool `toml:"shell_running_indicator"`
}

// MaintenanceSettings controls the automatic maintenance worker
type MaintenanceSettings struct {
	// Enabled enables the maintenance worker (default: false)
	// Prunes Gemini logs, cleans old backups, archives bloated sessions
	Enabled bool `toml:"enabled,omitempty"`
}

// DisplaySettings controls TUI rendering behavior.
type DisplaySettings struct {
	// FullRepaint forces a full screen clear on every render cycle instead of
	// incremental redraws. Enable this if you see vertical drift or rendering
	// artifacts in terminals that use unicode grapheme-cluster widths (e.g.
	// Ghostty 1.3+ with grapheme-width-method=unicode).
	// Can also be enabled via AGENTDECK_REPAINT=full env var.
	// Default: false
	FullRepaint bool `toml:"full_repaint,omitempty"`

	// DefaultFilter sets the initial status filter when the TUI opens.
	// Valid values: "" (all, default), "active" (hides error/stopped),
	// "running", "waiting", "idle", "error".
	// If set to "active" and no non-error sessions exist, falls back to showing all.
	DefaultFilter string `toml:"default_filter,omitempty"`

	// ActiveFilterLabel sets the label shown on the filter pill when the active
	// filter is engaged. Default: "Open". Examples: "Active", "Live", "Open".
	ActiveFilterLabel string `toml:"active_filter_label,omitempty"`

	// ActiveFilterExcludes is the list of session statuses that the % "Open"
	// filter hides. Default: ["error", "stopped"] — matches the original
	// upstream behavior. Set to ["error"] to keep stopped/closed sessions
	// visible while still hiding errors, or extend with "idle" for an
	// aggressive "show only running/waiting" definition. Unknown statuses
	// are dropped silently; if all entries are unknown the default applies.
	// Valid statuses: "running", "waiting", "idle", "error", "starting",
	// "stopped".
	ActiveFilterExcludes []string `toml:"active_filter_excludes,omitempty"`

	// IncludeCwdPrefix controls whether the terminal/pane title is prefixed
	// with "[<cwd-basename>]" (e.g. "[my-project] feature work"). Default true
	// preserves the historical format; set false to show only the session
	// title. Consumed by the tmux set-titles-string builder.
	IncludeCwdPrefix *bool `toml:"include_cwd_prefix,omitempty"`

	// ShowSessionTimestamps appends a dim "Nm ago" badge to every session row.
	// Default: false — opt-in to avoid crowding existing badges. See
	// renderSessionItem for the timestamp source.
	ShowSessionTimestamps bool `toml:"show_session_timestamps,omitempty"`

	// ShowPaneTitles shows the dim tmux pane-title (task description) suffix on
	// every session row, not just the selected one. Default: false — opt-in to
	// avoid crowding narrow sidebars. See renderSessionItem for the source.
	ShowPaneTitles bool `toml:"show_pane_titles,omitempty"`
}

// GetActiveFilterExcludes returns the resolved set of statuses the % filter
// should hide. Default {error, stopped} matches the original upstream
// hardcoded behavior; opt into ["error"] to keep stopped sessions visible.
// Unknown values are dropped; an empty resolved set falls back to the default.
func (d DisplaySettings) GetActiveFilterExcludes() map[Status]bool {
	defaults := func() map[Status]bool {
		return map[Status]bool{StatusError: true, StatusStopped: true}
	}
	if len(d.ActiveFilterExcludes) == 0 {
		return defaults()
	}
	valid := map[Status]bool{
		StatusRunning: true, StatusWaiting: true, StatusIdle: true,
		StatusError: true, StatusStarting: true, StatusStopped: true,
	}
	out := make(map[Status]bool, len(d.ActiveFilterExcludes))
	for _, s := range d.ActiveFilterExcludes {
		if st := Status(s); valid[st] {
			out[st] = true
		}
	}
	if len(out) == 0 {
		return defaults()
	}
	return out
}

// ValidDefaultFilters lists acceptable values for DefaultFilter.
var ValidDefaultFilters = map[string]bool{
	"":        true,
	"active":  true,
	"running": true,
	"waiting": true,
	"idle":    true,
	"error":   true,
}

// GetDefaultFilter returns the validated default_filter value, falling back to "" on invalid input.
func (d DisplaySettings) GetDefaultFilter() string {
	if ValidDefaultFilters[d.DefaultFilter] {
		return d.DefaultFilter
	}
	return ""
}

// GetFullRepaint returns whether full-repaint mode is active, checking
// the env var AGENTDECK_REPAINT=full as an override.
func (d DisplaySettings) GetFullRepaint() bool {
	if strings.EqualFold(os.Getenv("AGENTDECK_REPAINT"), "full") {
		return true
	}
	return d.FullRepaint
}

// GetIncludeCwdPrefix reports whether the "[<cwd-basename>]" title prefix is
// shown. Defaults to true to preserve the historical title format.
func (d DisplaySettings) GetIncludeCwdPrefix() bool {
	if d.IncludeCwdPrefix == nil {
		return true
	}
	return *d.IncludeCwdPrefix
}

// Default user config (empty maps)
var defaultUserConfig = UserConfig{
	Tools:   make(map[string]ToolDef),
	MCPs:    make(map[string]MCPDef),
	Plugins: make(map[string]PluginDef),
}

// cloneDefaultUserConfig returns a fresh shallow copy of defaultUserConfig with
// independent Tools/MCPs maps, so callers mutating the returned value cannot
// leak into later LoadUserConfig() calls. Introduced with v1.7.38's feedback
// opt-out work after a cross-test mutation leak (cfg.Feedback.Disabled=true)
// corrupted the shared global between parallel test cases.
func cloneDefaultUserConfig() UserConfig {
	c := defaultUserConfig
	c.Tools = make(map[string]ToolDef, len(defaultUserConfig.Tools))
	for k, v := range defaultUserConfig.Tools {
		c.Tools[k] = v
	}
	c.MCPs = make(map[string]MCPDef, len(defaultUserConfig.MCPs))
	for k, v := range defaultUserConfig.MCPs {
		c.MCPs[k] = v
	}
	c.Plugins = make(map[string]PluginDef, len(defaultUserConfig.Plugins))
	for k, v := range defaultUserConfig.Plugins {
		c.Plugins[k] = v
	}
	return c
}

// Cache for user config. Invalidated when config.toml's mtime advances past
// the snapshot taken at cache time, so long-running processes (TUI, web,
// notify-daemon) pick up external edits without requiring a full restart.
// Regression: TestLoadUserConfig_PicksUpExternalEdits.
//
// userConfigCacheErr remembers a parse error alongside the cached default
// config so cache hits keep returning it. Without it only the FIRST load
// after an mtime change saw the error; every later call got (defaults, nil)
// and a broken config.toml silently disabled all overrides with zero
// diagnostics until the file's mtime changed again.
var (
	userConfigCache      *UserConfig
	userConfigCacheMtime time.Time
	userConfigCacheErr   error
	userConfigCacheMu    sync.RWMutex
)

// GetUserConfigPath returns the path to the user config file
func GetUserConfigPath() (string, error) {
	return agentpaths.EffectiveConfigPath(UserConfigFileName)
}

// LoadUserConfig loads the user configuration from TOML file.
// After the first load the result is cached; the cache is invalidated when
// config.toml's mtime advances, so external edits to the file (e.g. the user
// editing ~/.agent-deck/config.toml by hand while the TUI is running) are
// picked up on the next call without a manual ClearUserConfigCache.
func LoadUserConfig() (*UserConfig, error) {
	configPath, pathErr := GetUserConfigPath()

	// Stat the file once up front so both the fast-path and the slow-path
	// agree on the same mtime. A missing file is not an error — we fall
	// through to the default config branch below.
	var currentMtime time.Time
	if pathErr == nil {
		if st, err := os.Stat(configPath); err == nil {
			currentMtime = st.ModTime()
		}
	}

	userConfigCacheMu.RLock()
	if userConfigCache != nil && currentMtime.Equal(userConfigCacheMtime) {
		defer userConfigCacheMu.RUnlock()
		return userConfigCache, userConfigCacheErr
	}
	userConfigCacheMu.RUnlock()

	userConfigCacheMu.Lock()
	defer userConfigCacheMu.Unlock()

	// Re-check under write lock: another goroutine may have refreshed the
	// cache to match currentMtime between our RLock drop and Lock acquire.
	if userConfigCache != nil && currentMtime.Equal(userConfigCacheMtime) {
		return userConfigCache, userConfigCacheErr
	}

	if pathErr != nil {
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = time.Time{}
		SetGroupSortMode(fresh.GetGroupSort())
		userConfigCacheErr = nil
		return userConfigCache, nil
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = time.Time{}
		SetGroupSortMode(fresh.GetGroupSort())
		userConfigCacheErr = nil
		return userConfigCache, nil
	}

	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		// Cache default to prevent hot-looping on a broken file, and cache
		// the error too so every call (not just the first after the mtime
		// change) can surface that the on-disk config is being ignored.
		fresh := cloneDefaultUserConfig()
		userConfigCache = &fresh
		userConfigCacheMtime = currentMtime
		SetGroupSortMode(fresh.GetGroupSort())
		userConfigCacheErr = fmt.Errorf("config.toml parse error: %w", err)
		return userConfigCache, userConfigCacheErr
	}

	if config.Tools == nil {
		config.Tools = make(map[string]ToolDef)
	}
	if config.MCPs == nil {
		config.MCPs = make(map[string]MCPDef)
	}
	if config.Plugins == nil {
		config.Plugins = make(map[string]PluginDef)
	}

	normalizeUIHiddenTools(&config.UI, config.Tools)

	// Keep the in-group sort mode in lockstep with the loaded config. This is
	// the single funnel for TUI, web, and CLI; ReloadUserConfig routes through
	// here too, so an external edit to group_sort takes effect on next load.
	SetGroupSortMode(config.GetGroupSort())

	userConfigCache = &config
	userConfigCacheMtime = currentMtime
	userConfigCacheErr = nil
	return userConfigCache, nil
}

// ReloadUserConfig forces a reload of the user config
func ReloadUserConfig() (*UserConfig, error) {
	userConfigCacheMu.Lock()
	userConfigCache = nil
	userConfigCacheMu.Unlock()
	return LoadUserConfig()
}

// SaveUserConfig writes the config to config.toml using atomic write pattern.
// This clears the cache so next LoadUserConfig() reads fresh values.
//
// Guarded path: it backs up the existing config.toml to config.toml.bak before
// overwriting (S2) and REFUSES a save that would drop a populated [mcps] or
// [groups] section to empty (S3, ErrRefusingConfigSectionDrop). Both are
// data-loss safeguards from the 2026-06-04 incident. Callers that genuinely
// intend to clear all MCPs/groups must use SaveUserConfigWithIntent.
func SaveUserConfig(config *UserConfig) error {
	return SaveUserConfigWithIntent(config, false)
}

// SaveUserConfigWithIntent is SaveUserConfig with an explicit opt-out of the S3
// section-drop guard. Pass allowSectionDrop=true only when the user genuinely
// wants to clear all [mcps] or [groups] entries; the default false path refuses
// such a save (ErrRefusingConfigSectionDrop) because zeroing a populated section
// is almost always a partially-built-config bug, not deliberate intent.
//
// The S2 config.toml.bak backup is taken on BOTH paths: the atomic rename
// prevents torn writes but not semantic clobbering, so the .bak is the recovery
// net regardless of intent.
func SaveUserConfigWithIntent(config *UserConfig, allowSectionDrop bool) error {
	configPath, err := GetUserConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Build config content in memory first
	var buf bytes.Buffer

	// Write header comment
	if _, err := buf.WriteString("# Agent Deck Configuration\n"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}
	if _, err := buf.WriteString("# All options: https://github.com/asheshgoplani/agent-deck/blob/main/skills/agent-deck/references/config-reference.md\n\n"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Encode to TOML
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(config); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	// Strip empty TOML sections left behind by omitempty/omitzero (the encoder
	// emits section headers even when all fields within are skipped).
	stripped := stripEmptyTOMLSections(buf.Bytes())
	buf = *bytes.NewBuffer(stripped)

	// ═══════════════════════════════════════════════════════════════════
	// S3 data-loss safeguard (2026-06-04 incident): refuse a save that would
	// drop a populated [mcps] or [groups] section to empty. We round-trip the
	// content we are ABOUT to write (decode buf) and compare its section counts
	// against what is currently on disk. The guard fires ONLY when disk had
	// entries and the new content has zero — a normal edit that loads the
	// config, removes ONE group, and saves still carries the rest of the map,
	// so its count stays > 0 and is unaffected. allowSectionDrop=true (the
	// explicit-intent path) skips the refusal but still backs up.
	// ═══════════════════════════════════════════════════════════════════
	if !allowSectionDrop {
		if err := guardConfigSectionDrop(configPath, buf.Bytes()); err != nil {
			return err
		}
	}

	// S2 data-loss safeguard: copy the existing config.toml to config.toml.bak
	// BEFORE the atomic rename. Atomic rename prevents torn writes but NOT
	// semantic clobbering (e.g. saving a config missing whole sections); the
	// .bak is the recovery net. Best-effort: a failed backup is logged, never
	// fatal — the caller asked to save, and the insurance copy must not become
	// a new failure mode. No-op when config.toml does not exist yet.
	if err := backupConfigFile(configPath); err != nil {
		slog.Warn("session: pre-save config backup failed (continuing with save)",
			"path", configPath, "err", err)
	}

	// ═══════════════════════════════════════════════════════════════════
	// ATOMIC + DURABLE WRITE: Prevents data corruption on crash/power loss
	// and preserves a dotfiles-managed config.toml symlink. The temp file is
	// fsync'd before the rename and the parent dir is fsync'd after (see
	// internal/atomicfile.WriteFileDurable). A symlinked config.toml is written
	// through to its real target rather than replaced with a regular file.
	// ═══════════════════════════════════════════════════════════════════

	if err := atomicfile.WriteFileDurable(configPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to finalize config save: %w", err)
	}

	// Clear cache so next load picks up changes
	ClearUserConfigCache()

	return nil
}

// guardConfigSectionDrop implements the S3 refusal: it decodes the on-disk
// config and the about-to-be-written content, and returns
// ErrRefusingConfigSectionDrop if either [mcps] or [groups] had entries on disk
// but would become empty. A missing/unparseable on-disk file means there is
// nothing populated to protect, so the guard passes (first write, or a file the
// loader would have replaced with defaults anyway).
func guardConfigSectionDrop(configPath string, newContent []byte) error {
	// Nothing on disk yet → nothing to lose.
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		return nil
	}

	var onDisk UserConfig
	if _, err := toml.DecodeFile(configPath, &onDisk); err != nil {
		// Can't read the old file to know what's populated; don't block the
		// save (the loader treats an unparseable file as defaults anyway).
		return nil
	}

	var next UserConfig
	if _, err := toml.Decode(string(newContent), &next); err != nil {
		// We just encoded this from a *UserConfig, so a decode failure is
		// unexpected — surface it rather than silently writing.
		return fmt.Errorf("session: failed to round-trip new config for section-drop guard: %w", err)
	}

	if countFunctionalMCPs(onDisk.MCPs) > 0 && len(next.MCPs) == 0 {
		return fmt.Errorf("%w: [mcps] had %d entries on disk, new config has none", ErrRefusingConfigSectionDrop, countFunctionalMCPs(onDisk.MCPs))
	}
	if countFunctionalGroups(onDisk.Groups) > 0 && len(next.Groups) == 0 {
		return fmt.Errorf("%w: [groups] had %d entries on disk, new config has none", ErrRefusingConfigSectionDrop, countFunctionalGroups(onDisk.Groups))
	}
	return nil
}

func countFunctionalMCPs(mcps map[string]MCPDef) int {
	count := 0
	for _, m := range mcps {
		if m.Command != "" || m.URL != "" || len(m.Args) > 0 || len(m.Env) > 0 || m.Description != "" || len(m.Headers) > 0 || m.Transport != "" || m.Server != nil {
			count++
		}
	}
	return count
}

func countFunctionalGroups(groups map[string]GroupSettings) int {
	var zero GroupSettings
	count := 0
	for _, g := range groups {
		if g.Create || strings.TrimSpace(g.DefaultPath) != "" || !reflect.DeepEqual(g.Claude, zero.Claude) || !reflect.DeepEqual(g.Hermes, zero.Hermes) {
			count++
		}
	}
	return count
}

// stripEmptyTOMLSections removes TOML section headers that have no key=value
// content. The BurntSushi/toml encoder emits headers for struct fields even when
// all their sub-fields are omitted by omitempty/omitzero, leaving orphan headers
// like "[mcp_pool]\n\n[conductor]\n". This strips those to keep the output minimal.
func stripEmptyTOMLSections(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	out := make([][]byte, 0, len(lines))

	for idx := 0; idx < len(lines); idx++ {
		trimmed := bytes.TrimSpace(lines[idx])
		if len(trimmed) == 0 || trimmed[0] != '[' {
			out = append(out, lines[idx])
			continue
		}

		// This line is a section header. Check if it has any content before
		// the next section header (or EOF).
		hasContent := false
		for peek := idx + 1; peek < len(lines); peek++ {
			nextLine := bytes.TrimSpace(lines[peek])
			if len(nextLine) == 0 {
				continue
			}
			if nextLine[0] == '[' {
				break
			}
			hasContent = true
			break
		}

		if hasContent {
			out = append(out, lines[idx])
		}
	}

	// Collapse runs of 3+ blank lines down to 2 (one visual separator).
	result := bytes.Join(out, []byte("\n"))
	for bytes.Contains(result, []byte("\n\n\n")) {
		result = bytes.ReplaceAll(result, []byte("\n\n\n"), []byte("\n\n"))
	}
	return result
}

// backupConfigFile copies config.toml to config.toml.bak (write-temp + rename
// so the .bak is never torn). No-op when the source does not exist yet (first
// save). Part of the S2 data-loss safeguard.
func backupConfigFile(configPath string) error {
	// safeio.Backup is the shared read → temp-write → rename copy (no torn .bak,
	// 0600). A missing config.toml returns ("", nil) — a benign no-op, same as
	// the previous bespoke implementation.
	_, err := safeio.Backup(configPath)
	return err
}

// ClearUserConfigCache clears the cached user config, allowing tests to reset state.
// This does NOT reload - the next LoadUserConfig() call will read fresh from disk.
// Resets both the cache pointer AND the tracked mtime so the invalidation state
// machine starts clean.
func ClearUserConfigCache() {
	userConfigCacheMu.Lock()
	userConfigCache = nil
	userConfigCacheMtime = time.Time{}
	userConfigCacheErr = nil
	userConfigCacheMu.Unlock()
}

// IsClaudeCompatible returns true if the tool is "claude" or a custom tool
// whose underlying command is "claude". Use this for capability gates
// (session tracking, MCP, skills, hooks, etc.) where custom tools wrapping
// Claude should get full Claude functionality.
func IsClaudeCompatible(toolName string) bool {
	if toolName == "claude" {
		return true
	}
	if def := GetToolDef(toolName); def != nil {
		return strings.EqualFold(strings.TrimSpace(def.CompatibleWith), "claude") || isClaudeCommand(def.Command)
	}
	return false
}

// UsesClaudeDeliveryVerify reports whether the Claude-tuned post-send delivery
// verification (issue #876) should be applied for this tool. That verify keys
// off Claude-specific TUI signals — an "active" status transition, the composer
// glyph, and unsent-paste markers. Only Claude-compatible tools surface those;
// every other tool (codex #1205, codewhale/deepseek #1238, gemini #876,
// opencode, and custom CLIs) would false-negative the verify and be reported as
// a silent drop despite successful delivery. Those tools therefore skip the
// Claude-tuned verify. This is the general superset of #1228's codex-only skip.
func UsesClaudeDeliveryVerify(toolName string) bool {
	return IsClaudeCompatible(toolName)
}

// IsCodexCompatible returns true if the tool is "codex" or a custom tool
// whose underlying command is "codex". Use this for capability gates
// where custom tools wrapping Codex should get full Codex functionality
// without losing their configured tool identity.
func IsCodexCompatible(toolName string) bool {
	if toolName == "codex" {
		return true
	}
	if def := GetToolDef(toolName); def != nil {
		return strings.EqualFold(strings.TrimSpace(def.CompatibleWith), "codex") || isCodexCommand(def.Command)
	}
	return false
}

// isShellBinary returns true if cmd is a known interactive shell process name.
// Used to distinguish "shell at a prompt" from "shell running a foreground command"
// (e.g. "node" from "yarn dev", "java" from "mvn spring-boot:run").
func isShellBinary(cmd string) bool {
	switch strings.ToLower(cmd) {
	case "bash", "zsh", "sh", "fish", "dash", "ksh", "tcsh", "csh", "nu", "nushell", "pwsh", "powershell":
		return true
	}
	return false
}

// isInteractiveForegroundProgram returns true for foreground commands that are
// interactive and effectively waiting for the user rather than doing background
// work: editors, pagers, system monitors, remote shells, and terminal
// multiplexers. A shell session sitting in one of these should NOT show a
// "running" indicator (an idle ssh prompt or an open editor is not busy work).
//
// REPLs and interpreters (node, python, ruby, …) are deliberately NOT listed:
// they share a process name with the long-running servers this feature targets
// (`yarn dev` runs as "node", `python manage.py runserver` runs as "python"),
// so denylisting them would defeat the primary use case. The rare REPL false
// positive is the lesser evil versus failing to flag a running dev server.
func isInteractiveForegroundProgram(cmd string) bool {
	switch strings.ToLower(cmd) {
	case
		// remote shells / terminal multiplexers
		"ssh", "mosh", "mosh-client", "et", "tmux", "screen", "zellij",
		// editors
		"vi", "vim", "nvim", "nano", "emacs", "emacsclient", "helix", "hx", "micro", "kak",
		// pagers / viewers
		"less", "more", "most", "man", "bat",
		// system monitors
		"top", "htop", "btop", "btm", "glances", "atop":
		return true
	}
	return false
}

// GetCodexCommand returns the configured Codex command/alias.
func GetCodexCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && strings.TrimSpace(userConfig.Codex.Command) != "" {
		return strings.TrimSpace(userConfig.Codex.Command)
	}
	return "codex"
}

func isClaudeCommand(command string) bool {
	return isCommand(command, "claude")
}

func isCodexCommand(command string) bool {
	return isCommand(command, "codex")
}

func isCommand(command, wantBase string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}

	cmdToken := ""
	for _, field := range fields {
		if isShellEnvAssignment(field) {
			continue
		}
		cmdToken = strings.Trim(field, `"'`)
		break
	}
	if cmdToken == "" {
		return false
	}

	base := filepath.Base(cmdToken)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".EXE")
	return strings.EqualFold(base, wantBase)
}

func isShellEnvAssignment(token string) bool {
	if token == "" {
		return false
	}
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}

	key := token[:idx]
	for i, r := range key {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

// GetToolDef returns a tool definition from user config
// Returns nil if tool is not defined
func GetToolDef(toolName string) *ToolDef {
	// Delegates to the registry's custom-tool lookup. GetCustom returns nil for
	// built-in names (their shadowing custom entries are rejected at registry
	// init), preserving this function's long-standing "nil for built-ins"
	// contract that callers branch on. See Registry.GetCustom / Registry.Get.
	return currentRegistry().GetCustom(toolName)
}

// GetCustomToolNames returns sorted custom tool names from config.toml,
// excluding names that shadow built-in tools (claude, gemini, opencode, codex, pi, shell, cursor, aider).
// Returns nil if no custom tools are configured.
func GetCustomToolNames() []string {
	return currentRegistry().CustomNames()
}

// GetToolCommand returns the configured command override for a builtin tool,
// falling back to the bare tool name if no override is set.
func GetToolCommand(toolName string) string {
	config, _ := LoadUserConfig()
	if config == nil {
		return toolName
	}
	switch toolName {
	case "claude":
		if config.Claude.Command != "" {
			return config.Claude.Command
		}
	case "gemini":
		if config.Gemini.Command != "" {
			return config.Gemini.Command
		}
	case "opencode":
		if config.OpenCode.Command != "" {
			return config.OpenCode.Command
		}
	case "codex":
		if config.Codex.Command != "" {
			return config.Codex.Command
		}
	case "copilot":
		if config.Copilot.Command != "" {
			return config.Copilot.Command
		}
	case "hermes":
		if config.Hermes.Command != "" {
			return config.Hermes.Command
		}
	}
	return toolName
}

func isBuiltinToolName(toolName string) bool {
	return currentRegistry().IsBuiltin(toolName)
}

// GetToolIcon returns the icon for a tool (custom or built-in)
func GetToolIcon(toolName string) string {
	// Check custom tools first
	if def := GetToolDef(toolName); def != nil && def.Icon != "" {
		return def.Icon
	}

	// Built-in icons
	switch toolName {
	case "claude":
		return "🤖"
	case "gemini":
		return "✨"
	case "opencode":
		return "🌐"
	case "codex":
		return "💻"
	case "copilot":
		return "🐙"
	case "crush":
		return "💘"
	case "cursor":
		return "📝"
	case "hermes":
		return "☤"
	case "pi":
		return "π"
	case "shell":
		return "🐚"
	default:
		return "🐚"
	}
}

// GetToolBusyPatterns returns busy patterns for a tool (custom + built-in)
func GetToolBusyPatterns(toolName string) []string {
	var patterns []string

	// Add custom patterns first
	if def := GetToolDef(toolName); def != nil {
		patterns = append(patterns, def.BusyPatterns...)
	}

	// Built-in patterns are handled by the detector
	return patterns
}

// MergeToolPatterns returns merged RawPatterns for a tool, combining built-in
// defaults with any user overrides/extras from config.toml.
// Works for ALL tools: built-in (claude, gemini, etc.) and custom.
// Returns nil only if there are no defaults AND no config entry.
func MergeToolPatterns(toolName string) *tmux.RawPatterns {
	defaults := tmux.DefaultRawPatterns(toolName)
	toolDef := GetToolDef(toolName)

	// No defaults and no config entry: nothing to do
	if defaults == nil && toolDef == nil {
		return nil
	}

	// Build overrides from ToolDef's replace fields (BusyPatterns, PromptPatterns, SpinnerChars)
	var overrides *tmux.RawPatterns
	if toolDef != nil && (toolDef.BusyPatterns != nil || toolDef.PromptPatterns != nil || toolDef.SpinnerChars != nil) {
		overrides = &tmux.RawPatterns{
			BusyPatterns:   toolDef.BusyPatterns,
			PromptPatterns: toolDef.PromptPatterns,
			SpinnerChars:   toolDef.SpinnerChars,
		}
	}

	// Build extras from ToolDef's *Extra fields
	var extras *tmux.RawPatterns
	if toolDef != nil &&
		(len(toolDef.BusyPatternsExtra) > 0 || len(toolDef.PromptPatternsExtra) > 0 || len(toolDef.SpinnerCharsExtra) > 0) {
		extras = &tmux.RawPatterns{
			BusyPatterns:   toolDef.BusyPatternsExtra,
			PromptPatterns: toolDef.PromptPatternsExtra,
			SpinnerChars:   toolDef.SpinnerCharsExtra,
		}
	}

	return tmux.MergeRawPatterns(defaults, overrides, extras)
}

// GetDefaultTool returns the user's preferred default tool for new sessions
// Returns empty string if not configured (defaults to shell)
func GetDefaultTool() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return ""
	}
	return config.DefaultTool
}

// GetWebMutationsEnabled returns whether `agent-deck web` should accept
// mutating HTTP requests (POST/PATCH/DELETE). Defaults to true when the
// `[web].mutations_enabled` key is omitted from config.toml.
func GetWebMutationsEnabled() bool {
	config, err := LoadUserConfig()
	if err != nil || config == nil || config.Web.MutationsEnabled == nil {
		return true
	}
	return *config.Web.MutationsEnabled
}

// GetHotkeyOverrides returns user-configured hotkey overrides from config.toml.
//
// Merge order (issue #434):
//  1. Start from the `[hotkeys]` table.
//  2. If `[tmux].detach_key` is set AND the caller has not already set
//     `[hotkeys].detach`, layer tmux.detach_key into the hotkeys map as the
//     "detach" action. Explicit `[hotkeys].detach` always wins so there is
//     exactly one authoritative source of truth when both are present.
//
// Returns nil only when nothing is configured in either table.
func GetHotkeyOverrides() map[string]string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return nil
	}

	out := make(map[string]string, len(config.Hotkeys)+1)
	for action, key := range config.Hotkeys {
		out[action] = key
	}

	if tmuxKey := strings.TrimSpace(config.Tmux.DetachKey); tmuxKey != "" {
		if _, alreadySet := out[hotkeyDetachAction]; !alreadySet {
			out[hotkeyDetachAction] = tmuxKey
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// hotkeyDetachAction is the canonical action name used by [hotkeys].detach.
// Duplicated from internal/ui/hotkeys.go::hotkeyDetach to avoid an import
// cycle (session <- ui). If the UI constant ever changes, update here too.
const hotkeyDetachAction = "detach"

// GetTheme returns the current theme, defaulting to "dark"
func GetTheme() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return "dark"
	}
	switch config.Theme {
	case "dark", "light", "system":
		return config.Theme
	default:
		return "dark"
	}
}

// ResolveTheme resolves the configured theme to "dark" or "light".
// If theme is "system", detects the OS dark mode setting.
// Falls back to "dark" on detection failure.
func ResolveTheme() string {
	theme := GetTheme()
	if theme != "system" {
		return theme
	}
	// Check the terminal's own declaration before asking the OS.
	// COLORFGBG is set by iTerm2 and other terminals; format is "fg;bg"
	// where bg < 8 means a dark background. This catches the common case
	// where macOS is in light mode but the terminal profile is dark.
	if colorfgbg := os.Getenv("COLORFGBG"); colorfgbg != "" {
		if idx := strings.LastIndex(colorfgbg, ";"); idx >= 0 {
			var bg int
			if _, err := fmt.Sscanf(colorfgbg[idx+1:], "%d", &bg); err == nil {
				if bg < 8 {
					return "dark"
				}
				return "light"
			}
		}
	}

	isDark, err := dark.IsDarkMode()
	if err != nil {
		return "dark"
	}
	if isDark {
		return "dark"
	}
	return "light"
}

// GetRemoveOrphans returns whether orphan log removal is enabled (default: true).
func (l LogSettings) GetRemoveOrphans() bool {
	if l.RemoveOrphans == nil {
		return true
	}
	return *l.RemoveOrphans
}

func (l LogSettings) GetDebugCompress() bool {
	if l.DebugCompress == nil {
		return true
	}
	return *l.DebugCompress
}

// GetLogSettings returns log management settings with defaults applied
func GetLogSettings() LogSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return LogSettings{
			MaxSizeMB: 10,
			MaxLines:  10000,
		}
	}

	settings := config.Logs

	if settings.MaxSizeMB <= 0 {
		settings.MaxSizeMB = 10
	}
	if settings.MaxLines <= 0 {
		settings.MaxLines = 10000
	}

	return settings
}

// GetAutoCleanup returns whether worktree auto-cleanup is enabled (default: true).
func (w WorktreeSettings) GetAutoCleanup() bool {
	if w.AutoCleanup == nil {
		return true
	}
	return *w.AutoCleanup
}

// GetWorktreeSettings returns worktree settings with defaults applied
func GetWorktreeSettings() WorktreeSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return WorktreeSettings{
			DefaultLocation: "subdirectory",
		}
	}

	settings := config.Worktree

	if settings.DefaultLocation == "" {
		settings.DefaultLocation = "subdirectory"
	}

	return settings
}

// GetUpdateSettings returns update settings with defaults applied
func GetUpdateSettings() UpdateSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return UpdateSettings{
			CheckIntervalHours: 24,
		}
	}

	settings := config.Updates

	if settings.CheckIntervalHours <= 0 {
		settings.CheckIntervalHours = 24
	}

	return settings
}

// GetPreviewSettings returns preview settings with defaults applied
func GetPreviewSettings() PreviewSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return PreviewSettings{
			ShowOutput:    nil, // nil means "default to true"
			ShowAnalytics: nil, // nil means "default to true"
		}
	}

	return config.Preview
}

// GetDatePrefix returns whether date prefixing is enabled (default: true).
func (e ExperimentsSettings) GetDatePrefix() bool {
	if e.DatePrefix == nil {
		return true
	}
	return *e.DatePrefix
}

// GetExperimentsSettings returns experiments settings with defaults applied
func GetExperimentsSettings() ExperimentsSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		homeDir, _ := os.UserHomeDir()
		return ExperimentsSettings{
			Directory:   filepath.Join(homeDir, "src", "tries"),
			DefaultTool: "claude",
		}
	}

	settings := config.Experiments

	if settings.Directory == "" {
		homeDir, _ := os.UserHomeDir()
		settings.Directory = filepath.Join(homeDir, "src", "tries")
	} else {
		settings.Directory = ExpandPath(settings.Directory)
	}

	if settings.DefaultTool == "" {
		settings.DefaultTool = "claude"
	}

	return settings
}

// GetEnabled returns whether notifications are enabled (default: true).
func (n NotificationsConfig) GetEnabled() bool {
	if n.Enabled == nil {
		return true
	}
	return *n.Enabled
}

// GetNotificationsSettings returns notification bar settings with defaults applied
func GetNotificationsSettings() NotificationsConfig {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return NotificationsConfig{
			MaxShown: 6,
		}
	}

	settings := config.Notifications

	if settings.MaxShown <= 0 {
		settings.MaxShown = 6
	}

	return settings
}

// GetSelfHealSettings returns self-heal settings from config. The zero value
// (Enabled=false) is the safe default: self-heal does nothing unless explicitly
// enabled. Mode is normalized to a known value by SelfHealMode().
func GetSelfHealSettings() SelfHealSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return SelfHealSettings{}
	}
	return config.SelfHeal
}

// SelfHealAuditPath returns the durable NDJSON audit path for a profile. It uses
// the configured AuditPath override when set, else a per-profile default under
// the agent-deck data dir (so the ≥1-week observe window's records survive
// restarts and are easy to locate for review). profile may be "" (default).
func SelfHealAuditPath(profile string) (string, error) {
	s := GetSelfHealSettings()
	if s.AuditPath != "" {
		// Keep an explicit override profile-scoped too, so multiple profiles do
		// not interleave their records into one file (they run in separate
		// processes but could point at the same override). Insert the profile
		// before the extension: /x/audit.ndjson -> /x/audit-<profile>.ndjson.
		if profile == "" {
			return s.AuditPath, nil
		}
		ext := filepath.Ext(s.AuditPath)
		base := strings.TrimSuffix(s.AuditPath, ext)
		return base + "-" + profile + ext, nil
	}
	name := "selfheal-audit.ndjson"
	if profile != "" {
		name = "selfheal-audit-" + profile + ".ndjson"
	}
	// Lands under <data-dir>/runtime/selfheal/ so the ≥1-week observe-window
	// records survive restarts and are easy to locate for review.
	return runtimeDataPath(filepath.Join("selfheal", name))
}

// GetMaintenanceSettings returns maintenance settings from config
func GetMaintenanceSettings() MaintenanceSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return MaintenanceSettings{Enabled: false}
	}
	return config.Maintenance
}

// GetStatusSettings returns status detection settings with defaults applied.
func GetStatusSettings() StatusSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return StatusSettings{}
	}
	return config.Status
}

// GetDockerSettings returns docker sandbox settings with defaults applied.
func GetDockerSettings() DockerSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return DockerSettings{}
	}
	return config.Docker
}

// GetTmuxSettings returns tmux option overrides from config
func GetTmuxSettings() TmuxSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return TmuxSettings{}
	}
	return config.Tmux
}

// TerminalSettings controls outer-terminal chrome agent-deck writes directly
// to the host terminal (bypassing tmux). These settings affect what the
// terminal emulator displays — currently only iTerm2's badge.
//
// Example config.toml:
//
//	[terminal]
//	iterm_badge = true
type TerminalSettings struct {
	// ITermBadge controls whether agent-deck sets the iTerm2 badge to the
	// attached session's title for the duration of the attach, and refreshes
	// it when Claude renames the session mid-attach. No-op outside iTerm2.
	//
	// AGENTDECK_ITERM_BADGE env var overrides this in either direction
	// (=1/true/yes/on force on, =0/false/no/off force off; unset defers to
	// this config). Caveat: env reliably reaches the attach/detach path
	// (agent-deck reads its own env directly) but the rename-while-attached
	// path runs in a hook subprocess spawned through agent-deck → tmux →
	// Claude → hook, and Claude may filter custom env vars. For consistent
	// behavior on both paths, prefer this config setting — every process
	// re-reads it from disk, so propagation is independent of the spawn
	// chain.
	//
	// Default: false (opt-in). Most users have their own iTerm2 badge scheme
	// (e.g. host/cwd via shell PROMPT_COMMAND), so silently overwriting it on
	// every attach is too presumptuous a default. Users who want the
	// per-session badge set this to true explicitly.
	ITermBadge *bool `toml:"iterm_badge,omitempty"`
}

// GetITermBadge returns whether the iTerm2 badge integration is enabled,
// defaulting to false (opt-in). Mirrors the GetInjectStatusLine pattern but
// with the inverse default — see ITermBadge field doc for rationale.
func (t TerminalSettings) GetITermBadge() bool {
	if t.ITermBadge == nil {
		return false
	}
	return *t.ITermBadge
}

// GetTerminalSettings returns terminal-chrome settings from config.
func GetTerminalSettings() TerminalSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return TerminalSettings{}
	}
	return config.Terminal
}

// GetInstanceSettings returns instance behavior settings
func GetInstanceSettings() InstanceSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return InstanceSettings{} // Defaults applied via GetAllowMultiple()
	}
	return config.Instances
}

// getMCPPoolConfigSection returns the MCP pool config section based on platform
// On unsupported platforms (WSL1, Windows), it's commented out with explanation
func getMCPPoolConfigSection() string {
	header := `
# ============================================================================
# MCP Socket Pool (Advanced)
# ============================================================================
# The MCP pool shares MCP processes across multiple Claude sessions via Unix
# domain sockets. This reduces memory usage when running many sessions.
#
# PLATFORM SUPPORT:
#   macOS/Linux: Full support
#   WSL2: Full support
#   WSL1: NOT SUPPORTED (Unix sockets unreliable)
#   Windows: NOT SUPPORTED
#
# When pooling is disabled or unsupported, MCPs use stdio mode (default).
# Both modes work identically - pooling is just a memory optimization.

`
	if platform.SupportsUnixSockets() {
		// Platform supports pooling - show enabled example
		return header + `# Uncomment to enable MCP socket pooling:
# [mcp_pool]
# enabled = true
# pool_all = true           # Pool all MCPs defined above
# fallback_to_stdio = true  # Fall back to stdio if socket fails
# exclude_mcps = []         # MCPs to exclude from pooling
`
	}

	// Platform doesn't support pooling - explain why it's disabled
	p := platform.Detect()
	reason := "Unix sockets not supported"
	tip := ""

	switch p {
	case platform.PlatformWSL1:
		reason = "WSL1 detected - Unix sockets unreliable"
		tip = "\n# TIP: Upgrade to WSL2 for socket pooling support:\n#      wsl --set-version <distro> 2\n"
	case platform.PlatformWindows:
		reason = "Windows detected - Unix sockets not available"
	}

	return header + fmt.Sprintf(`# MCP pool is DISABLED on this platform: %s
# MCPs will use stdio mode (works fine, just uses more memory with many sessions).
%s
# [mcp_pool]
# enabled = false  # Cannot be enabled on this platform
`, reason, tip)
}

// CreateExampleConfig creates an example config file if none exists
func CreateExampleConfig() error {
	configPath, err := GetUserConfigPath()
	if err != nil {
		return err
	}

	// Don't overwrite existing config
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	exampleConfig := `# Agent Deck User Configuration
# This file is loaded on startup. Edit to customize tools and MCPs.

# Default AI tool for new sessions
# When creating a new session (pressing 'n'), this tool will be pre-selected
# Valid values: "claude", "gemini", "opencode", "codex", "pi", or any custom tool name
# Leave commented out or empty to default to shell (no pre-selection)
# default_tool = "claude"

# Hotkey overrides (optional)
# Action names are defined by agent-deck. Value is the key string.
# Set value to "" to unbind an action.
# [hotkeys]
# delete = "d"
# close_session = "D"
# restart = "R"
# detach = "ctrl+d"   # PTY-attach detach key, default ctrl+q (issue #434).
                      # Alias [tmux].detach_key exists; [hotkeys].detach wins.
# Session switcher (cycle sessions without first detaching to the list).
# OPT-IN: unbound by default. Enabling it makes the attach loop intercept the
# chord before the attached program sees it, so the key is taken from whatever
# runs inside the session. The old default Ctrl+S is a poor choice — it is
# Claude Code's "stash prompt" key and the terminal XOFF flow-control freeze.
# No control byte is safe to steal from every tool, so pick one your attached
# tools do not use. Must be a "ctrl+<letter>" chord.
# switch_session = "ctrl+s"   # opens the switcher while attached. Tap again to
#                             # cycle forward (Ctrl+A to go back); it auto-
#                             # attaches ~1s after you stop, Enter attaches now,
#                             # Esc cancels. Same key opens it from the list.

# Instance behavior (optional)
# [instances]
# allow_multiple = false   # Default: one agent-deck per profile (single-instance gate).
                           # A second instance is rejected to prevent concurrent
                           # reviver/restart loops from tearing down each other's live
                           # sessions (issue #1246). Set true to opt in to multiple
                           # instances (e.g. PC + phone-over-SSH); the first instance
                           # (primary) owns the notification bar.
# follow_cwd_on_attach = true

# Preview settings (optional)
# [preview]
# show_notes = false
# notes_output_split = 0.33

# Claude Code integration
# [claude]
# Custom config directory (for dual account setups)
# Default: ~/.claude (or CLAUDE_CONFIG_DIR env var takes priority)
# config_dir = "~/.claude-work"
# Optional per-profile override (takes precedence over [claude] when profile matches)
# [profiles.work.claude]
# config_dir = "~/.claude-work"
# Enable --dangerously-skip-permissions by default (default: false)
# dangerous_mode = true
# Extra Claude CLI flags remembered from the New Session dialog
# extra_args = ["--agent", "reviewer"]
# Default model preselected for new sessions (must be a known catalog model)
# default_model = "claude-opus-4-7"
# Enable Chrome / teammate mode by default
# use_chrome = false
# use_teammate_mode = false

# Gemini CLI integration
# [gemini]
# Enable --yolo (auto-approve all actions) by default (default: false)
# yolo_mode = true

# OpenCode CLI integration
# [opencode]
# Default model for new sessions (format: "provider/model")
# default_model = "anthropic/claude-sonnet-4-5-20250929"
# Default agent for new sessions
# default_agent = ""

# Codex CLI integration
# [codex]
# Codex CLI command or alias to use (default: "codex")
# command = "codex"
# Custom config directory/home for Codex sessions
# Default: ~/.codex (or CODEX_HOME env var takes priority)
# config_dir = "~/.codex-work"
# Optional per-profile override (takes precedence over [codex] when profile matches)
# [profiles.work.codex]
# config_dir = "~/.codex-work"
# Enable --yolo (bypass approvals and sandbox) by default (default: false)
# yolo_mode = true

# Log file management
# Agent-deck logs session output to ~/.agent-deck/logs/ for status detection
# These settings control automatic log maintenance to prevent disk bloat
[logs]
# Maximum log file size in MB before truncation (default: 10)
max_size_mb = 10
# Number of lines to keep when truncating (default: 10000)
max_lines = 10000
# Remove log files for sessions that no longer exist (default: true)
remove_orphans = true

# Update settings
# Controls automatic update checking and installation
[updates]
# Automatically install updates without prompting (default: false)
# auto_update = true
# Enable update checks on startup (default: true)
check_enabled = true
# How often to check for updates in hours (default: 24)
check_interval_hours = 24
# Show update notification in CLI commands, not just TUI (default: true)
notify_in_cli = true

# Experiments (for 'agent-deck try' command)
# Quick experiment folder management with auto-dated directories
[experiments]
# Base directory for experiments (default: ~/src/tries)
directory = "~/src/tries"
# Add YYYY-MM-DD- prefix to new experiment folders (default: true)
date_prefix = true
# Default AI tool for experiment sessions (default: "claude")
default_tool = "claude"

# Git worktree settings
# Worktrees allow creating isolated working directories for branches
[worktree]
# Where to create worktrees: "sibling" (next to repo) or "subdirectory" (inside repo)
default_location = "sibling"
# Pre-check "Create in worktree" in new-session and fork dialogs (default: false)
# default_enabled = true
# Automatically remove worktree when session is deleted
auto_cleanup = true
# Custom path template (overrides default_location if set)
# Variables:
#   {repo-name}, {repo-root}, {session-id}
#   {branch}         -> sanitized (human-friendly, may collide)
#   {branch-escaped} -> URL-escaped (collision-resistant, reversible)
# path_template = "../worktrees/{repo-name}/{branch}"

# Default scope for MCP operations: "local", "global", or "user"
# "local" writes to .mcp.json (project-only, default)
# "global" writes to Claude profile config (profile-wide)
# "user" writes to ~/.claude.json (all profiles)
# mcp_default_scope = "local"

# Disable ALL .mcp.json management (default: true)
# Set to false if you manage .mcp.json manually or via another tool and don't
# want agent-deck to touch it. LOCAL-scope MCP changes will be silently skipped.
# manage_mcp_json = false

# Tmux session settings
# Controls how agent-deck configures tmux sessions
# [tmux]
# inject_status_line controls whether agent-deck sets up a custom tmux status bar
# When false, your existing tmux status line configuration is preserved and
# agent-deck stops mutating the global tmux notification bar / number key bindings
# Default: true (agent-deck injects its own status bar with session info)
# inject_status_line = false
# mouse controls whether agent-deck enables tmux mouse mode.
# Set this to false if your terminal (e.g. VS Code's Linux integrated terminal)
# interprets mouse events at the terminal layer and you want click-drag text
# selection to bypass tmux entirely. Issue #730.
# Default: true (tmux mouse mode is enabled — scrolling, pane resize, selection in tmux)
# mouse = false
# launch_in_user_scope starts new tmux servers with systemd-run --user --scope
# so they survive when the current login session is torn down (e.g. SSH logout).
# Default: true on Linux+systemd hosts where 'systemd-run --user --version'
#          succeeds, false on macOS / BSD / Linux without a user manager.
# An explicit setting here is ALWAYS honored.
# launch_in_user_scope = false
# window_style_override sets the tmux window-style for all sessions, overriding
# the theme default. Use "default" to let your terminal's background show through.
# window_style_override = "default"
# clear_on_restart clears the tmux scrollback buffer when a session is restarted.
# When false (default), previous output is preserved. When true, scrollback is wiped.
# clear_on_restart = true
# detach_key overrides the PTY-attach detach key (default Ctrl+Q, issue #434).
# Same format as [hotkeys].detach — lowercase "ctrl+<letter>". Useful when your
# editor (e.g. Neovim) uses Ctrl+Q for another binding. [hotkeys].detach is the
# canonical source; [tmux].detach_key is an alias applied only when hotkeys.detach
# is absent. Both live options, documented so users find the one they look for.
# detach_key = "ctrl+d"
# Override tmux options applied to every session (applied after defaults).
# agent-deck does NOT set history-limit by default, so your tmux.conf value is used.
# Options matching agent-deck's managed keys (status, status-style,
# status-left-length, status-right, status-right-length) will cause agent-deck
# to skip its default for that key, letting your value take full effect.
# options = { "allow-passthrough" = "all", "history-limit" = "50000" }
# Example: keep agent-deck notifications but use a 2-line status bar
# options = { "status" = "2" }

# Outer-terminal chrome (sequences agent-deck writes to the host terminal,
# bypassing tmux). Currently controls the iTerm2 badge; future window-title
# integrations will live in the same section.
# [terminal]
# iterm_badge sets the iTerm2 badge to the attached session's title for the
# duration of the attach (cleared on detach), and refreshes it when Claude
# renames the session mid-attach. Opt-in because most users already drive
# the badge from their shell prompt. No-op outside iTerm2.
# Override at runtime: AGENTDECK_ITERM_BADGE=1 forces on, =0 forces off.
# Caveat: the env var reliably reaches the attach/detach path but is
# unreliable for rename-while-attached (Claude may filter env vars when
# spawning hook subprocesses). Prefer this config setting for both paths.
# Default: false
# iterm_badge = true

# ============================================================================
# MCP Server Definitions
# ============================================================================
# Define available MCP servers here. These can be attached/detached per-project
# using the MCP Manager (press 'M' on a Claude session).
#
# Supports two transport types:
#
# STDIO MCPs (local command-line tools):
#   command     - The executable to run (e.g., "npx", "docker", "node")
#   args        - Command-line arguments (array)
#   env         - Environment variables (optional)
#   description - Help text shown in the MCP Manager (optional)
#
# HTTP/SSE MCPs (remote servers):
#   url         - The endpoint URL (http:// or https://)
#   transport   - "http" or "sse" (defaults to "http" if url is set)
#   description - Help text shown in the MCP Manager (optional)

# ---------- STDIO Examples ----------

# Example: Exa Search MCP
# [mcps.exa]
# command = "npx"
# args = ["-y", "@anthropics/exa-mcp"]
# description = "Web search via Exa AI"

# Example: Filesystem MCP with restricted paths
# [mcps.filesystem]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-filesystem", "/Users/you/projects"]
# description = "Read/write local files"

# Example: GitHub MCP with token
# [mcps.github]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-github"]
# env = { GITHUB_TOKEN = "ghp_your_token_here" }
# description = "GitHub repository operations"

# Example: Sequential Thinking MCP
# [mcps.thinking]
# command = "npx"
# args = ["-y", "@modelcontextprotocol/server-sequential-thinking"]
# description = "Step-by-step reasoning for complex problems"

# ---------- HTTP/SSE Examples ----------

# Example: HTTP MCP server (local or remote)
# [mcps.my-http-server]
# url = "http://localhost:8000/mcp"
# transport = "http"
# description = "My custom HTTP MCP server"

# Example: HTTP MCP with authentication headers
# [mcps.authenticated-api]
# url = "https://api.example.com/mcp"
# transport = "http"
# headers = { Authorization = "Bearer your-token-here", "X-API-Key" = "your-api-key" }
# description = "HTTP MCP with auth headers"

# Example: SSE MCP server
# [mcps.remote-sse]
# url = "https://api.example.com/mcp/sse"
# transport = "sse"
# description = "Remote SSE-based MCP"

# ---------- HTTP MCP with Auto-Start Server ----------
# For MCPs that need a local server process (e.g., piekstra/slack-mcp-server),
# add a [mcps.NAME.server] block to have agent-deck auto-start the server.

# Example: Slack MCP with auto-start server
# [mcps.slack]
# url = "http://localhost:30000/mcp/"
# transport = "http"
# description = "Slack 23+ tools (piekstra)"
# [mcps.slack.headers]
#   Authorization = "Bearer xoxb-your-token"
# [mcps.slack.server]
#   command = "uvx"
#   args = ["--python", "3.12", "slack-mcp-server", "--port", "30000"]
#   startup_timeout = 5000
#   health_check = "http://localhost:30000/health"
#   [mcps.slack.server.env]
#     SLACK_API_TOKEN = "xoxb-your-token"

# ============================================================================
# Custom Tool Definitions
# ============================================================================
# Each tool can have:
#   command      - The shell command to run
#   icon         - Emoji/symbol shown in the UI
#   compatible_with - Built-in compatibility to mirror ("claude" or "codex")
#   busy_patterns - Strings that indicate the tool is processing

# Example: Add a custom AI tool
# [tools.my-ai]
# command = "my-ai-assistant"
# icon = "🧠"
# busy_patterns = ["thinking...", "processing..."]

# Example: Add GitHub Copilot CLI
# [tools.copilot]
# command = "gh copilot"
# icon = "🤖"
# busy_patterns = ["Generating..."]

# Example: Custom tool with inline env vars (appears in command picker)
# [tools.glm]
# command = "claude"
# icon = "🧠"
# dangerous_mode = true
# dangerous_flag = "--dangerously-skip-permissions"
# env = { ANTHROPIC_BASE_URL = "https://api.example.com/v4", API_KEY = "your-key" }

# Example: Custom Codex wrapper that should restart and detect status like Codex
# [tools.my-codex]
# command = "codex-wrapper"
# compatible_with = "codex"
# icon = "C"

# ============================================================================
# Status Detection Pattern Overrides (Advanced)
# ============================================================================
# Built-in tools (claude, gemini, opencode, codex, pi) have default detection
# patterns that work out of the box. You can extend them with *_extra fields
# (appended to defaults) or replace them entirely with the base fields.
# Patterns prefixed with "re:" are compiled as regex.
#
# Extend defaults (recommended):
# [tools.claude]
# busy_patterns_extra = ["my custom busy text", "re:custom.*regex"]
# prompt_patterns_extra = ["Custom>"]
# spinner_chars_extra = ["@"]
#
# Replace all defaults (use with caution):
# [tools.claude]
# busy_patterns = ["only-this-pattern"]
`

	// Add platform-aware MCP pool section
	exampleConfig += getMCPPoolConfigSection()

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(configPath, []byte(exampleConfig), 0o600)
}

// GetAvailableMCPs returns MCPs from config.toml as a map
// This replaces the old catalog-based approach with explicit user configuration
func GetAvailableMCPs() map[string]MCPDef {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return make(map[string]MCPDef)
	}
	return config.MCPs
}

// GetAvailableMCPNames returns sorted list of MCP names from config.toml
func GetAvailableMCPNames() []string {
	mcps := GetAvailableMCPs()
	names := make([]string, 0, len(mcps))
	for name := range mcps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetMCPDefaultScope returns the configured default MCP scope.
// Returns "local", "global", or "user". Defaults to "local" if unset or invalid.
func GetMCPDefaultScope() string {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return "local"
	}
	switch config.MCPDefaultScope {
	case "global", "user":
		return config.MCPDefaultScope
	default:
		return "local"
	}
}

// GetManageMCPJson returns whether agent-deck should write to .mcp.json files.
// Defaults to true when unset.
func GetManageMCPJson() bool {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return true
	}
	if config.ManageMCPJson == nil {
		return true
	}
	return *config.ManageMCPJson
}

// GetMCPDef returns a specific MCP definition by name
// Returns nil if not found
func GetMCPDef(name string) *MCPDef {
	mcps := GetAvailableMCPs()
	if def, ok := mcps[name]; ok {
		return &def
	}
	return nil
}

// telegramOfficialRefusalSource is the marketplace id whose telegram entry
// is rejected at catalog-load and CLI/mutator level in v1
// (RFC docs/rfc/PLUGIN_ATTACH.md §6). Forks (different source) are allowed.
const telegramOfficialRefusalSource = "claude-plugins-official"

// pluginIdentifierRe is the strict charset for PluginDef.Name and
// PluginDef.Source (RFC docs/rfc/PLUGIN_ATTACH.md, security finding S5/S6).
// Closes the path-traversal / argv-injection class:
//   - rejects ".." segments via the no-leading-dot anchor + rune set
//   - rejects leading "-" so values can't be parsed as flags by claude
//   - rejects "/" except as a single owner/repo separator (Source only)
//   - rejects null bytes, whitespace, shell metacharacters
//
// Name: single segment, no slash. Source: single segment OR owner/repo.
var (
	pluginNameRe   = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]*$`)
	pluginSourceRe = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]*(/[a-zA-Z0-9_][a-zA-Z0-9._-]*)?$`)
)

// validatePluginDef returns nil iff the def's Name and Source pass the
// strict charset filter. Catalog accessors call this so unsafe values
// never reach exec, filesystem ops, or settings.json mutations.
func validatePluginDef(name string, def PluginDef) error {
	if !pluginNameRe.MatchString(def.Name) {
		return fmt.Errorf("plugin %q: invalid name %q (allowed: [a-zA-Z0-9._-], no leading dot/dash, no path separators)", name, def.Name)
	}
	if !pluginSourceRe.MatchString(def.Source) {
		return fmt.Errorf("plugin %q: invalid source %q (allowed: <single-segment> or <owner>/<repo>, charset [a-zA-Z0-9._-])", name, def.Source)
	}
	return nil
}

// IsTelegramOfficialRefusal reports whether (name, source) pair is the
// exact "telegram@claude-plugins-official" id refused in v1. The check is
// case-sensitive — the upstream catalog uses these literal strings.
func IsTelegramOfficialRefusal(name, source string) bool {
	return name == "telegram" && source == telegramOfficialRefusalSource
}

// GetAvailablePlugins returns the plugin catalog from config.toml, never nil.
// Filters out:
//   - entries refused by IsTelegramOfficialRefusal (RFC §6)
//   - entries failing validatePluginDef (RFC charset filter — security
//     defense against path traversal, argv injection, lock-path escape)
//
// Invalid entries are logged once per LoadUserConfig cycle and silently
// dropped — callers never see them, so unsafe values cannot reach exec,
// filesystem ops, or settings.json mutations.
func GetAvailablePlugins() map[string]PluginDef {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return make(map[string]PluginDef)
	}
	out := make(map[string]PluginDef, len(config.Plugins))
	for k, v := range config.Plugins {
		if IsTelegramOfficialRefusal(v.Name, v.Source) {
			continue
		}
		if err := validatePluginDef(k, v); err != nil {
			slog.Warn("plugin_catalog_entry_rejected",
				slog.String("key", k),
				slog.String("error", err.Error()),
			)
			continue
		}
		out[k] = v
	}
	return out
}

// GetAvailablePluginNames returns sorted catalog keys of plugins.
// Refused entries are excluded (consistent with GetAvailablePlugins).
func GetAvailablePluginNames() []string {
	plugins := GetAvailablePlugins()
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetPluginDef returns a specific plugin definition by catalog key.
// Returns nil if not found OR if the entry matches the v1 refusal policy.
func GetPluginDef(name string) *PluginDef {
	plugins := GetAvailablePlugins()
	if def, ok := plugins[name]; ok {
		return &def
	}
	return nil
}

// CostsSettings configures cost tracking, budgets, and pricing overrides.
type CostsSettings struct {
	Currency      string `toml:"currency,omitempty"`
	Timezone      string `toml:"timezone,omitempty"`
	RetentionDays int    `toml:"retention_days,omitzero"`
	// CostLineTemplate overrides the home status-bar cost segment.
	// Three-state pointer: nil falls through to the next layer
	// (profile -> global -> hardcoded); explicit empty string disables.
	CostLineTemplate *string `toml:"cost_line_template,omitempty"`
	// CostLineHideWhenZero hides the segment when every recognized variable
	// in the active template renders to $0.00. Three-state pointer; default
	// is true (preserves the legacy "no events, no segment" behavior).
	CostLineHideWhenZero *bool           `toml:"cost_line_hide_when_zero,omitempty"`
	Budgets              BudgetSettings  `toml:"budgets,omitempty"`
	Pricing              PricingSettings `toml:"pricing,omitempty"`
}

// ProfileCosts holds per-profile overrides for cost-related settings.
// Pointer fields use the same fall-through semantics as CostsSettings.
type ProfileCosts struct {
	CostLineTemplate     *string `toml:"cost_line_template,omitempty"`
	CostLineHideWhenZero *bool   `toml:"cost_line_hide_when_zero,omitempty"`
}

// defaultCostLineTemplate is the hardcoded fallback that preserves the
// pre-template status-bar segment exactly: render today's total and hide
// when zero events have been recorded.
const defaultCostLineTemplate = "{cost_today} today"

// ResolveCostLineTemplate returns the active status-bar cost-line template
// and hide-when-zero flag, applying the resolution chain:
//
//	profile.costs > [costs] > hardcoded "{cost_today} today" (template, default true for hide)
//
// Pointer semantics:
//   - nil at any level falls through to the next level
//   - explicit empty string for template disables the segment (returned as "")
//   - explicit bool for hide_when_zero is honored at that level
//
// Safe to call with cfg == nil; returns the hardcoded default + true.
func ResolveCostLineTemplate(cfg *UserConfig, profile string) (template string, hideWhenZero bool) {
	template = defaultCostLineTemplate
	hideWhenZero = true

	if cfg == nil {
		return
	}

	var profileCosts *ProfileCosts
	if cfg.Profiles != nil {
		if p, ok := cfg.Profiles[profile]; ok {
			profileCosts = p.Costs
		}
	}

	// Template: profile (set) > global (set) > hardcoded
	switch {
	case profileCosts != nil && profileCosts.CostLineTemplate != nil:
		template = *profileCosts.CostLineTemplate
	case cfg.Costs.CostLineTemplate != nil:
		template = *cfg.Costs.CostLineTemplate
	}

	// Hide flag: profile (set) > global (set) > true
	switch {
	case profileCosts != nil && profileCosts.CostLineHideWhenZero != nil:
		hideWhenZero = *profileCosts.CostLineHideWhenZero
	case cfg.Costs.CostLineHideWhenZero != nil:
		hideWhenZero = *cfg.Costs.CostLineHideWhenZero
	}

	return
}

type BudgetSettings struct {
	DailyLimit   float64                  `toml:"daily_limit,omitzero"`
	WeeklyLimit  float64                  `toml:"weekly_limit,omitzero"`
	MonthlyLimit float64                  `toml:"monthly_limit,omitzero"`
	Groups       map[string]GroupBudget   `toml:"groups,omitempty"`
	Sessions     map[string]SessionBudget `toml:"sessions,omitempty"`
}

type GroupBudget struct {
	DailyLimit float64 `toml:"daily_limit,omitzero"`
}

type SessionBudget struct {
	TotalLimit float64 `toml:"total_limit,omitzero"`
}

type PricingSettings struct {
	Overrides map[string]PricingOverride `toml:"overrides,omitempty"`
}

type PricingOverride struct {
	InputPerMtok      float64 `toml:"input_per_mtok,omitzero"`
	OutputPerMtok     float64 `toml:"output_per_mtok,omitzero"`
	CacheReadPerMtok  float64 `toml:"cache_read_per_mtok,omitzero"`
	CacheWritePerMtok float64 `toml:"cache_write_per_mtok,omitzero"`
}

func (c CostsSettings) GetRetentionDays() int {
	if c.RetentionDays > 0 {
		return c.RetentionDays
	}
	return 90
}

func (c CostsSettings) GetTimezone() string {
	if c.Timezone != "" {
		return c.Timezone
	}
	return "Local"
}

// SystemStatsSettings configures the system stats display in the status bar.
type SystemStatsSettings struct {
	// Enabled controls whether system stats are collected and displayed (default: true)
	Enabled *bool `toml:"enabled,omitempty"`

	// RefreshSeconds sets the collection interval in seconds (default: 5, min: 2)
	RefreshSeconds int `toml:"refresh_seconds,omitzero"`

	// Format controls display density: "compact" (icons), "full" (labels), "minimal" (values only)
	Format string `toml:"format,omitempty"`

	// Show lists which stats to display: "cpu", "ram", "disk", "load", "gpu", "network"
	Show []string `toml:"show,omitempty"`
}

// GetEnabled returns whether system stats display is enabled (default: true).
func (s SystemStatsSettings) GetEnabled() bool {
	if s.Enabled != nil {
		return *s.Enabled
	}
	return true
}

// GetRefreshSeconds returns the collection interval, clamped to [2, 300].
func (s SystemStatsSettings) GetRefreshSeconds() int {
	if s.RefreshSeconds >= 2 {
		if s.RefreshSeconds > 300 {
			return 300
		}
		return s.RefreshSeconds
	}
	return 5
}

// GetFormat returns the display format (default: "compact").
func (s SystemStatsSettings) GetFormat() string {
	switch s.Format {
	case "full", "minimal":
		return s.Format
	default:
		return "compact"
	}
}

// GetShow returns the list of stats to display. Defaults to cpu, ram, disk, network.
func (s SystemStatsSettings) GetShow() []string {
	if len(s.Show) > 0 {
		return s.Show
	}
	return []string{"cpu", "ram", "disk", "network"}
}

// WatcherSettings configures the event watcher system.
type WatcherSettings struct {
	// MaxEventsPerWatcher is the maximum number of events to retain per watcher (default: 500)
	MaxEventsPerWatcher int `toml:"max_events_per_watcher,omitzero"`

	// MaxSilenceMinutes triggers a health warning when no events received (default: 60)
	MaxSilenceMinutes int `toml:"max_silence_minutes,omitzero"`

	// HealthCheckIntervalSeconds is the interval between health checks in seconds (default: 30)
	HealthCheckIntervalSeconds int `toml:"health_check_interval_seconds,omitzero"`

	// Alerts configures the health alerts bridge (opt-in). See WatcherAlertsSettings.
	Alerts WatcherAlertsSettings `toml:"alerts,omitempty"`
}

// GetMaxEventsPerWatcher returns the max events per watcher (default: 500).
func (w WatcherSettings) GetMaxEventsPerWatcher() int {
	if w.MaxEventsPerWatcher > 0 {
		return w.MaxEventsPerWatcher
	}
	return 500
}

// GetMaxSilenceMinutes returns the silence threshold in minutes (default: 60).
func (w WatcherSettings) GetMaxSilenceMinutes() int {
	if w.MaxSilenceMinutes > 0 {
		return w.MaxSilenceMinutes
	}
	return 60
}

// GetHealthCheckIntervalSeconds returns the health check interval in seconds (default: 30).
func (w WatcherSettings) GetHealthCheckIntervalSeconds() int {
	if w.HealthCheckIntervalSeconds > 0 {
		return w.HealthCheckIntervalSeconds
	}
	return 30
}

// WatcherAlertsSettings configures the health alerts bridge (REQ-WF-3).
// Opt-in via [watcher.alerts] in config.toml.
type WatcherAlertsSettings struct {
	// Enabled turns the bridge on. Default: false (no alerts emitted).
	Enabled bool `toml:"enabled,omitempty"`

	// Channels lists notification channel names the bridge's notifier should fan out to
	// (e.g. "telegram", "slack", "discord"). Semantics are owned by the Notifier
	// implementation; the bridge only passes the list to the notifier.
	Channels []string `toml:"channels,omitempty"`

	// DebounceMinutes is the per-(watcher x trigger) debounce window. Default: 15.
	DebounceMinutes int `toml:"debounce_minutes,omitzero"`
}

// GetDebounceMinutes returns the debounce window in minutes (default: 15).
func (a WatcherAlertsSettings) GetDebounceMinutes() int {
	if a.DebounceMinutes > 0 {
		return a.DebounceMinutes
	}
	return 15
}
