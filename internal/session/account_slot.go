package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// accountTool follows the same explicit provenance gate as the shell command
// builder. A regular shell command is not a tool-specific account operation.
func (i *Instance) accountTool() string {
	if i.Tool == "shell" && i.SubcommandPassthrough {
		if fields := strings.Fields(i.Command); len(fields) > 0 {
			return MatchTool(fields[0])
		}
	}
	return i.Tool
}

// ValidateAccount must run before registration or account-dependent loadout
// writes, as well as before starting an already registered session.
func (i *Instance) ValidateAccount() error {
	return ValidateAccountSlot(i.Account, i.accountTool())
}

// ValidateAccountSlot rejects a named slot that would otherwise fall through
// to ambient credentials. An empty slot preserves the existing default chain.
func ValidateAccountSlot(account, tool string) error {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil
	}
	config, err := LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load account configuration: %w", err)
	}
	if config == nil {
		return fmt.Errorf("account %q is not configured; run agent-deck accounts", account)
	}
	if _, exists := config.Profiles[account]; !exists {
		return fmt.Errorf("account %q is not configured; run agent-deck accounts", account)
	}
	if IsClaudeCompatible(tool) && config.GetProfileClaudeConfigDir(account) == "" {
		return fmt.Errorf("account %q has no Claude config_dir", account)
	}
	if IsCodexCompatible(tool) && config.GetProfileCodexConfigDir(account) == "" {
		return fmt.Errorf("account %q has no Codex config_dir", account)
	}
	if tool == "deepseek" && config.GetProfileDeepSeekConfigDir(account) == "" {
		return fmt.Errorf("account %q has no DeepSeek config_dir", account)
	}
	return nil
}

// DefaultAccountForNewSession returns the [claude].default_account slot a new
// Claude session starts on when nobody picked one (#2895), or "" to inherit
// the conductor/group/env chain. Callers apply it to Claude sessions only.
//
// It is a creation-time prefill, not a level of resolveClaudeConfigDir: the
// slot is stored on Instance.Account, so changing the default never moves an
// existing session (that stays switch-account's job). Instance.Account beats
// group and conductor config_dir, so the default yields to a
// [groups."<groupPath>".claude] (or ancestor group) config_dir and to a parent
// conductor's [conductors.<name>.claude] config_dir, which the user scoped
// more narrowly than one machine-wide switch. A slot no longer configured
// falls back to inherit with a log line instead of failing the create.
func DefaultAccountForNewSession(cfg *UserConfig, groupPath string, parent *Instance) string {
	if cfg == nil {
		return ""
	}
	account := strings.TrimSpace(cfg.Claude.DefaultAccount)
	if account == "" {
		return ""
	}
	if cfg.GetGroupClaudeConfigDir(groupPath) != "" {
		return ""
	}
	if parent != nil && parent.IsConductor && cfg.GetConductorClaudeConfigDir(parent.ResolveConductorNameThreadSafe()) != "" {
		return ""
	}
	if !slices.Contains(ConfiguredAccountNames(cfg), account) {
		sessionLog.Warn("default_account_not_configured", slog.String("account", account))
		return ""
	}
	return account
}

// ClaudeConfigDirHasLogin reports whether Claude may start authenticated from
// dir. It fails safe toward true: it returns false only when every known auth
// signal is absent and every file it needs was readable. Signals: API key,
// auth token or OAuth token in the environment; Bedrock, Vertex or Foundry
// mode; a credentials file; oauthAccount, primaryApiKey or apiKeyHelper in
// .claude.json (for the implicit ~/.claude, Claude keeps that file at
// ~/.claude.json) or apiKeyHelper in settings.json. It checks presence only
// and never reads or logs a secret value. On macOS the login lives in the
// keychain, which cannot be probed, so it is assumed present.
func ClaudeConfigDirHasLogin(dir string) bool { return ClaudeLoginProbe(dir) }

// ClaudeLoginProbe is the login check ResolveNewSessionAccount uses; a test
// binary whose fixtures create Claude sessions on a bare temp HOME replaces it.
var ClaudeLoginProbe = claudeConfigDirHasLoginOnDisk

