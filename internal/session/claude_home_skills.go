package session

func loadClaudeHomeSkillsManifest(claudeHome string) (*ProjectSkillsManifest, error) {
	store, err := newHomeSkillStore(claudeHome, "Claude")
	if err != nil {
		return nil, err
	}
	return store.loadManifest()
}

// AttachSkillToClaudeHome resolves and materializes one declarative group or
// conductor skill below the selected CLAUDE_CONFIG_DIR. Explicit skill attach
// remains project-scoped.
func AttachSkillToClaudeHome(claudeHome, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	store, err := newHomeSkillStore(claudeHome, "Claude")
	if err != nil {
		return nil, err
	}
	return store.attach(skillRef, sourceName)
}

func healthyManagedClaudeHomeSkillAttachment(claudeHome, skillID string) bool {
	store, err := newHomeSkillStore(claudeHome, "Claude")
	return err == nil && store.healthy(skillID)
}
