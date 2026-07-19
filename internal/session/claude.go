package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// claudeDirNameRegex matches any character that's not alphanumeric or hyphen
// Claude Code replaces all such characters with hyphens in project directory names
var claudeDirNameRegex = regexp.MustCompile(`[^a-zA-Z0-9-]`)

// uuidSessionFileRegex matches UUID-format JSONL session filenames.
var uuidSessionFileRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

// uuidBareRegex matches a bare UUID (no .jsonl suffix). Used to validate
// candidates extracted from `claude --session-id <token>` in a wrapper
// command string before we trust them as the explicit session id.
var uuidBareRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// extractExplicitClaudeSessionID parses the user-supplied wrapper command
// string and returns the literal UUID argument of `--session-id <uuid>`
// (or `--session-id=<uuid>`) if exactly one is present and well-formed.
//
// Issue #1147: in multi-session-per-cwd setups the user explicitly bakes
// a distinct `--session-id <uuid>` into each session's launch command so
// each tenant owns its own Claude conversation. The disk-discovery
// preludes at instance.go:2576 / :2613 (`ensureClaudeSessionIDFromDisk`
// and its restart variant) walk the shared cwd and pick the newest JSONL
// by mtime, which silently hijacks every sibling's id onto whichever
// transcript was written last. The explicit-flag extraction below makes
// the launch command authoritative so disk discovery never gets a chance
// to override an explicit user choice.
//
// Returns ("", false) when:
//   - command is empty
//   - command contains no `--session-id` token
//   - the token is not followed by a well-formed UUID
//   - the command contains shell metacharacters in the UUID argument
//     (e.g. `--session-id "$VAR"`) — the user is doing dynamic id
//     resolution; we cannot safely declare the id without expansion.
func extractExplicitClaudeSessionID(command string) (string, bool) {
	if command == "" {
		return "", false
	}
	// Tokenize by whitespace and `=`. We do NOT attempt full shell parsing —
	// the launch surface is a string like
	//   `env FOO=bar claude --session-id <uuid> --resume <other>`
	// and the contract is: if the literal token `--session-id <uuid>` (or
	// `--session-id=<uuid>`) appears anywhere, trust it. Quoting, command
	// substitution, and variable expansion are not supported because we
	// cannot evaluate them without spawning a shell.
	fields := strings.Fields(command)
	for idx, f := range fields {
		var candidate string
		switch {
		case f == "--session-id":
			if idx+1 >= len(fields) {
				return "", false
			}
			candidate = fields[idx+1]
		case strings.HasPrefix(f, "--session-id="):
			candidate = strings.TrimPrefix(f, "--session-id=")
		default:
			continue
		}
		// Strip a single layer of matched surrounding quotes so
		// `--session-id "abc...def"` still parses. Anything that survives
		// must be a bare UUID — no $VAR, no $(), no backticks.
		candidate = strings.TrimSpace(candidate)
		if len(candidate) >= 2 {
			if (candidate[0] == '"' && candidate[len(candidate)-1] == '"') ||
				(candidate[0] == '\'' && candidate[len(candidate)-1] == '\'') {
				candidate = candidate[1 : len(candidate)-1]
			}
		}
		if !uuidBareRegex.MatchString(candidate) {
			return "", false
		}
		return candidate, true
	}
	return "", false
}

// ConvertToClaudeDirName converts a filesystem path to Claude's directory naming format.
// Claude Code replaces all non-alphanumeric characters (except hyphens) with hyphens.
// Example: /Users/master/Code cloud/!Project → -Users-master-Code-cloud--Project
func ConvertToClaudeDirName(path string) string {
	return claudeDirNameRegex.ReplaceAllString(path, "-")
}

// ClaudeProject represents a project entry in Claude's config
type ClaudeProject struct {
	LastSessionId string `json:"lastSessionId"`
}

// ClaudeConfig represents the structure of .claude.json
type ClaudeConfig struct {
	Projects map[string]ClaudeProject `json:"projects"`
}

// LocalMCP represents an MCP defined in a local .mcp.json file
type LocalMCP struct {
	Name       string // MCP name
	SourcePath string // Directory containing the .mcp.json file
}

// MCPInfo contains MCP server information for a session
type MCPInfo struct {
	Global    []string   // From CLAUDE_CONFIG_DIR/.claude.json mcpServers
	Project   []string   // From CLAUDE_CONFIG_DIR/.claude.json projects[path].mcpServers
	LocalMCPs []LocalMCP // From .mcp.json files (walks up parent directories)
}