var claudeManagedSettingsPath = "/etc/claude-code/managed-settings.json"

var claudeAuthEnvSignals = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
}

// jsonFileHasAnyKey reports whether the JSON object in path has any of keys set
// to a non-null value. A key named "env" counts only when it holds at least one
// entry (settings files apply that block to every session, which is the
// documented way to set Bedrock, Vertex or an API key). A missing file is a
// definite no; any other read or parse problem is unknown and counts as present.
func jsonFileHasAnyKey(path string, keys ...string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return !os.IsNotExist(err)
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(data, &parsed) != nil {
		return true
	}
	for _, k := range keys {
		v, ok := parsed[k]
		if !ok || len(v) == 0 || string(v) == "null" {
			continue
		}
		if k == "env" {
			var env map[string]json.RawMessage
			if json.Unmarshal(v, &env) != nil || len(env) > 0 {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// claudeOnboardingMayBePending reports whether Claude's first-run (onboarding)
// screen would show for dir. Claude Code runs its onboarding flow at startup
// unless hasCompletedOnboarding is true in the config file, which is
// <dir>/.claude.json (and ~/.claude.json for the implicit ~/.claude). It is
// false (no first run) when any of those files says onboarding is complete,
// and also when a file is unreadable or unparseable (unknown counts as no
// change). Only the flag is read.
func claudeOnboardingMayBePending(files []string) bool {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false
		}
		var parsed map[string]json.RawMessage
		if json.Unmarshal(data, &parsed) != nil {
			return false
		}
		if string(parsed["hasCompletedOnboarding"]) == "true" {
			return false
		}
	}
	return true
}

// claudeConfigDirHasLoginOnDisk returns false only for a really fresh dir: the
// first-run screen would show (onboarding not completed) AND no auth signal is
// present anywhere. Anything else, including anything unreadable, is true.
func claudeConfigDirHasLoginOnDisk(dir string) bool {
	for _, k := range claudeAuthEnvSignals {
		if os.Getenv(k) != "" {
			return true
		}
	}
	if runtime.GOOS == "darwin" || dir == "" {
		return true
	}
	files := []string{filepath.Join(dir, ".claude.json")}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(dir) == filepath.Join(home, ".claude") {
		files = append(files, filepath.Join(home, ".claude.json"))
	}
	if !claudeOnboardingMayBePending(files) {
		return true
	}
	if st, err := os.Stat(filepath.Join(dir, credentialsFileName)); err == nil {
		if st.Size() > 0 {
			return true
		}
	} else if !os.IsNotExist(err) {
		return true
	}
	for _, f := range []string{filepath.Join(dir, "settings.json"), filepath.Join(dir, "settings.local.json"), claudeManagedSettingsPath} {
		if jsonFileHasAnyKey(f, "apiKeyHelper", "env") {
			return true
		}
	}
	for _, f := range files {
		if jsonFileHasAnyKey(f, "oauthAccount", "primaryApiKey", "apiKeyHelper") {
			return true
		}
	}
	return false
}

// claudeConfigDirHasPositiveLogin reports whether dir shows a login: a
// non-empty credentials file, or oauthAccount, primaryApiKey or apiKeyHelper
// in its .claude.json, or apiKeyHelper in its settings files. Unreadable or
// unparseable files do not count. On macOS the keychain cannot be probed, so a
// configured dir is assumed logged in.
func claudeConfigDirHasPositiveLogin(dir string) bool {
	if dir == "" {
		return false
	}
	if runtime.GOOS == "darwin" {
		return true
	}
	if st, err := os.Stat(filepath.Join(dir, credentialsFileName)); err == nil && st.Size() > 0 {
		return true
	}
	positive := func(path string, keys ...string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var parsed map[string]json.RawMessage
		if json.Unmarshal(data, &parsed) != nil {
			return false
		}
		for _, k := range keys {
			if v, ok := parsed[k]; ok && len(v) > 0 && string(v) != "null" {
				return true
			}
		}
		return false
	}
	return positive(filepath.Join(dir, ".claude.json"), "oauthAccount", "primaryApiKey", "apiKeyHelper") ||
		positive(filepath.Join(dir, "settings.json"), "apiKeyHelper") ||
		positive(filepath.Join(dir, "settings.local.json"), "apiKeyHelper")
}

// claudeSpawnAuthUnknown reports whether a new session could get its auth from
// something agent-deck cannot see from here: a [claude].command alias or
// wrapper, an env_file or inline env at the claude, group or conductor level,
// or [shell] env_files or an init script. Any of them counts as unknown.
func claudeSpawnAuthUnknown(cfg *UserConfig) bool {
	if cfg == nil {
		return false
	}
	customCmd := func(c string) bool { c = strings.TrimSpace(c); return c != "" && c != "claude" }
	if customCmd(cfg.Claude.Command) || strings.TrimSpace(cfg.Claude.EnvFile) != "" ||
		len(cfg.Shell.EnvFiles) > 0 || strings.TrimSpace(cfg.Shell.InitScript) != "" {
		return true
	}
	for _, g := range cfg.Groups {
		if customCmd(g.Claude.Command) || strings.TrimSpace(g.Claude.EnvFile) != "" || len(g.Claude.Env) > 0 {
			return true
		}
	}
	for _, c := range cfg.Conductors {
		if customCmd(c.Claude.Command) || strings.TrimSpace(c.Claude.EnvFile) != "" || len(c.Claude.Env) > 0 {
			return true
		}
	}
	return false
}

// ResolveNewSessionAccount returns the account slot a new Claude session
// should start on. It applies DefaultAccountForNewSession first. When that
// leaves the session on the implicit ~/.claude (source "default") and that dir
// has no login, Claude would open its first-run screen, so it falls back to a
// configured account that is logged in: [claude].default_account, then the
// only logged-in slot. (The active profile's own slot never reaches this
// point: a profile config_dir resolves with source "profile".) With none, it refuses
// with a message naming the fix. An explicitly configured dir (group,
// conductor, env, profile, global) is the user's choice and is left alone.
//
// It only applies to the plain "claude" tool: a custom Claude-compatible tool
// brings its own env and auth. It also leaves the session alone whenever the
// auth could come from something it cannot see (see claudeSpawnAuthUnknown).
func ResolveNewSessionAccount(cfg *UserConfig, tool, groupPath string, parent *Instance) (string, error) {
	if tool != "claude" {
		return "", nil
	}
	if account := DefaultAccountForNewSession(cfg, groupPath, parent); account != "" {
		return account, nil
	}
	probe := &Instance{Tool: "claude", GroupPath: groupPath}
	if parent != nil && parent.IsConductor {
		probe.IsConductor = true
		probe.ConductorName = parent.ResolveConductorNameThreadSafe()
	}
	dir, source := GetClaudeConfigDirSourceForInstance(probe)
	if source != "default" || ClaudeConfigDirHasLogin(dir) || claudeSpawnAuthUnknown(cfg) {
		return "", nil
	}
	configured := ConfiguredAccountNames(cfg)
	var loggedIn []string
	for _, name := range configured {
		// A fallback target must show a POSITIVE login; "unknown" is enough to
		// leave a session alone but not to move one onto another account.
		if claudeConfigDirHasPositiveLogin(cfg.GetProfileClaudeConfigDir(name)) {
			loggedIn = append(loggedIn, name)
		}
	}
	prefer := []string{}
	if cfg != nil {
		prefer = append(prefer, strings.TrimSpace(cfg.Claude.DefaultAccount))
	}
	for _, name := range prefer {
		if name != "" && slices.Contains(loggedIn, name) {
			return name, nil
		}
	}
	if len(loggedIn) == 1 {
		return loggedIn[0], nil
	}
	if len(loggedIn) > 1 {
		return "", fmt.Errorf("%s has no Claude login and several accounts are available (%s); pick one with --account or set [claude].default_account", dir, strings.Join(loggedIn, ", "))
	}
	return "", fmt.Errorf("%s has no Claude login, so a new session would open Claude's first-run screen; run `claude /login` for that profile or add a logged-in account with `agent-deck accounts` and set [claude].default_account", dir)
}
