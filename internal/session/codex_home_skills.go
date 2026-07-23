package session

const codexHomeSkillsDir = agentHomeSkillsDir

func codexHomeSkillsManifestPath(codexHome string) string {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return ""
	}
	return store.manifestPath()
}

func validateCodexHome(codexHome string) (string, error) {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return "", err
	}
	return store.home, nil
}

func codexHomeSkillTarget(codexHome, targetRel string) (string, error) {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return "", err
	}
	return store.target(targetRel)
}

func loadCodexHomeSkillsManifest(codexHome string) (*ProjectSkillsManifest, error) {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return nil, err
	}
	return store.loadManifest()
}

func saveCodexHomeSkillsManifest(codexHome string, manifest *ProjectSkillsManifest) error {
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return err
	}
	return store.saveManifest(manifest)
}

func materializeCodexHomeSkill(sourcePath, targetPath string) (string, error) {
	return materializeHomeSkill(sourcePath, targetPath)
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
