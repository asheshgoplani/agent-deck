package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const agentHomeSkillsDir = "skills"

type homeSkillStore struct {
	home  string
	label string
}

func newHomeSkillStore(home, label string) (*homeSkillStore, error) {
	if hasParentPathComponent(home) {
		return nil, fmt.Errorf("%s home %q contains parent traversal", label, home)
	}
	clean := filepath.Clean(strings.TrimSpace(home))
	if clean == "." || !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("%s home must be an absolute path: %q", label, home)
	}
	clean = canonicalAgentHomePath(clean)
	if clean == filepath.Clean(string(os.PathSeparator)) {
		return nil, fmt.Errorf("refusing to use filesystem root as %s home", label)
	}
	return &homeSkillStore{home: clean, label: label}, nil
}

func (s *homeSkillStore) manifestPath() string {
	return filepath.Join(s.home, projectSkillsDirName, projectSkillsManifest)
}

func (s *homeSkillStore) target(targetRel string) (string, error) {
	targetRel = filepath.ToSlash(strings.TrimSpace(targetRel))
	if filepath.IsAbs(targetRel) || !targetPathUsesSkillDir(targetRel, agentHomeSkillsDir) {
		return "", fmt.Errorf("refusing %s home skill target outside skills dir: %s", s.label, targetRel)
	}
	base := filepath.Join(s.home, agentHomeSkillsDir)
	target := filepath.Join(s.home, filepath.FromSlash(targetRel))
	if !isContainedIn(base, target) {
		return "", fmt.Errorf("refusing %s home skill target outside skills dir: %s", s.label, target)
	}
	return target, nil
}

func (s *homeSkillStore) loadManifest() (*ProjectSkillsManifest, error) {
	if _, err := os.Stat(s.manifestPath()); os.IsNotExist(err) {
		return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
	}
	var manifest ProjectSkillsManifest
	if _, err := toml.DecodeFile(s.manifestPath(), &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse %s home skills manifest: %w", s.label, err)
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

func (s *homeSkillStore) saveManifest(manifest *ProjectSkillsManifest) error {
	if manifest == nil {
		manifest = &ProjectSkillsManifest{}
	}
	for i := range manifest.Skills {
		manifest.Skills[i] = normalizeAttachment(manifest.Skills[i])
	}
	sortAttachments(manifest.Skills)

	if err := os.MkdirAll(filepath.Dir(s.manifestPath()), 0o700); err != nil {
		return fmt.Errorf("create %s home skill manifest directory: %w", s.label, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(manifest); err != nil {
		return fmt.Errorf("encode %s home skills manifest: %w", s.label, err)
	}
	if err := atomicfile.WriteFile(s.manifestPath(), buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("save %s home skills manifest: %w", s.label, err)
	}
	return nil
}

func materializeHomeSkill(sourcePath, targetPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", err
	}
	resolvedSource := sourcePath
	if resolved, err := filepath.EvalSymlinks(sourcePath); err == nil {
		resolvedSource = resolved
	}
	base := filepath.Dir(targetPath)
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	if relTarget, err := filepath.Rel(base, resolvedSource); err == nil {
		if err := os.Symlink(relTarget, targetPath); err == nil {
			if _, err := os.Stat(targetPath); err == nil {
				return "symlink", nil
			}
			_ = os.Remove(targetPath)
		} else if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("target already exists and is not managed: %s", targetPath)
		}
	}

	if err := os.Mkdir(targetPath, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("target already exists and is not managed: %s", targetPath)
		}
		return "", err
	}
	if err := copyDir(sourcePath, targetPath); err != nil {
		_ = os.RemoveAll(targetPath)
		return "", err
	}
	return "copy", nil
}

func (s *homeSkillStore) attach(skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	candidate, err := ResolveSkillCandidate(skillRef, sourceName)
	if err != nil {
		return nil, err
	}
	if err := validateAttachableSkillCandidate(*candidate); err != nil {
		return nil, err
	}
	lock, err := acquireCodexConfigLock(s.manifestPath())
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	manifest, err := s.loadManifest()
	if err != nil {
		return nil, err
	}

	candidateID := buildSkillID(candidate.Source, candidate.Name)
	targetRel := buildProjectSkillTargetPath(agentHomeSkillsDir, candidate.EntryName)
	target, err := s.target(targetRel)
	if err != nil {
		return nil, err
	}
	for i := range manifest.Skills {
		existing := manifest.Skills[i]
		if normalizeSkillToken(skillIDForAttachment(existing)) != normalizeSkillToken(candidateID) {
			continue
		}
		if existing.TargetPath != targetRel {
			return nil, fmt.Errorf("%s home skill %s is managed at unexpected target %s", s.label, candidate.Name, existing.TargetPath)
		}
		if _, err := s.target(existing.TargetPath); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(target); err == nil {
			return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyAttached, candidate.Name)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		mode, err := materializeHomeSkill(candidate.SourcePath, target)
		if err != nil {
			return nil, err
		}
		existing.SourcePath = candidate.SourcePath
		existing.EntryName = candidate.EntryName
		existing.Mode = mode
		existing.AttachedAt = time.Now().Format(time.RFC3339)
		manifest.Skills[i] = normalizeAttachment(existing)
		if err := s.saveManifest(manifest); err != nil {
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
	mode, err := materializeHomeSkill(candidate.SourcePath, target)
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
	if err := s.saveManifest(manifest); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	return &attachment, nil
}

func (s *homeSkillStore) healthy(skillID string) bool {
	manifest, err := s.loadManifest()
	if err != nil {
		return false
	}
	for _, attachment := range manifest.Skills {
		if normalizeSkillToken(skillIDForAttachment(attachment)) != normalizeSkillToken(skillID) {
			continue
		}
		target, err := s.target(attachment.TargetPath)
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