// Local returns MCP names for backward compatibility
// Use LocalMCPs directly if you need source path information
func (m *MCPInfo) Local() []string {
	names := make([]string, len(m.LocalMCPs))
	for i, mcp := range m.LocalMCPs {
		names[i] = mcp.Name
	}
	return names
}

// HasAny returns true if any MCPs are configured
func (m *MCPInfo) HasAny() bool {
	return len(m.Global) > 0 || len(m.Project) > 0 || len(m.LocalMCPs) > 0
}

// Total returns total number of MCPs across all sources
func (m *MCPInfo) Total() int {
	return len(m.Global) + len(m.Project) + len(m.LocalMCPs)
}

// AllNames returns a deduplicated, sorted list of all MCP names across all sources
// Used for capturing loaded MCPs at session start for sync tracking
func (m *MCPInfo) AllNames() []string {
	seen := make(map[string]bool)
	for _, name := range m.Global {
		seen[name] = true
	}
	for _, name := range m.Project {
		seen[name] = true
	}
	for _, mcp := range m.LocalMCPs {
		seen[mcp.Name] = true
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// claudeConfigForMCP is used for parsing MCP-related fields from .claude.json
type claudeConfigForMCP struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	} `json:"projects"`
}

// projectMCPConfig is used for parsing .mcp.json files
type projectMCPConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// MCP info cache (30 second TTL to avoid re-reading files on every render)
var (
	mcpInfoCache   = make(map[string]*MCPInfo)
	mcpInfoCacheMu sync.RWMutex
	mcpCacheExpiry = 30 * time.Second
	mcpCacheTimes  = make(map[string]time.Time)
)

// MCPServer represents an MCP with its enabled state
type MCPServer struct {
	Name    string
	Source  string // "local", "global", "project"
	Enabled bool
}

