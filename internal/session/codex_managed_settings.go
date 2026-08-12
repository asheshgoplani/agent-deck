package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const codexManagedSettingsManifestName = "managed-codex-settings.json"

type codexManagedSettingsManifest struct {
	RootKeys map[string]string `json:"root_keys"`
}

// ApplyCodexManagedSettings projects Agent Deck-owned root settings into a
// CODEX_HOME/config.toml while preserving Codex-owned tables and values.
func ApplyCodexManagedSettings(codexHome, model, reasoningEffort string) error {
	assignments := map[string]string{}
	if model = strings.TrimSpace(model); model != "" {
		assignments["model"] = model
	}
	if reasoningEffort = strings.TrimSpace(reasoningEffort); reasoningEffort != "" {
		assignments["model_reasoning_effort"] = reasoningEffort
	}
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(store.home, 0o700); err != nil {
		return fmt.Errorf("create Codex home %s: %w", store.home, err)
	}
	configPath := filepath.Join(store.home, "config.toml")
	lock, err := acquireCodexConfigLock(configPath)
	if err != nil {
		return err
	}
	defer lock.Release()
	manifestPath := filepath.Join(store.home, ".agent-deck", codexManagedSettingsManifestName)
	manifest, err := loadCodexManagedSettingsManifest(manifestPath)
	if err != nil {
		return err
	}
	if len(assignments) == 0 && len(manifest.RootKeys) == 0 {
		return nil
	}

	existing, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read Codex config %s: %w", configPath, err)
	}
	if len(existing) > 0 {
		var parsed map[string]any
		if err := toml.Unmarshal(existing, &parsed); err != nil {
			return fmt.Errorf("refusing to overwrite unparseable Codex config %s: %w", configPath, err)
		}
	}

	removals := make(map[string]string)
	for key, value := range manifest.RootKeys {
		if _, stillManaged := assignments[key]; !stillManaged {
			removals[key] = value
		}
	}
	updated, err := composeCodexRootAssignments(existing, assignments, removals)
	if err != nil {
		return err
	}
	if string(updated) != string(existing) {
		var verified map[string]any
		if err := toml.Unmarshal(updated, &verified); err != nil {
			return fmt.Errorf("refusing to write invalid Codex config: %w", err)
		}
		if err := atomicfile.WriteFile(configPath, updated, 0o600); err != nil {
			return fmt.Errorf("save Codex config %s: %w", configPath, err)
		}
	}
	if err := saveCodexManagedSettingsManifest(manifestPath, assignments); err != nil {
		return err
	}
	return nil
}

func loadCodexManagedSettingsManifest(path string) (codexManagedSettingsManifest, error) {
	manifest := codexManagedSettingsManifest{RootKeys: map[string]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return manifest, nil
	}
	if err != nil {
		return manifest, fmt.Errorf("read Codex managed-settings manifest %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("read Codex managed-settings manifest %s: %w", path, err)
	}
	if manifest.RootKeys == nil {
		manifest.RootKeys = map[string]string{}
	}
	return manifest, nil
}

func saveCodexManagedSettingsManifest(path string, rootKeys map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Codex managed-settings directory: %w", err)
	}
	data, err := json.Marshal(codexManagedSettingsManifest{RootKeys: rootKeys})
	if err != nil {
		return fmt.Errorf("encode Codex managed-settings manifest: %w", err)
	}
	if err := atomicfile.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("save Codex managed-settings manifest %s: %w", path, err)
	}
	return nil
}

func composeCodexRootAssignments(existing []byte, assignments, removals map[string]string) ([]byte, error) {
	encoded := make(map[string]string, len(assignments))
	for key, value := range assignments {
		data, err := toml.Marshal(map[string]string{key: value})
		if err != nil {
			return nil, err
		}
		encoded[key] = strings.TrimSuffix(string(data), "\n")
	}

	lines := strings.SplitAfter(string(existing), "\n")
	if len(existing) == 0 {
		lines = nil
	}
	var out strings.Builder
	currentTable := false
	emitted := make(map[string]bool, len(encoded))
	flushMissing := func() {
		for _, key := range []string{"model", "model_reasoning_effort"} {
			if line, ok := encoded[key]; ok && !emitted[key] {
				out.WriteString(line)
				out.WriteString(preferredLineEnding(existing))
				emitted[key] = true
			}
		}
	}

	for _, line := range lines {
		if _, isTable := tomlTableName(line); isTable {
			if !currentTable {
				flushMissing()
			}
			currentTable = true
			out.WriteString(line)
			continue
		}
		if !currentTable {
			if key, ok := codexRootAssignmentKey(line); ok {
				if replacement, managed := encoded[key]; managed {
					out.WriteString(leadingWhitespace(line))
					out.WriteString(replacement)
					out.WriteString(lineEnding(line))
					emitted[key] = true
					continue
				}
				if previousValue, remove := removals[key]; remove && codexRootAssignmentHasValue(line, key, previousValue) {
					continue
				}
			}
		}
		out.WriteString(line)
	}
	if !currentTable {
		flushMissing()
	}
	return []byte(out.String()), nil
}

func codexRootAssignmentHasValue(line, key, want string) bool {
	var parsed map[string]string
	if _, err := toml.Decode(line, &parsed); err != nil {
		return false
	}
	return parsed[key] == want
}

func codexRootAssignmentKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	for _, key := range []string{"model", "model_reasoning_effort"} {
		if strings.HasPrefix(trimmed, key) {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, key))
			if strings.HasPrefix(rest, "=") {
				return key, true
			}
		}
	}
	return "", false
}
