package session

func loadCodexHomeSkillsManifest(codexHome string) (*ProjectSkillsManifest, error) {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return nil, err
	}
	return store.loadManifest()
}

// AttachSkillToCodexHome resolves and materializes one declarative group skill
// below the selected CODEX_HOME. Explicit skill attach remains project-scoped.
func AttachSkillToCodexHome(codexHome, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return nil, err
	}
	return store.attach(skillRef, sourceName)
}

func healthyManagedCodexHomeSkillAttachment(codexHome, skillID string) bool {
	store, err := newHomeSkillStore(codexHome, "Codex")
	return err == nil && store.healthy(skillID)
}
