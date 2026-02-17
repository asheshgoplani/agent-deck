package session

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	skillsDirName            = "skills"
	skillSourcesFileName     = "sources.toml"
	projectSkillsDirName     = ".agent-deck"
	projectSkillsManifest    = "skills.toml"
	projectClaudeSkillsDir   = ".claude/skills"
	defaultSkillSourcePool   = "pool"
	defaultSkillSourceClaude = "claude-global"
)

var (
	ErrSkillSourceExists    = errors.New("skill source already exists")
	ErrSkillSourceNotFound  = errors.New("skill source not found")
	ErrSkillNotFound        = errors.New("skill not found")
	ErrSkillAmbiguous       = errors.New("skill reference is ambiguous")
	ErrSkillAlreadyAttached = errors.New("skill already attached")
	ErrSkillNotAttached     = errors.New("skill not attached")
	ErrSkillTargetConflict  = errors.New("skill target path conflict")
)

// SkillSourceDef defines a named source path for discovering skills.
type SkillSourceDef struct {
	Path        string `toml:"path"`
	Description string `toml:"description,omitempty"`
	Enabled     *bool  `toml:"enabled,omitempty"`
}

// IsEnabled returns true when the source should be considered during discovery.
func (s SkillSourceDef) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// SkillSourcesConfig is persisted in ~/.agent-deck/skills/sources.toml.
type SkillSourcesConfig struct {
	Sources map[string]SkillSourceDef `toml:"sources"`
}

// SkillSource is a resolved source used for display and discovery.
type SkillSource struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// SkillCandidate is a discovered skill from one source.
type SkillCandidate struct {
	ID          string `json:"id"` // source/name
	Name        string `json:"name"`
	Source      string `json:"source"`
	SourcePath  string `json:"source_path"`
	EntryName   string `json:"entry_name"` // directory/file name in source
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"` // "dir" or "file"
}

// ProjectSkillAttachment is persisted in .agent-deck/skills.toml.
type ProjectSkillAttachment struct {
	ID         string `toml:"id"`
	Name       string `toml:"name"`
	Source     string `toml:"source"`
	SourcePath string `toml:"source_path"`
	EntryName  string `toml:"entry_name"`
	TargetPath string `toml:"target_path"` // relative to project path
	Mode       string `toml:"mode,omitempty"`
	AttachedAt string `toml:"attached_at,omitempty"`
}

// ProjectSkillsManifest is the project-local attachment state.
type ProjectSkillsManifest struct {
	Skills []ProjectSkillAttachment `toml:"skills"`
}

func skillBoolPtr(v bool) *bool {
	b := v
	return &b
}

func normalizeSkillToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func buildSkillID(source, name string) string {
	return strings.TrimSpace(source) + "/" + strings.TrimSpace(name)
}

func skillIDForAttachment(a ProjectSkillAttachment) string {
	if strings.TrimSpace(a.ID) != "" {
		return strings.TrimSpace(a.ID)
	}
	return buildSkillID(a.Source, a.Name)
}

func expandSkillPath(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Clean(filepath.Join(home, path[2:]))
		}
	}
	return filepath.Clean(path)
}

func resolveSkillSourcePath(path string) (string, error) {
	resolvedPath := expandSkillPath(path)
	if resolvedPath == "" {
		return "", fmt.Errorf("source path is required")
	}
	if !filepath.IsAbs(resolvedPath) {
		absPath, err := filepath.Abs(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve source path: %w", err)
		}
		resolvedPath = absPath
	}
	return filepath.Clean(resolvedPath), nil
}

func isContainedIn(basePath, targetPath string) bool {
	normalizedBase := filepath.Clean(basePath)
	normalizedTarget := filepath.Clean(targetPath)
	if normalizedBase == normalizedTarget {
		return true
	}
	return strings.HasPrefix(normalizedTarget, normalizedBase+string(os.PathSeparator))
}