// ProjectMCPSettings represents .claude/settings.local.json
type ProjectMCPSettings struct {
	EnableAllProjectMcpServers bool     `json:"enableAllProjectMcpServers,omitempty"`
	EnabledMcpjsonServers      []string `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string `json:"disabledMcpjsonServers,omitempty"`
}

// MCPMode indicates how MCP enabling/disabling is configured
type MCPMode int

const (
	MCPModeDefault   MCPMode = iota // No explicit config, all enabled
	MCPModeWhitelist                // enabledMcpjsonServers is set
	MCPModeBlacklist                // disabledMcpjsonServers is set
)

// GetMCPInfo retrieves MCP server information for a project path (cached)
// It reads from three sources:
// 1. Global MCPs: CLAUDE_CONFIG_DIR/.claude.json → mcpServers
// 2. Project MCPs: CLAUDE_CONFIG_DIR/.claude.json → projects[projectPath].mcpServers
// 3. Local MCPs: {projectPath}/.mcp.json → mcpServers
func GetMCPInfo(projectPath string) *MCPInfo {
	// Check cache first
	mcpInfoCacheMu.RLock()
	if cached, ok := mcpInfoCache[projectPath]; ok {
		if time.Since(mcpCacheTimes[projectPath]) < mcpCacheExpiry {
			mcpInfoCacheMu.RUnlock()
			return cached
		}
	}
	mcpInfoCacheMu.RUnlock()

	// Cache miss or expired - fetch fresh data
	info := getMCPInfoUncached(projectPath)

	// Update cache
	mcpInfoCacheMu.Lock()
	mcpInfoCache[projectPath] = info
	mcpCacheTimes[projectPath] = time.Now()
	mcpInfoCacheMu.Unlock()

	return info
}

// getMCPInfoUncached reads MCP info from disk (called by cached wrapper)
func getMCPInfoUncached(projectPath string) *MCPInfo {
	info := &MCPInfo{}
	configDir := GetClaudeConfigDir()

	// Read .claude.json for global and project MCPs
	configFile := filepath.Join(configDir, ".claude.json")
	if data, err := os.ReadFile(configFile); err == nil {
		var config claudeConfigForMCP
		if json.Unmarshal(data, &config) == nil {
			// Global MCPs
			for name := range config.MCPServers {
				info.Global = append(info.Global, name)
			}
			// Project-specific MCPs
			if proj, ok := config.Projects[projectPath]; ok {
				for name := range proj.MCPServers {
					info.Project = append(info.Project, name)
				}
			}
		}
	}

	// Read .mcp.json from project directory for local MCPs
	// Walk up parent directories to find .mcp.json (matches Claude Code behavior)
	currentPath := projectPath
	for {
		mcpFile := filepath.Join(currentPath, ".mcp.json")
		if data, err := os.ReadFile(mcpFile); err == nil {
			var mcp projectMCPConfig
			if json.Unmarshal(data, &mcp) == nil {
				for name := range mcp.MCPServers {
					info.LocalMCPs = append(info.LocalMCPs, LocalMCP{
						Name:       name,
						SourcePath: currentPath,
					})
				}
			}
			break // Stop at first .mcp.json found
		}

		// Move up to parent directory
		parent := filepath.Dir(currentPath)
		if parent == currentPath || parent == "/" || parent == "." {
			break // Reached root or invalid path
		}
		currentPath = parent
	}

	// Sort for consistent display
	sort.Strings(info.Global)
	sort.Strings(info.Project)
	// Sort LocalMCPs by name
	sort.Slice(info.LocalMCPs, func(i, j int) bool {
		return info.LocalMCPs[i].Name < info.LocalMCPs[j].Name
	})

	return info
}

// resolveOpts selects which priority chain resolveClaudeConfigDir walks.
//   - inst != nil  → instance chain: conductor > group > env > profile > global > default
//   - inst == nil  → group chain:    group > env > profile > global > default (#1508)
//
// groupPath is consulted in both chains; for the instance chain it falls
// back to inst.GroupPath when not set explicitly.
type resolveOpts struct {
	inst      *Instance
	groupPath string
}

// resolveClaudeConfigDir is the single source of truth for the Claude
// config-dir priority chain. All public Get*ConfigDir*, Is*Explicit, and
// Source* helpers route through this function so the chains cannot
// silently drift (#881).
//
// Returns (path, source) where source is one of:
//
//	"account"   — Instance.Account (issue #924) resolved via [profiles.<account>.claude].config_dir
//	"env"       — CLAUDE_CONFIG_DIR env var
//	"conductor" — [conductors.<name>.claude].config_dir
//	"group"     — [groups."<groupPath>".claude].config_dir
//	"profile"   — [profiles.<profile>.claude].config_dir
//	"global"    — top-level [claude].config_dir
//	"default"   — ~/.claude
//
// On the instance chain Account is the most-specific level (beats
// conductor/group/env). Conductor and group beat env (the #881 fix); see
// GetClaudeConfigDirForInstance doc for the rationale. On the group chain a
// group config_dir also beats env (#1508) so both chains agree that a
// config.toml-scoped group override wins over a shell-wide CLAUDE_CONFIG_DIR.
func resolveClaudeConfigDir(opts resolveOpts) (path, source string) {
	userConfig, _ := LoadUserConfig()

	groupPath := opts.groupPath
	if groupPath == "" && opts.inst != nil {
		groupPath = opts.inst.GroupPath
	}

	if opts.inst != nil {
		// Instance chain: account is the most-specific override (#924).
		// Falls through to conductor/group/env when the account name has
		// no matching [profiles.<account>.claude].config_dir block — so an
		// unconfigured account name is a silent no-op, matching the
		// permissive style of the other levels.
		if userConfig != nil && opts.inst.Account != "" {
			if accountDir := userConfig.GetProfileClaudeConfigDir(opts.inst.Account); accountDir != "" {
				return accountDir, "account"
			}
		}
		// Instance chain: conductor / group beat env.
		if userConfig != nil {
			if name := conductorNameFromInstance(opts.inst); name != "" {
				if conductorDir := userConfig.GetConductorClaudeConfigDir(name); conductorDir != "" {
					return conductorDir, "conductor"
				}
			}
			if groupDir := userConfig.GetGroupClaudeConfigDir(groupPath); groupDir != "" {
				return groupDir, "group"
			}
		}
		if envDir := envClaudeConfigDirIgnoringScratchLeak(); envDir != "" {
			return envDir, "env"
		}
	} else {
		// Group chain: group beats env (#1508). A
		// [groups."<groupPath>".claude].config_dir is a config.toml-scoped
		// override and is strictly more specific than a shell-wide
		// CLAUDE_CONFIG_DIR (which dev shells commonly export via aliases).
		// This mirrors the instance chain so a grouped child launched from a
		// shell exporting a stale ambient account still resolves to its
		// group's configured account. Env still wins when the group has no
		// config_dir to assert.
		if userConfig != nil {
			if groupDir := userConfig.GetGroupClaudeConfigDir(groupPath); groupDir != "" {
				return groupDir, "group"
			}
		}
		if envDir := envClaudeConfigDirIgnoringScratchLeak(); envDir != "" {
			return envDir, "env"
		}
	}

	if userConfig != nil {
		profile := GetEffectiveProfile("")
		if profileDir := userConfig.GetProfileClaudeConfigDir(profile); profileDir != "" {
			return profileDir, "profile"
		}
		if userConfig.Claude.ConfigDir != "" {
			return ExpandPath(userConfig.Claude.ConfigDir), "global"
		}
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude"), "default"
}

// envClaudeConfigDirIgnoringScratchLeak returns the expanded CLAUDE_CONFIG_DIR
// env var, or "" when it is unset OR points inside the worker-scratch root.
//
// Nested-scratch credential chain (successor to #1222/#1224): when agent-deck
// runs INSIDE a scratch-pinned worker (a session that itself spawns children),
// the parent's scratch CLAUDE_CONFIG_DIR is the ambient env value. Honoring it
// as the "env" priority level makes children inherit ANOTHER WORKER'S scratch
// dir as their source profile, so their credentials symlink lands on the
// parent's scratch entry — possibly a forked real-file copy (post-/login
// clobber) that reassertCredentialSymlink can then never collapse to the real
// canonical. A worker-scratch dir is an ephemeral, agent-deck-owned mirror; it
// is never a legitimate user profile, so it is never a legitimate env
// override. Ignoring it falls through to profile/global/default — the real
// canonical chain. ChildLaunchEnv (#1163) already strips the var from child
// envs; this guards the resolver itself for in-process resolution paths.
func envClaudeConfigDirIgnoringScratchLeak() string {
	envDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if envDir == "" {
		return ""
	}
	expanded := ExpandPath(envDir)
	if pathUnderWorkerScratch(expanded) {
		return ""
	}
	return expanded
}

// GetClaudeConfigDir returns the Claude config directory for the active profile.
func GetClaudeConfigDir() string {
	path, _ := resolveClaudeConfigDir(resolveOpts{})
	return path
}

// IsClaudeConfigDirExplicit returns true when any priority level (env,
// profile, global) sets the dir.
func IsClaudeConfigDirExplicit() bool {
	_, source := resolveClaudeConfigDir(resolveOpts{})
	return source != "default"
}

// GetClaudeConfigDirForGroup returns the Claude config directory, walking
// the group chain: env > group > profile > global > default.
func GetClaudeConfigDirForGroup(groupPath string) string {
	path, _ := resolveClaudeConfigDir(resolveOpts{groupPath: groupPath})
	return path
}

// IsClaudeConfigDirExplicitForGroup returns true when any priority level sets the dir.
func IsClaudeConfigDirExplicitForGroup(groupPath string) bool {
	_, source := resolveClaudeConfigDir(resolveOpts{groupPath: groupPath})
	return source != "default"
}

// GetClaudeConfigDirSourceForGroup returns (path, source) for the group chain.
// Sources: "env", "group", "profile", "global", "default".
func GetClaudeConfigDirSourceForGroup(groupPath string) (path, source string) {
	return resolveClaudeConfigDir(resolveOpts{groupPath: groupPath})
}

// conductorNameFromInstance extracts the conductor name from an Instance's
// Title. Returns "" for non-conductor sessions. Mirrors the canonical pattern
// used in env.go getConductorEnv (line 267) — single source of truth for
// conductor name derivation from a session.
func conductorNameFromInstance(inst *Instance) string {
	if inst == nil {
		return ""
	}
	name := strings.TrimPrefix(inst.Title, "conductor-")
	if name == "" || name == inst.Title {
		return ""
	}
	return name
}

// GetClaudeConfigDirForInstance returns the Claude config directory for
// this Instance. Per-instance TOML overrides (conductor, group) beat the
// shell-wide CLAUDE_CONFIG_DIR env var; less-specific config (profile,
// global) falls to env and below.
//
// Priority (most-specific → least-specific):
//
//  1. Instance.Account (#924) → [profiles.<account>.claude].config_dir
//  2. [conductors.<name>.claude].config_dir — consulted only when
//     Instance.Title starts with "conductor-"
//  3. [groups."<group>".claude].config_dir
//  4. CLAUDE_CONFIG_DIR env var
//  5. [profiles.<profile>.claude].config_dir
//  6. [claude].config_dir
//  7. ~/.claude
//
// Why conductor/group beat env (fix-config-dir-priority, 2026-04-17):
// developer shells commonly export CLAUDE_CONFIG_DIR via aliases (cdp,
// cdw) to select a profile. When the user then writes an explicit
// [conductors.foo.claude] or [groups.bar.claude] block in config.toml,
// that TOML block is scoped to exactly that conductor/group and is
// semantically MORE specific than a shell-wide default. The old
// env-first order silently shadowed every TOML override. Profile/global
// remain beaten by env because they're shell-wide too (less specific
// than env in intent).
func GetClaudeConfigDirForInstance(inst *Instance) string {
	path, _ := resolveClaudeConfigDir(resolveOpts{inst: inst})
	return path
}

// GetClaudeConfigDirSourceForInstance returns (path, source) for the
// instance chain. Source labels: "account" (issue #924), "conductor",
// "group", "env", "profile", "global", "default".
func GetClaudeConfigDirSourceForInstance(inst *Instance) (path, source string) {
	return resolveClaudeConfigDir(resolveOpts{inst: inst})
}

// IsClaudeConfigDirExplicitForInstance returns true if ANY priority level
// sets a config dir for this Instance.
func IsClaudeConfigDirExplicitForInstance(inst *Instance) bool {
	_, source := resolveClaudeConfigDir(resolveOpts{inst: inst})
	return source != "default"
}

// GetClaudeCommand returns the configured Claude command/alias
// Priority: 1) UserConfig setting, 2) Default "claude"
// This allows users to configure an alias like "cdw" or "cdp" that sets
// CLAUDE_CONFIG_DIR automatically, avoiding the need for config_dir setting
func GetClaudeCommand() string {
	return GetToolCommand("claude")
}

// GetClaudeCommandForInstance returns the Claude command for this Instance,
// extending GetClaudeCommand with the per-conductor / per-group levels.
// Priority (most-specific → least-specific, same order as the config_dir
// chain, CFG-08 mirror semantics):
//
//  1. [conductors.<name>.claude].command — conductor sessions only
//  2. [groups."<group>".claude].command — ancestor-walking
//  3. [claude].command (global)
//  4. "claude"
//
// An instance-level custom command (a non-"claude" command string stored on
// the session, e.g. from `add -c <wrapper>`) never reaches this resolver —
// the spawn builders only consult it when the stored command is the default
// "claude", so the explicit per-session command stays the strongest level.
func GetClaudeCommandForInstance(inst *Instance) string {
	if userConfig, _ := LoadUserConfig(); userConfig != nil && inst != nil {
		if name := conductorNameFromInstance(inst); name != "" {
			if cmd := userConfig.GetConductorClaudeCommand(name); cmd != "" {
				return cmd
			}
		}
		if cmd := userConfig.GetGroupClaudeCommand(inst.GroupPath); cmd != "" {
			return cmd
		}
	}
	return GetClaudeCommand()
}

// resolveClaudeLaunchModel returns the --model value for a claude spawn.
// Priority:
//
//  1. explicit per-session model (CLI --model / new-session dialog,
//     persisted on ClaudeOptions — passed in as optsModel)
//  2. [conductors.<name>.claude].model — conductor sessions only
//  3. [groups."<group>".claude].model — ancestor-walking
//  4. "" — no flag; Claude Code's own default
//
// Resolved at command-build time (not baked in at create) so a config edit
// takes effect on the next start/restart without re-creating the session —
// matching how config_dir and env_file resolve. The global
// [claude].default_model deliberately stays a new-session-dialog prefill
// (#1172) and is NOT applied here, so behavior without group/conductor
// blocks is unchanged.
func (i *Instance) resolveClaudeLaunchModel(optsModel string) string {
	if optsModel != "" {
		return optsModel
	}
	userConfig, _ := LoadUserConfig()
	if userConfig == nil {
		return ""
	}
	if name := conductorNameFromInstance(i); name != "" {
		if m := userConfig.GetConductorClaudeModel(name); m != "" {
			return m
		}
	}
	return userConfig.GetGroupClaudeModel(i.GroupPath)
}

// GetClaudeSessionID returns the ACTIVE session ID for a project path
// It first tries to find the currently running session by checking recently
// modified .jsonl files, then falls back to lastSessionId from config
func GetClaudeSessionID(projectPath string) (string, error) {
	configDir := GetClaudeConfigDir()

	// First, try to find active session from recently modified files
	activeID := findActiveSessionID(configDir, projectPath)
	if activeID != "" {
		return activeID, nil
	}

	// Fall back to lastSessionId from config
	configFile := filepath.Join(configDir, ".claude.json")
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", fmt.Errorf("failed to read Claude config: %w", err)
	}

	var config ClaudeConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse Claude config: %w", err)
	}

	// Look up project by path
	if project, ok := config.Projects[projectPath]; ok {
		if project.LastSessionId != "" {
			return project.LastSessionId, nil
		}
	}

	return "", fmt.Errorf("no session found for project: %s", projectPath)
}

// findActiveSessionID looks for the most recently modified session file
// This finds the CURRENTLY RUNNING session, not the last completed one
func findActiveSessionID(configDir, projectPath string) string {
	return findActiveSessionIDExcluding(configDir, projectPath, nil)
}

// findActiveSessionIDExcluding looks for the most recently modified session file,
// skipping any session IDs in the exclude set. This prevents picking up a .jsonl
// owned by another agent-deck instance when multiple sessions share the same project.
func findActiveSessionIDExcluding(configDir, projectPath string, excludeIDs map[string]bool) string {
	// Convert project path to Claude's directory format
	// Claude replaces ALL non-alphanumeric chars (spaces, !, etc.) with hyphens
	// /Users/master/Code cloud/!Project -> -Users-master-Code-cloud--Project
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)

	// Check if project directory exists
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return ""
	}

	// Find session files (UUID format, not agent-* files)
	files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return ""
	}

	// UUID pattern for session files (compiled once at package level)
	uuidPattern := uuidSessionFileRegex

	var mostRecent string
	var mostRecentTime time.Time

	for _, file := range files {
		base := filepath.Base(file)

		// Skip agent files (agent-*.jsonl)
		if strings.HasPrefix(base, "agent-") {
			continue
		}

		// Only consider UUID-named files
		if !uuidPattern.MatchString(base) {
			continue
		}

		sessionID := strings.TrimSuffix(base, ".jsonl")

		// Skip IDs owned by other agent-deck instances
		if excludeIDs[sessionID] {
			continue
		}

		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// Find the most recently modified file
		if info.ModTime().After(mostRecentTime) {
			mostRecentTime = info.ModTime()
			mostRecent = sessionID
		}
	}

	// Only return if modified within last 5 minutes (actively used)
	if mostRecent != "" && time.Since(mostRecentTime) < 5*time.Minute {
		return mostRecent
	}

	return ""
}

// discoverLatestClaudeJSONL resolves the newest UUID-named JSONL transcript
// (by mtime) under Claude Code's canonical projects directory for the given
// projectPath. Returns (uuid, true) on a hit — where uuid is the JSONL
// basename stripped of the ".jsonl" suffix — and ("", false) when the
// project dir is absent, empty, or contains zero UUID-named JSONLs.
//
// This helper is PURE: no side effects, no Instance mutation, no logging.
// Phase 5 (REQ-7 / PERSIST-11..13) call site in Instance.Start() /
// Instance.StartWithMessage() is responsible for write-through persistence
// of the discovered UUID into i.ClaudeSessionID BEFORE spawn, and for
// emitting the D-07 `resume: id=<uuid> reason=jsonl_discovery` log line.
//
// Semantic differences vs findActiveSessionID (claude.go:332):
//   - NO 5-minute recency cap. findActiveSessionID detects a CURRENTLY
//     running session for session-id reconciliation at instance.go:2602-2660;
//     the cap is intentional there. Phase 5 picks a resume target at
//     cold-boot/start time, where any JSONL however old is a legitimate
//     candidate — a cap would silently break every cold resume.
//   - No exclude-ID plumbing. Discovery at start time has no notion of
//     "another agent-deck instance's session id to avoid".
//
// Path encoding mirrors sessionHasConversationData at instance.go:4845-4926
// and findActiveSessionID at claude.go:340-344: EvalSymlinks on projectPath
// first (resolves macOS /tmp → /private/tmp), then ConvertToClaudeDirName,
// then join under GetClaudeConfigDir() + "/projects/" (falling back to
// $HOME/.claude when GetClaudeConfigDir returns "").
//
// Errors are swallowed (returns "", false) — PERSIST-13 guarantees that an
// absent / unreadable / empty project dir is NOT an error; the caller falls
// through to the existing fresh-session branch.
func discoverLatestClaudeJSONL(projectPath string) (string, bool) {
	if projectPath == "" {
		return "", false
	}

	configDir := GetClaudeConfigDir()
	if configDir == "" {
		configDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}

	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}

	encoded := ConvertToClaudeDirName(resolvedPath)
	if encoded == "" {
		encoded = "-"
	}

	projectDir := filepath.Join(configDir, "projects", encoded)
	// #nosec G703 -- projectDir is derived from configDir (CLAUDE_CONFIG_DIR)
	// joined with an encoded session ID; not from untrusted input.
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return "", false
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil || len(entries) == 0 {
		return "", false
	}

	var bestUUID string
	var bestMTime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if strings.HasPrefix(base, "agent-") {
			continue
		}
		if !uuidSessionFileRegex.MatchString(base) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().After(bestMTime) {
			bestMTime = info.ModTime()
			bestUUID = strings.TrimSuffix(base, ".jsonl")
		}
	}

	if bestUUID == "" {
		return "", false
	}
	return bestUUID, true
}

// getProjectSettingsPath returns the path to .claude/settings.local.json for a project
func getProjectSettingsPath(projectPath string) string {
	return filepath.Join(projectPath, ".claude", "settings.local.json")
}

// readProjectMCPSettings reads the project's MCP settings file
func readProjectMCPSettings(projectPath string) (*ProjectMCPSettings, error) {
	settingsPath := getProjectSettingsPath(projectPath)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No settings file = default (all enabled)
			return &ProjectMCPSettings{}, nil
		}
		return nil, fmt.Errorf("failed to read settings: %w", err)
	}

	var settings ProjectMCPSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse settings: %w", err)
	}

	return &settings, nil
}

// GetMCPMode determines the MCP configuration mode for a project
func GetMCPMode(projectPath string) MCPMode {
	settings, err := readProjectMCPSettings(projectPath)
	if err != nil {
		return MCPModeDefault
	}

	// Whitelist takes priority if set
	if len(settings.EnabledMcpjsonServers) > 0 {
		return MCPModeWhitelist
	}

	// Check for blacklist
	if len(settings.DisabledMcpjsonServers) > 0 {
		return MCPModeBlacklist
	}

	return MCPModeDefault
}

// GetLocalMCPState returns Local MCPs with their enabled state
func GetLocalMCPState(projectPath string) ([]MCPServer, error) {
	// Get all Local MCPs from .mcp.json
	mcpFile := filepath.Join(projectPath, ".mcp.json")
	data, err := os.ReadFile(mcpFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No .mcp.json = no Local MCPs
		}
		return nil, fmt.Errorf("failed to read .mcp.json: %w", err)
	}

	var mcpConfig projectMCPConfig
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		return nil, fmt.Errorf("failed to parse .mcp.json: %w", err)
	}

	if len(mcpConfig.MCPServers) == 0 {
		return nil, nil
	}

	// Get settings to determine enabled state
	settings, err := readProjectMCPSettings(projectPath)
	if err != nil {
		return nil, err
	}

	mode := GetMCPMode(projectPath)

	// Build result with enabled state
	var servers []MCPServer
	for name := range mcpConfig.MCPServers {
		enabled := isMCPEnabled(name, settings, mode)
		servers = append(servers, MCPServer{
			Name:    name,
			Source:  "local",
			Enabled: enabled,
		})
	}

	// Sort for consistent display
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

	return servers, nil
}

// isMCPEnabled determines if an MCP is enabled based on settings and mode
func isMCPEnabled(name string, settings *ProjectMCPSettings, mode MCPMode) bool {
	switch mode {
	case MCPModeWhitelist:
		// Whitelist: enabled only if in enabledMcpjsonServers
		for _, enabled := range settings.EnabledMcpjsonServers {
			if enabled == name {
				return true
			}
		}
		return false

	case MCPModeBlacklist:
		// Blacklist: enabled unless in disabledMcpjsonServers
		for _, disabled := range settings.DisabledMcpjsonServers {
			if disabled == name {
				return false
			}
		}
		return true

	default:
		// Default: all enabled
		return true
	}
}

// PruneMCPCache removes cache entries older than maxAge to prevent unbounded growth.
// Called periodically from the TUI tick handler.
func PruneMCPCache(maxAge time.Duration) {
	mcpInfoCacheMu.Lock()
	defer mcpInfoCacheMu.Unlock()
	now := time.Now()
	for path, t := range mcpCacheTimes {
		if now.Sub(t) > maxAge {
			delete(mcpInfoCache, path)
			delete(mcpCacheTimes, path)
		}
	}
}

// ClearMCPCache invalidates the MCP cache for a project path and all parent directories
// This is important because getMCPInfoUncached walks up parent directories to find .mcp.json
func ClearMCPCache(projectPath string) {
	mcpInfoCacheMu.Lock()
	defer mcpInfoCacheMu.Unlock()

	// Clear the exact path
	delete(mcpInfoCache, projectPath)
	delete(mcpCacheTimes, projectPath)

	// Also clear all parent directories (MCP lookup walks up the tree)
	currentPath := projectPath
	for {
		parent := filepath.Dir(currentPath)
		if parent == currentPath || parent == "/" || parent == "." {
			break
		}
		delete(mcpInfoCache, parent)
		delete(mcpCacheTimes, parent)
		currentPath = parent
	}
}

// ToggleLocalMCP toggles a Local MCP on/off
// It respects the existing mode (whitelist vs blacklist) or initializes with blacklist
func ToggleLocalMCP(projectPath, mcpName string) error {
	// Read current settings (preserving other fields)
	settingsPath := getProjectSettingsPath(projectPath)
	var rawSettings map[string]interface{}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read settings: %w", err)
		}
		// File doesn't exist, start fresh
		rawSettings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &rawSettings); err != nil {
			return fmt.Errorf("failed to parse settings: %w", err)
		}
	}

	// Detect mode
	mode := GetMCPMode(projectPath)

	// Get current enabled state
	settings, _ := readProjectMCPSettings(projectPath)
	currentlyEnabled := isMCPEnabled(mcpName, settings, mode)

	// Toggle based on mode
	switch mode {
	case MCPModeWhitelist:
		// Modify enabledMcpjsonServers
		enabled := getStringSlice(rawSettings, "enabledMcpjsonServers")
		if currentlyEnabled {
			// Disable: remove from whitelist
			enabled = removeFromSlice(enabled, mcpName)
		} else {
			// Enable: add to whitelist
			enabled = appendIfMissing(enabled, mcpName)
		}
		rawSettings["enabledMcpjsonServers"] = enabled

	case MCPModeBlacklist:
		// Modify disabledMcpjsonServers
		disabled := getStringSlice(rawSettings, "disabledMcpjsonServers")
		if currentlyEnabled {
			// Disable: add to blacklist
			disabled = appendIfMissing(disabled, mcpName)
		} else {
			// Enable: remove from blacklist
			disabled = removeFromSlice(disabled, mcpName)
		}
		rawSettings["disabledMcpjsonServers"] = disabled

	default:
		// No mode set, initialize with blacklist
		if currentlyEnabled {
			// Disable: add to blacklist
			rawSettings["disabledMcpjsonServers"] = []string{mcpName}
		}
		// Enable does nothing (already enabled by default)
	}

	// Ensure .claude directory exists
	claudeDir := filepath.Join(projectPath, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Write atomically (temp file + rename)
	newData, err := json.MarshalIndent(rawSettings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, settingsPath); err != nil {
		os.Remove(tmpPath) // Clean up on failure
		return fmt.Errorf("failed to rename settings file: %w", err)
	}

	// Clear cache so changes are reflected
	ClearMCPCache(projectPath)

	return nil
}

// getStringSlice extracts a string slice from a map
func getStringSlice(m map[string]interface{}, key string) []string {
	val, ok := m[key]
	if !ok {
		return nil
	}

	arr, ok := val.([]interface{})
	if !ok {
		return nil
	}

	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// removeFromSlice removes a string from a slice
func removeFromSlice(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

// appendIfMissing adds a string to a slice if not already present
func appendIfMissing(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
