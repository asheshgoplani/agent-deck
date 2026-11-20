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

// ClaudeConfigDirHasLogin reports whether Claude would start authenticated
// from dir: an API key or OAuth token in the environment, a credentials file,
// or an oauthAccount in .claude.json (for the implicit ~/.claude, Claude keeps
// that file at ~/.claude.json). On macOS the login lives in the keychain,
// which cannot be probed here, so it is assumed present.
func ClaudeConfigDirHasLogin(dir string) bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return true
	}
	if runtime.GOOS == "darwin" {
		return true
	}
	if dir == "" {
		return false
	}
	if st, err := os.Stat(filepath.Join(dir, credentialsFileName)); err == nil && st.Size() > 0 {
		return true
	}
	files := []string{filepath.Join(dir, ".claude.json")}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(dir) == filepath.Join(home, ".claude") {
		files = append(files, filepath.Join(home, ".claude.json"))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var parsed struct {
			OAuthAccount json.RawMessage `json:"oauthAccount"`
		}
		if json.Unmarshal(data, &parsed) == nil && len(parsed.OAuthAccount) > 0 && string(parsed.OAuthAccount) != "null" {
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
// active profile's slot, then the only logged-in slot. With none, it refuses
// with a message naming the fix. An explicitly configured dir (group,
// conductor, env, profile, global) is the user's choice and is left alone.
func ResolveNewSessionAccount(cfg *UserConfig, groupPath string, parent *Instance) (string, error) {
	if account := DefaultAccountForNewSession(cfg, groupPath, parent); account != "" {
		return account, nil
	}
	probe := &Instance{Tool: "claude", GroupPath: groupPath}
	if parent != nil && parent.IsConductor {
		probe.IsConductor = true
		probe.ConductorName = parent.ResolveConductorNameThreadSafe()
	}
	dir, source := GetClaudeConfigDirSourceForInstance(probe)
	if source != "default" || ClaudeConfigDirHasLogin(dir) {
		return "", nil
	}
	configured := ConfiguredAccountNames(cfg)
	var loggedIn []string
	for _, name := range configured {
		if ClaudeConfigDirHasLogin(cfg.GetProfileClaudeConfigDir(name)) {
			loggedIn = append(loggedIn, name)
		}
	}
	prefer := []string{}
	if cfg != nil {
		prefer = append(prefer, strings.TrimSpace(cfg.Claude.DefaultAccount))
	}
	prefer = append(prefer, resolvedProcessProfile())
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