// GetSkillsRootPath returns ~/.agent-deck/skills.
func GetSkillsRootPath() (string, error) {
	base, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, skillsDirName), nil
}

// GetSkillSourcesPath returns ~/.agent-deck/skills/sources.toml.
func GetSkillSourcesPath() (string, error) {
	root, err := GetSkillsRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, skillSourcesFileName), nil
}

// GetSkillPoolPath returns ~/.agent-deck/skills/pool.
func GetSkillPoolPath() (string, error) {
	root, err := GetSkillsRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "pool"), nil
}

func defaultSkillSources() map[string]SkillSourceDef {
	poolPath, _ := GetSkillPoolPath()
	claudePath := filepath.Join(GetClaudeConfigDir(), "skills")
	return map[string]SkillSourceDef{
		defaultSkillSourcePool: {
			Path:        poolPath,
			Description: "Managed Agent Deck skill pool",
			Enabled:     skillBoolPtr(true),
		},
		defaultSkillSourceClaude: {
			Path:        claudePath,
			Description: "Claude global skills directory",
			Enabled:     skillBoolPtr(true),
		},
	}
}

// LoadSkillSources loads the global source registry.
// If no registry exists yet, defaults are returned.
func LoadSkillSources() (map[string]SkillSourceDef, error) {
	sourcesPath, err := GetSkillSourcesPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(sourcesPath); os.IsNotExist(err) {
		return defaultSkillSources(), nil
	}

	var cfg SkillSourcesConfig
	if _, err := toml.DecodeFile(sourcesPath, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse skill sources: %w", err)
	}
	if cfg.Sources == nil {
		cfg.Sources = make(map[string]SkillSourceDef)
	}

	for name, def := range cfg.Sources {
		def.Path = expandSkillPath(def.Path)
		cfg.Sources[name] = def
	}

	return cfg.Sources, nil
}

// SaveSkillSources writes the source registry atomically.
func SaveSkillSources(sources map[string]SkillSourceDef) error {
	sourcesPath, err := GetSkillSourcesPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(sourcesPath), 0o700); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	cleaned := make(map[string]SkillSourceDef, len(sources))
	for name, def := range sources {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		def.Path = expandSkillPath(def.Path)
		cleaned[name] = def
	}

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(SkillSourcesConfig{Sources: cleaned}); err != nil {
		return fmt.Errorf("failed to encode skill sources: %w", err)
	}

	tmpPath := sourcesPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to write skill sources: %w", err)
	}
	if err := os.Rename(tmpPath, sourcesPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to save skill sources: %w", err)
	}
	return nil
}

// AddSkillSource adds a new named local source path.
func AddSkillSource(name, path, description string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("source name is required")
	}

	sources, err := LoadSkillSources()
	if err != nil {
		return err
	}

	if _, exists := sources[name]; exists {
		return fmt.Errorf("%w: %s", ErrSkillSourceExists, name)
	}

	resolvedPath, err := resolveSkillSourcePath(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return fmt.Errorf("invalid source path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path must be a directory")
	}

	sources[name] = SkillSourceDef{
		Path:        resolvedPath,
		Description: strings.TrimSpace(description),
		Enabled:     skillBoolPtr(true),
	}

	return SaveSkillSources(sources)
}

// RemoveSkillSource removes a named source.
func RemoveSkillSource(name string) error {
	sources, err := LoadSkillSources()
	if err != nil {
		return err
	}

	if _, exists := sources[name]; !exists {
		return fmt.Errorf("%w: %s", ErrSkillSourceNotFound, name)
	}

	delete(sources, name)
	return SaveSkillSources(sources)
}

