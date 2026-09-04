package session

import (
	"fmt"
	"strings"
)

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
	return nil
}
