package session

import (
	"fmt"
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