// ListSkillSources returns sorted source definitions for display.
func ListSkillSources() ([]SkillSource, error) {
	sources, err := LoadSkillSources()
	if err != nil {
		return nil, err
	}

	result := make([]SkillSource, 0, len(sources))
	for name, def := range sources {
		result = append(result, SkillSource{
			Name:        name,
			Path:        expandSkillPath(def.Path),
			Description: def.Description,
			Enabled:     def.IsEnabled(),
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func parseSkillMetadata(skillMDPath, fallbackName string) (string, string) {
	content, err := os.ReadFile(skillMDPath)
	if err != nil {
		return fallbackName, ""
	}

	text := string(content)
	name := fallbackName
	description := ""

	if strings.HasPrefix(text, "---\n") {
		rest := text[4:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			header := rest[:idx]
			for _, line := range strings.Split(header, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, ":", 2)
				if len(parts) != 2 {
					continue
				}
				key := strings.TrimSpace(strings.ToLower(parts[0]))
				val := strings.TrimSpace(parts[1])
				val = strings.Trim(val, `"'`)
				switch key {
				case "name":
					if val != "" {
						name = val
					}
				case "description":
					if val != "" {
						description = val
					}
				}
			}
		}
	}

	if description == "" {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				description = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}

	return strings.TrimSpace(name), strings.TrimSpace(description)
}

func discoverSkillsFromSource(sourceName string, source SkillSourceDef) ([]SkillCandidate, error) {
	sourcePath := expandSkillPath(source.Path)
	if sourcePath == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read source %s: %w", sourceName, err)
	}

	candidates := make([]SkillCandidate, 0, len(entries))
	seen := make(map[string]bool)

	for _, entry := range entries {
		entryPath := filepath.Join(sourcePath, entry.Name())
		info, err := os.Stat(entryPath)
		if err != nil {
			continue
		}

		var candidate *SkillCandidate
		if info.IsDir() {
			skillMDPath := filepath.Join(entryPath, "SKILL.md")
			if _, err := os.Stat(skillMDPath); err != nil {
				continue
			}
			name, desc := parseSkillMetadata(skillMDPath, entry.Name())
			if name == "" {
				name = entry.Name()
			}
			c := SkillCandidate{
				ID:          buildSkillID(sourceName, name),
				Name:        name,
				Source:      sourceName,
				SourcePath:  entryPath,
				EntryName:   entry.Name(),
				Description: desc,
				Kind:        "dir",
			}
			candidate = &c
		} else if strings.HasSuffix(strings.ToLower(entry.Name()), ".skill") {
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			c := SkillCandidate{
				ID:         buildSkillID(sourceName, name),
				Name:       name,
				Source:     sourceName,
				SourcePath: entryPath,
				EntryName:  entry.Name(),
				Kind:       "file",
			}
			candidate = &c
		}

		if candidate == nil {
			continue
		}

		if seen[candidate.ID] {
			continue
		}
		seen[candidate.ID] = true
		candidates = append(candidates, *candidate)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name == candidates[j].Name {
			return candidates[i].Source < candidates[j].Source
		}
		return candidates[i].Name < candidates[j].Name
	})

	return candidates, nil
}

// ListAvailableSkills returns all discovered skills across enabled sources.
func ListAvailableSkills() ([]SkillCandidate, error) {
	sources, err := ListSkillSources()
	if err != nil {
		return nil, err
	}

	candidates := make([]SkillCandidate, 0)
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		found, err := discoverSkillsFromSource(source.Name, SkillSourceDef{
			Path:        source.Path,
			Description: source.Description,
			Enabled:     skillBoolPtr(source.Enabled),
		})
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Name == candidates[j].Name {
			if candidates[i].Source == candidates[j].Source {
				return candidates[i].EntryName < candidates[j].EntryName
			}
			return candidates[i].Source < candidates[j].Source
		}
		return candidates[i].Name < candidates[j].Name
	})

	return candidates, nil
}

func matchesSkillReference(candidate SkillCandidate, skillRef string) bool {
	ref := normalizeSkillToken(skillRef)
	if ref == "" {
		return false
	}
	return normalizeSkillToken(candidate.ID) == ref ||
		normalizeSkillToken(candidate.Name) == ref ||
		normalizeSkillToken(candidate.EntryName) == ref
}

