package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const codexHomeSkillsDir = "skills"

func codexHomeSkillsManifestPath(codexHome string) string {
	return filepath.Join(codexHome, projectSkillsDirName, projectSkillsManifest)
}

func validateCodexHome(codexHome string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(codexHome))
	if clean == "." || !filepath.IsAbs(clean) {
		return "", fmt.Errorf("Codex home must be an absolute path: %q", codexHome)
	}
	if clean == filepath.Clean(string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to use filesystem root as Codex home")
	}
	return clean, nil
}

func codexHomeSkillTarget(codexHome, targetRel string) (string, error) {
	targetRel = filepath.ToSlash(strings.TrimSpace(targetRel))
	if filepath.IsAbs(targetRel) || !targetPathUsesSkillDir(targetRel, codexHomeSkillsDir) {
		return "", fmt.Errorf("refusing Codex home skill target outside skills dir: %s", targetRel)
	}
	base := filepath.Join(codexHome, codexHomeSkillsDir)
	target := filepath.Join(codexHome, filepath.FromSlash(targetRel))
	if !isContainedIn(base, target) {
		return "", fmt.Errorf("refusing Codex home skill target outside skills dir: %s", target)
	}
	return target, nil
}

func loadCodexHomeSkillsManifest(codexHome string) (*ProjectSkillsManifest, error) {
	manifestPath := codexHomeSkillsManifestPath(codexHome)
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
	}
	var manifest ProjectSkillsManifest
	if _, err := toml.DecodeFile(manifestPath, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse Codex home skills manifest: %w", err)
	}
	if manifest.Skills == nil {
		manifest.Skills = []ProjectSkillAttachment{}
	}
	for i := range manifest.Skills {
		manifest.Skills[i] = normalizeAttachment(manifest.Skills[i])
	}
	sortAttachments(manifest.Skills)
	return &manifest, nil
}

func saveCodexHomeSkillsManifest(codexHome string, manifest *ProjectSkillsManifest) error {
	if manifest == nil {
		manifest = &ProjectSkillsManifest{}
	}
	for i := range manifest.Skills {
		manifest.Skills[i] = normalizeAttachment(manifest.Skills[i])
	}
	sortAttachments(manifest.Skills)

	manifestPath := codexHomeSkillsManifestPath(codexHome)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return fmt.Errorf("create Codex home skill manifest directory: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(manifest); err != nil {
		return fmt.Errorf("encode Codex home skills manifest: %w", err)
	}
	tmpPath := manifestPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write Codex home skills manifest: %w", err)
	}
	if err := os.Rename(tmpPath, manifestPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save Codex home skills manifest: %w", err)
	}
	return nil
}

// AttachSkillToCodexHome resolves and materializes one declarative group skill
// below the selected CODEX_HOME. Explicit skill attach remains project-scoped.
func AttachSkillToCodexHome(codexHome, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	home, err := validateCodexHome(codexHome)
	if err != nil {
		return nil, err
	}
	candidate, err := ResolveSkillCandidate(skillRef, sourceName)
	if err != nil {
		return nil, err
	}
	if err := validateAttachableSkillCandidate(*candidate); err != nil {
		return nil, err
	}
	manifest, err := loadCodexHomeSkillsManifest(home)
	if err != nil {
		return nil, err
	}

	candidateID := buildSkillID(candidate.Source, candidate.Name)
	targetRel := buildProjectSkillTargetPath(codexHomeSkillsDir, candidate.EntryName)
	target, err := codexHomeSkillTarget(home, targetRel)
	if err != nil {
		return nil, err
	}
	for i := range manifest.Skills {
		existing := manifest.Skills[i]
		if normalizeSkillToken(skillIDForAttachment(existing)) != normalizeSkillToken(candidateID) {
			continue
		}
		if existing.TargetPath != targetRel {
			return nil, fmt.Errorf("Codex home skill %s is managed at unexpected target %s", candidate.Name, existing.TargetPath)
		}
		if _, err := codexHomeSkillTarget(home, existing.TargetPath); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(target); err == nil {
			return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyAttached, candidate.Name)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		mode, err := materializeSkill(candidate.SourcePath, target)
		if err != nil {
			return nil, err
		}
		existing.SourcePath = candidate.SourcePath
		existing.EntryName = candidate.EntryName
		existing.Mode = mode
		existing.AttachedAt = time.Now().Format(time.RFC3339)
		manifest.Skills[i] = normalizeAttachment(existing)
		if err := saveCodexHomeSkillsManifest(home, manifest); err != nil {
			return nil, err
		}
		updated := manifest.Skills[i]
		return &updated, nil
	}

	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("target already exists and is not managed: %s", target)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	mode, err := materializeSkill(candidate.SourcePath, target)
	if err != nil {
		return nil, err
	}
	attachment := normalizeAttachment(ProjectSkillAttachment{
		ID:         candidateID,
		Name:       candidate.Name,
		Source:     candidate.Source,
		SourcePath: candidate.SourcePath,
		EntryName:  candidate.EntryName,
		TargetPath: targetRel,
		Mode:       mode,
		AttachedAt: time.Now().Format(time.RFC3339),
	})
	manifest.Skills = append(manifest.Skills, attachment)
	if err := saveCodexHomeSkillsManifest(home, manifest); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	return &attachment, nil
}

func healthyManagedCodexHomeSkillAttachment(codexHome, skillID string) bool {
	home, err := validateCodexHome(codexHome)
	if err != nil {
		return false
	}
	manifest, err := loadCodexHomeSkillsManifest(home)
	if err != nil {
		return false
	}
	for _, attachment := range manifest.Skills {
		if normalizeSkillToken(skillIDForAttachment(attachment)) != normalizeSkillToken(skillID) {
			continue
		}
		target, err := codexHomeSkillTarget(home, attachment.TargetPath)
		if err != nil {
			return false
		}
		if _, err := os.Lstat(target); err != nil {
			return false
		}
		if attachment.Mode != "symlink" {
			return true
		}
		actual, actualErr := filepath.EvalSymlinks(target)
		expected, expectedErr := filepath.EvalSymlinks(attachment.SourcePath)
		return actualErr == nil && expectedErr == nil && actual == expected
	}
	return false
}