// ResolveSkillCandidate resolves one skill from discovery by name or source/name.
func ResolveSkillCandidate(skillRef, sourceName string) (*SkillCandidate, error) {
	all, err := ListAvailableSkills()
	if err != nil {
		return nil, err
	}

	sourceName = strings.TrimSpace(sourceName)
	ref := strings.TrimSpace(skillRef)
	if strings.Contains(ref, "/") && sourceName == "" {
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 && parts[0] != "" {
			sourceName = parts[0]
			ref = parts[1]
		}
	}

	matches := make([]SkillCandidate, 0)
	for _, candidate := range all {
		if sourceName != "" && normalizeSkillToken(candidate.Source) != normalizeSkillToken(sourceName) {
			continue
		}
		if matchesSkillReference(candidate, ref) {
			matches = append(matches, candidate)
		}
	}

	if len(matches) == 0 {
		if sourceName == "" {
			return nil, fmt.Errorf("%w: %s", ErrSkillNotFound, ref)
		}
		return nil, fmt.Errorf("%w: %s (source: %s)", ErrSkillNotFound, ref, sourceName)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, fmt.Sprintf("%s (%s)", m.Name, m.Source))
		}
		sort.Strings(names)
		return nil, fmt.Errorf("%w: %s (%s)", ErrSkillAmbiguous, ref, strings.Join(names, ", "))
	}

	result := matches[0]
	return &result, nil
}

// GetProjectSkillsManifestPath returns <project>/.agent-deck/skills.toml.
func GetProjectSkillsManifestPath(projectPath string) string {
	return filepath.Join(projectPath, projectSkillsDirName, projectSkillsManifest)
}

// GetProjectClaudeSkillsPath returns <project>/.claude/skills.
func GetProjectClaudeSkillsPath(projectPath string) string {
	return filepath.Join(projectPath, filepath.FromSlash(projectClaudeSkillsDir))
}

func normalizeAttachment(a ProjectSkillAttachment) ProjectSkillAttachment {
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" {
		a.ID = buildSkillID(strings.TrimSpace(a.Source), strings.TrimSpace(a.Name))
	}
	a.Name = strings.TrimSpace(a.Name)
	a.Source = strings.TrimSpace(a.Source)
	a.SourcePath = filepath.Clean(a.SourcePath)
	a.EntryName = strings.TrimSpace(a.EntryName)
	a.TargetPath = filepath.ToSlash(strings.TrimSpace(a.TargetPath))
	if a.TargetPath == "" && a.EntryName != "" {
		a.TargetPath = filepath.ToSlash(filepath.Join(filepath.FromSlash(projectClaudeSkillsDir), a.EntryName))
	}
	if a.AttachedAt == "" {
		a.AttachedAt = time.Now().Format(time.RFC3339)
	}
	if a.Mode == "" {
		a.Mode = "symlink"
	}
	return a
}

func sortAttachments(skills []ProjectSkillAttachment) {
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			if skills[i].Source == skills[j].Source {
				return skills[i].EntryName < skills[j].EntryName
			}
			return skills[i].Source < skills[j].Source
		}
		return skills[i].Name < skills[j].Name
	})
}

// LoadProjectSkillsManifest reads project attachment state.
func LoadProjectSkillsManifest(projectPath string) (*ProjectSkillsManifest, error) {
	manifestPath := GetProjectSkillsManifestPath(projectPath)
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
	}

	var manifest ProjectSkillsManifest
	if _, err := toml.DecodeFile(manifestPath, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse skills manifest: %w", err)
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

// SaveProjectSkillsManifest writes project attachment state atomically.
func SaveProjectSkillsManifest(projectPath string, manifest *ProjectSkillsManifest) error {
	if manifest == nil {
		manifest = &ProjectSkillsManifest{}
	}
	if manifest.Skills == nil {
		manifest.Skills = []ProjectSkillAttachment{}
	}
	for i := range manifest.Skills {
		manifest.Skills[i] = normalizeAttachment(manifest.Skills[i])
	}
	sortAttachments(manifest.Skills)

	manifestPath := GetProjectSkillsManifestPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		return fmt.Errorf("failed to create manifest directory: %w", err)
	}

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("failed to encode skills manifest: %w", err)
	}

	tmpPath := manifestPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("failed to write skills manifest: %w", err)
	}
	if err := os.Rename(tmpPath, manifestPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to save skills manifest: %w", err)
	}
	return nil
}

// GetAttachedProjectSkills returns manifest-backed attached skills.
func GetAttachedProjectSkills(projectPath string) ([]ProjectSkillAttachment, error) {
	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return nil, err
	}
	result := make([]ProjectSkillAttachment, len(manifest.Skills))
	copy(result, manifest.Skills)
	sortAttachments(result)
	return result, nil
}

// ListMaterializedProjectSkills returns all entries currently present in .claude/skills.
func ListMaterializedProjectSkills(projectPath string) ([]string, error) {
	skillsDir := GetProjectClaudeSkillsPath(projectPath)
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		info, err := os.Lstat(srcPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			realPath, err := filepath.EvalSymlinks(srcPath)
			if err != nil {
				return err
			}
			info, err = os.Stat(realPath)
			if err != nil {
				return err
			}
			srcPath = realPath
		}

		if info.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func materializeSkill(sourcePath, targetPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return "", err
	}

	if err := os.RemoveAll(targetPath); err != nil {
		return "", err
	}

	relTarget, relErr := filepath.Rel(filepath.Dir(targetPath), sourcePath)
	if relErr == nil {
		if err := os.Symlink(relTarget, targetPath); err == nil {
			return "symlink", nil
		}
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		if err := copyDir(sourcePath, targetPath); err != nil {
			return "", err
		}
	} else {
		if err := copyFile(sourcePath, targetPath); err != nil {
			return "", err
		}
	}
	return "copy", nil
}

func resolveTargetPath(projectPath, targetPath string) string {
	if filepath.IsAbs(targetPath) {
		return filepath.Clean(targetPath)
	}
	return filepath.Clean(filepath.Join(projectPath, filepath.FromSlash(targetPath)))
}

func removeAttachmentTarget(projectPath string, attachment ProjectSkillAttachment) error {
	targetPath := resolveTargetPath(projectPath, attachment.TargetPath)
	base := GetProjectClaudeSkillsPath(projectPath)
	if !isContainedIn(base, targetPath) {
		return fmt.Errorf("refusing to remove path outside project skills dir: %s", targetPath)
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return err
	}
	return nil
}

func buildAttachment(projectPath string, candidate SkillCandidate, mode string) ProjectSkillAttachment {
	targetRel := filepath.ToSlash(filepath.Join(filepath.FromSlash(projectClaudeSkillsDir), candidate.EntryName))
	return normalizeAttachment(ProjectSkillAttachment{
		ID:         buildSkillID(candidate.Source, candidate.Name),
		Name:       candidate.Name,
		Source:     candidate.Source,
		SourcePath: candidate.SourcePath,
		EntryName:  candidate.EntryName,
		TargetPath: targetRel,
		Mode:       mode,
		AttachedAt: time.Now().Format(time.RFC3339),
	})
}

func attachSkillCandidate(projectPath string, candidate SkillCandidate) (*ProjectSkillAttachment, error) {
	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return nil, err
	}

	candidateID := buildSkillID(candidate.Source, candidate.Name)
	for i := range manifest.Skills {
		existing := manifest.Skills[i]
		if normalizeSkillToken(skillIDForAttachment(existing)) == normalizeSkillToken(candidateID) {
			targetPath := resolveTargetPath(projectPath, existing.TargetPath)
			if _, err := os.Lstat(targetPath); err == nil {
				return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyAttached, candidate.Name)
			}

			mode, err := materializeSkill(candidate.SourcePath, targetPath)
			if err != nil {
				return nil, err
			}

			existing.SourcePath = candidate.SourcePath
			existing.EntryName = candidate.EntryName
			existing.Mode = mode
			existing.AttachedAt = time.Now().Format(time.RFC3339)
			manifest.Skills[i] = normalizeAttachment(existing)

			if err := SaveProjectSkillsManifest(projectPath, manifest); err != nil {
				return nil, err
			}
			updated := manifest.Skills[i]
			return &updated, nil
		}
	}

	attachment := buildAttachment(projectPath, candidate, "")
	targetPath := resolveTargetPath(projectPath, attachment.TargetPath)

	for _, existing := range manifest.Skills {
		if normalizeSkillToken(existing.TargetPath) == normalizeSkillToken(attachment.TargetPath) {
			return nil, fmt.Errorf("target already managed by %s", existing.Name)
		}
	}

	if _, err := os.Lstat(targetPath); err == nil {
		return nil, fmt.Errorf("target already exists and is not managed: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	mode, err := materializeSkill(candidate.SourcePath, targetPath)
	if err != nil {
		return nil, err
	}
	attachment.Mode = mode
	attachment = normalizeAttachment(attachment)

	manifest.Skills = append(manifest.Skills, attachment)
	if err := SaveProjectSkillsManifest(projectPath, manifest); err != nil {
		_ = removeAttachmentTarget(projectPath, attachment)
		return nil, err
	}

	return &attachment, nil
}

// AttachSkillToProject resolves and attaches one skill into <project>/.claude/skills.
func AttachSkillToProject(projectPath, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	candidate, err := ResolveSkillCandidate(skillRef, sourceName)
	if err != nil {
		return nil, err
	}
	return attachSkillCandidate(projectPath, *candidate)
}

func matchesAttachmentReference(a ProjectSkillAttachment, skillRef, sourceName string) bool {
	if strings.TrimSpace(sourceName) != "" && normalizeSkillToken(a.Source) != normalizeSkillToken(sourceName) {
		return false
	}

	ref := strings.TrimSpace(skillRef)
	if ref == "" {
		return false
	}
	if strings.Contains(ref, "/") && strings.TrimSpace(sourceName) == "" {
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 {
			if normalizeSkillToken(a.Source) != normalizeSkillToken(parts[0]) {
				return false
			}
			ref = parts[1]
		}
	}

	refNorm := normalizeSkillToken(ref)
	return normalizeSkillToken(a.Name) == refNorm ||
		normalizeSkillToken(a.EntryName) == refNorm ||
		normalizeSkillToken(skillIDForAttachment(a)) == normalizeSkillToken(skillRef)
}

// DetachSkillFromProject detaches one managed skill and removes its manifest entry.
func DetachSkillFromProject(projectPath, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return nil, err
	}

	matchedIdx := -1
	matches := 0
	for i, attachment := range manifest.Skills {
		if matchesAttachmentReference(attachment, skillRef, sourceName) {
			matchedIdx = i
			matches++
		}
	}

	if matches == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSkillNotAttached, skillRef)
	}
	if matches > 1 {
		return nil, fmt.Errorf("%w: %s", ErrSkillAmbiguous, skillRef)
	}

	removed := manifest.Skills[matchedIdx]
	if err := removeAttachmentTarget(projectPath, removed); err != nil {
		return nil, err
	}

	manifest.Skills = append(manifest.Skills[:matchedIdx], manifest.Skills[matchedIdx+1:]...)
	if err := SaveProjectSkillsManifest(projectPath, manifest); err != nil {
		return nil, err
	}

	return &removed, nil
}

// ApplyProjectSkills makes project attachments exactly match desired candidates.
// This is useful for TUI apply flows where users move items between columns.
func ApplyProjectSkills(projectPath string, desired []SkillCandidate) error {
	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return err
	}

	currentByID := make(map[string]ProjectSkillAttachment, len(manifest.Skills))
	managedTargetOwner := make(map[string]string, len(manifest.Skills))
	for _, attachment := range manifest.Skills {
		normalized := normalizeAttachment(attachment)
		id := normalizeSkillToken(skillIDForAttachment(normalized))
		currentByID[id] = normalized
		managedTargetOwner[normalizeSkillToken(normalized.TargetPath)] = id
	}

	desiredByID := make(map[string]SkillCandidate, len(desired))
	desiredTargetByID := make(map[string]string, len(desired))
	desiredTargetOwner := make(map[string]string, len(desired))
	orderedIDs := make([]string, 0, len(desired))
	for _, candidate := range desired {
		id := normalizeSkillToken(buildSkillID(candidate.Source, candidate.Name))
		if _, exists := desiredByID[id]; exists {
			continue
		}

		desiredByID[id] = candidate
		orderedIDs = append(orderedIDs, id)

		targetRel := filepath.ToSlash(filepath.Join(filepath.FromSlash(projectClaudeSkillsDir), candidate.EntryName))
		if current, exists := currentByID[id]; exists && strings.TrimSpace(current.TargetPath) != "" {
			targetRel = current.TargetPath
		}
		targetRel = filepath.ToSlash(strings.TrimSpace(targetRel))
		desiredTargetByID[id] = targetRel

		targetKey := normalizeSkillToken(targetRel)
		if existingOwner, exists := desiredTargetOwner[targetKey]; exists && existingOwner != id {
			return fmt.Errorf("%w: %s and %s both map to %s", ErrSkillTargetConflict, existingOwner, id, targetRel)
		}
		desiredTargetOwner[targetKey] = id
	}

	// Preflight unmanaged target conflicts before removing/recreating anything.
	for _, id := range orderedIDs {
		if _, exists := currentByID[id]; exists {
			continue
		}

		targetRel := desiredTargetByID[id]
		targetKey := normalizeSkillToken(targetRel)
		if ownerID, managed := managedTargetOwner[targetKey]; managed {
			if _, ok := desiredByID[ownerID]; !ok {
				// The current owner will be removed in this apply.
				continue
			}
			// ownerStillDesired with another ID is blocked above by desiredTargetOwner collision.
			continue
		}

		targetPath := resolveTargetPath(projectPath, targetRel)
		if _, err := os.Lstat(targetPath); err == nil {
			return fmt.Errorf("target already exists and is not managed: %s", targetPath)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	for _, attachment := range manifest.Skills {
		id := normalizeSkillToken(skillIDForAttachment(attachment))
		if _, keep := desiredByID[id]; keep {
			continue
		}
		if err := removeAttachmentTarget(projectPath, attachment); err != nil {
			return err
		}
	}

	newManifest := make([]ProjectSkillAttachment, 0, len(desiredByID))
	for _, id := range orderedIDs {
		candidate := desiredByID[id]
		if current, exists := currentByID[id]; exists {
			targetPath := resolveTargetPath(projectPath, current.TargetPath)
			if _, err := os.Lstat(targetPath); err != nil {
				mode, err := materializeSkill(candidate.SourcePath, targetPath)
				if err != nil {
					return err
				}
				current.Mode = mode
				current.AttachedAt = time.Now().Format(time.RFC3339)
			}
			current.SourcePath = candidate.SourcePath
			current.EntryName = candidate.EntryName
			newManifest = append(newManifest, normalizeAttachment(current))
			continue
		}

		attachment := buildAttachment(projectPath, candidate, "")
		targetPath := resolveTargetPath(projectPath, attachment.TargetPath)
		mode, err := materializeSkill(candidate.SourcePath, targetPath)
		if err != nil {
			return err
		}
		attachment.Mode = mode
		newManifest = append(newManifest, normalizeAttachment(attachment))
	}

	manifest.Skills = newManifest
	return SaveProjectSkillsManifest(projectPath, manifest)
}
