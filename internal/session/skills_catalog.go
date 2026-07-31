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

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

const (
	skillsDirName            = "skills"
	skillSourcesFileName     = "sources.toml"
	projectSkillsDirName     = ".agent-deck"
	projectSkillsManifest    = "skills.toml"
	projectClaudeSkillsDir   = ".claude/skills"
	projectAgentsSkillsDir   = ".agents/skills"
	projectHermesSkillsDir   = ".hermes/skills"
	defaultSkillSourcePool   = "pool"
	defaultSkillSourceClaude = "claude-global"
)

var (
	ErrSkillSourceExists    = errors.New("skill source already exists")
	ErrSkillSourceNotFound  = errors.New("skill source not found")
	ErrSkillNotFound        = errors.New("skill not found")
	ErrSkillAmbiguous       = errors.New("skill reference is ambiguous")
	ErrSkillUnsupportedKind = errors.New("skill is not a Claude-compatible directory skill")
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
//
// The json tags are required for the web API: without them encoding/json
// ignores the toml tags and falls back to the exported field names, emitting
// PascalCase (`Name`) instead of the `name` the frontend + e2e tests read.
// Tag style mirrors the sibling SkillCandidate so both skill types serialize
// consistently across the /api/skills surface.
type ProjectSkillAttachment struct {
	ID         string `toml:"id" json:"id"`
	Name       string `toml:"name" json:"name"`
	Source     string `toml:"source" json:"source"`
	SourcePath string `toml:"source_path" json:"source_path"`
	EntryName  string `toml:"entry_name" json:"entry_name"`
	TargetPath string `toml:"target_path" json:"target_path"` // relative to project path
	Mode       string `toml:"mode,omitempty" json:"mode,omitempty"`
	AttachedAt string `toml:"attached_at,omitempty" json:"attached_at,omitempty"`
}

// ProjectSkillsManifest is the project-local attachment state.
type ProjectSkillsManifest struct {
	Skills []ProjectSkillAttachment `toml:"skills"`
}

// MaterializedProjectSkill is one on-disk skill entry under a managed project root.
type MaterializedProjectSkill struct {
	EntryName  string `json:"entry_name"`
	TargetPath string `json:"target_path"`
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

func knownProjectSkillsDirs() []string {
	return []string{projectClaudeSkillsDir, projectAgentsSkillsDir, projectHermesSkillsDir}
}

// SupportsProjectSkills reports whether the runtime supports project skill materialization.
func SupportsProjectSkills(tool string) bool {
	_, ok := GetProjectSkillsDir(tool)
	return ok
}

// ShouldRestartProjectSkills reports whether agent-deck should auto-restart the session
// after project skill changes for this runtime.
func ShouldRestartProjectSkills(tool string) bool {
	return IsClaudeCompatible(tool) || tool == "gemini" || tool == "codex" || tool == "hermes"
}

// GetProjectSkillsDir returns the runtime-managed project skill directory.
func GetProjectSkillsDir(tool string) (string, bool) {
	switch {
	case IsClaudeCompatible(tool):
		return projectClaudeSkillsDir, true
	case tool == "gemini" || tool == "codex" || tool == "pi":
		return projectAgentsSkillsDir, true
	case tool == "hermes":
		return projectHermesSkillsDir, true
	default:
		return "", false
	}
}

// GetProjectSkillsPath returns the runtime-specific project skills path.
func GetProjectSkillsPath(projectPath, tool string) string {
	dir, ok := GetProjectSkillsDir(tool)
	if !ok {
		return ""
	}
	return filepath.Join(projectPath, filepath.FromSlash(dir))
}

func buildProjectSkillTargetPath(skillDir, entryName string) string {
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(skillDir), entryName))
}

func targetPathUsesSkillDir(targetPath, skillDir string) bool {
	normalizedTarget := filepath.ToSlash(strings.TrimSpace(targetPath))
	normalizedDir := filepath.ToSlash(strings.TrimSpace(skillDir))
	return normalizedTarget == normalizedDir || strings.HasPrefix(normalizedTarget, normalizedDir+"/")
}

func managedProjectSkillsDirForTarget(targetPath string) (string, bool) {
	for _, dir := range knownProjectSkillsDirs() {
		if targetPathUsesSkillDir(targetPath, dir) {
			return dir, true
		}
	}
	return "", false
}

func expandSkillPath(path string) string {
	if path == "" {
		return ""
	}
	// Expand $HOME and ${HOME} anywhere in the path so sources.toml is portable
	// across machines with different home-directory layouts (issue #617). Only
	// HOME is recognised; other env references pass through verbatim so config
	// paths do not silently inherit arbitrary process environment.
	if strings.Contains(path, "$") {
		if home, err := os.UserHomeDir(); err == nil {
			path = os.Expand(path, func(name string) string {
				if name == "HOME" {
					return home
				}
				return "$" + name
			})
		}
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
	// filepath.Rel-based containment instead of a string-prefix compare: with
	// base "/" the old base+PathSeparator prefix became "//", which no cleaned
	// path carries, so every legitimate target under a root-based project was
	// rejected (attach/detach/apply all failed). Rel handles the root base and
	// any trailing-separator quirks uniformly; "." means equal-to-base, which
	// was previously accepted and stays accepted.
	rel, err := filepath.Rel(filepath.Clean(basePath), filepath.Clean(targetPath))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// GetSkillsRootPath returns the Agent Deck skills config directory, falling back
// to the legacy ~/.agent-deck/skills tree when it already exists.
func GetSkillsRootPath() (string, error) {
	configDir, err := agentpaths.ConfigDir()
	if err != nil {
		return "", err
	}
	xdgPath := filepath.Join(configDir, skillsDirName)
	if _, err := os.Stat(xdgPath); err == nil {
		return xdgPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	legacyDir, err := agentpaths.LegacyDir()
	if err != nil {
		return xdgPath, nil
	}
	legacyPath := filepath.Join(legacyDir, skillsDirName)
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return xdgPath, nil
}

// GetSkillSourcesPath returns the skill source registry path.
func GetSkillSourcesPath() (string, error) {
	root, err := GetSkillsRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, skillSourcesFileName), nil
}

// GetSkillPoolPath returns the managed skill pool path.
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
			// A directory is a candidate when it is a directory skill
			// (SKILL.md at the root) OR a Claude Code plugin
			// (.claude-plugin/plugin.json). Plugin dirs are valid project
			// skills: materialized into <project>/.claude/skills/ they are
			// discovered by Claude Code's project-scope plugin loading
			// (commands/hooks/MCP included) exactly like a hand-placed dir.
			skillMDPath := filepath.Join(entryPath, "SKILL.md")
			name, desc := "", ""
			if _, err := os.Stat(skillMDPath); err == nil {
				name, desc = parseSkillMetadata(skillMDPath, entry.Name())
			} else if !hasPluginManifest(entryPath) {
				continue
			}
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

// ListMaterializedProjectSkills returns all entries currently present in known managed project roots.
func ListMaterializedProjectSkills(projectPath string) ([]MaterializedProjectSkill, error) {
	materialized := make([]MaterializedProjectSkill, 0)
	for _, skillDir := range knownProjectSkillsDirs() {
		entries, err := os.ReadDir(filepath.Join(projectPath, filepath.FromSlash(skillDir)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			materialized = append(materialized, MaterializedProjectSkill{
				EntryName:  e.Name(),
				TargetPath: buildProjectSkillTargetPath(skillDir, e.Name()),
			})
		}
	}
	sort.Slice(materialized, func(i, j int) bool {
		return materialized[i].TargetPath < materialized[j].TargetPath
	})
	return materialized, nil
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

// resolveContainedSourcePath validates a materialization SOURCE path the way
// targets are validated (CodeQL go/path-injection, alert 237): the destination
// side is Root-confined, but the source still arrives from manifest- or
// candidate-derived data and is opened by path. A source is accepted only when
// it sits strictly inside a registered skill source root (sources.toml: the
// managed pool, claude-global, and operator-registered dirs) or strictly
// inside one of the project's own managed skills dirs (the migration fallback
// that copies from the current, already containment-checked target). Anything
// else — absolute paths into arbitrary trees, "../" escapes — is refused
// before any filesystem read.
func resolveContainedSourcePath(projectPath, sourcePath string) (string, error) {
	resolved, err := resolveSkillSourcePath(sourcePath)
	if err != nil {
		return "", err
	}

	sources, err := LoadSkillSources()
	if err != nil {
		return "", err
	}
	for _, def := range sources {
		rootPath := expandSkillPath(def.Path)
		if rootPath == "" || !filepath.IsAbs(rootPath) {
			continue
		}
		if resolved != filepath.Clean(rootPath) && isContainedIn(rootPath, resolved) {
			return resolved, nil
		}
	}

	for _, dir := range knownProjectSkillsDirs() {
		base := filepath.Join(projectPath, filepath.FromSlash(dir))
		if resolved != filepath.Clean(base) && isContainedIn(base, resolved) {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("refusing to materialize from source outside registered skill sources: %s", resolved)
}

// copyFileIntoRoot copies src (read with plain os APIs — src has passed
// resolveContainedSourcePath at the materialize entry points) to dstRel inside
// root, creating the file descriptor-relative so a hostile path swap under
// the managed dir cannot redirect the write outside it.
func copyFileIntoRoot(root *os.Root, src, dstRel string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	if dir := filepath.Dir(dstRel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	dstFile, err := root.OpenFile(dstRel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}

// copyDirIntoRoot mirrors copyDir but writes descriptor-relative inside root.
// Symlinked SOURCE entries are resolved and copied as content, same as before.
func copyDirIntoRoot(root *os.Root, src, dstRel string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(dstRel, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		entryRel := filepath.Join(dstRel, entry.Name())

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
			if err := copyDirIntoRoot(root, srcPath, entryRel); err != nil {
				return err
			}
		} else {
			if err := copyFileIntoRoot(root, srcPath, entryRel); err != nil {
				return err
			}
		}
	}
	return nil
}

func copySkillIntoRoot(root *os.Root, sourcePath, targetRel string) (string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		if err := copyDirIntoRoot(root, sourcePath, targetRel); err != nil {
			return "", err
		}
	} else {
		if err := copyFileIntoRoot(root, sourcePath, targetRel); err != nil {
			return "", err
		}
	}
	return "copy", nil
}

// openManagedTargetRoot validates targetRel via managedTargetBaseAndRel,
// ensures the managed skills dir exists, and opens it as an os.Root so all
// mutations happen descriptor-relative (no path re-traversal, no TOCTOU
// escape). Returns the root, the target's root-relative path, and the
// validated absolute target path (for read-only checks and messages).
func openManagedTargetRoot(projectPath, targetRel string) (*os.Root, string, string, error) {
	base, rel, err := managedTargetBaseAndRel(projectPath, targetRel)
	if err != nil {
		return nil, "", "", err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, "", "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", "", err
	}
	return root, rel, filepath.Join(base, rel), nil
}

func materializeSkillCopyOnly(projectPath, sourcePath, targetRel string) (string, error) {
	sourcePath, err := resolveContainedSourcePath(projectPath, sourcePath)
	if err != nil {
		return "", err
	}
	root, rel, _, err := openManagedTargetRoot(projectPath, targetRel)
	if err != nil {
		return "", err
	}
	defer root.Close()

	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := root.RemoveAll(rel); err != nil {
		return "", err
	}
	return copySkillIntoRoot(root, sourcePath, rel)
}

func materializeSkill(projectPath, sourcePath, targetRel string) (string, error) {
	sourcePath, err := resolveContainedSourcePath(projectPath, sourcePath)
	if err != nil {
		return "", err
	}
	root, rel, targetPath, err := openManagedTargetRoot(projectPath, targetRel)
	if err != nil {
		return "", err
	}
	defer root.Close()

	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := root.RemoveAll(rel); err != nil {
		return "", err
	}

	// Resolve symlinks before computing relative paths.
	// This avoids broken relative links when target lives under a symlinked path
	// (for example macOS /tmp -> /private/tmp).
	resolvedSourcePath := sourcePath
	if resolved, err := filepath.EvalSymlinks(sourcePath); err == nil {
		resolvedSourcePath = resolved
	}

	relBase := filepath.Dir(targetPath)
	if resolvedBase, err := filepath.EvalSymlinks(relBase); err == nil {
		relBase = resolvedBase
	}

	relTarget, relErr := filepath.Rel(relBase, resolvedSourcePath)
	if relErr == nil {
		// Root.Symlink creates the link descriptor-relative; the link CONTENT
		// may point at the pool outside the root — creating it is fine, Root
		// only forbids traversing through escapes.
		if err := root.Symlink(relTarget, rel); err == nil {
			// Validate that the symlink resolves to a real target. Use plain
			// os.Stat on the absolute path: Root.Stat refuses symlinks that
			// resolve outside the root, and legitimate pool links do exactly
			// that. If the link dangles, fall back to copy mode below.
			if _, err := os.Stat(targetPath); err == nil {
				return "symlink", nil
			}
			_ = root.Remove(rel)
		}
	}

	return copySkillIntoRoot(root, sourcePath, rel)
}

func resolveTargetPath(projectPath, targetPath string) string {
	if filepath.IsAbs(targetPath) {
		return filepath.Clean(targetPath)
	}
	return filepath.Clean(filepath.Join(projectPath, filepath.FromSlash(targetPath)))
}

// resolveContainedTargetPath resolves targetRel against projectPath and REFUSES
// any result that is not contained in a managed project-skills dir — the same
// containment guard #1200 added for worktree deletion (Audit M3), applied here
// to every filesystem sink (Stat/Lstat/symlink/copy) that consumes a
// manifest- or candidate-derived path, not just os.RemoveAll. A non-managed,
// absolute, or "../"-escaping target returns an error instead of a path, so
// CodeQL's go/path-injection sinks in attachSkillCandidate and
// ApplyProjectSkills only ever see a path proven to sit inside one of
// knownProjectSkillsDirs(). Callers must check the error and must not fall
// back to the raw, unchecked resolveTargetPath for the same operation.
//
// Containment is symlink-aware, not just string-based: the project root is
// resolved via EvalSymlinks (its OWN ancestry may legitimately contain
// symlinks, e.g. macOS /tmp -> /private/tmp), then every component from the
// project root down to the target is Lstat-walked. Any EXISTING non-final
// component that is itself a symlink is refused outright — whether it points
// outside the project, back INSIDE it (a repo shipping .claude/skills -> ..
// would otherwise expose the project root, .git included, to RemoveAll), or
// DANGLES (an EvalSymlinks-based check reads a dangling link's ENOENT as
// "component absent" and passes it lexically). The managed skills dir is
// therefore required to sit at its canonical physical location with no
// symlinked components. Missing components are fine: the creation path
// materializes them as real directories. The FINAL target component may be a
// symlink — pool-attached skills are symlinks inside the managed dir, and
// removal deletes the link, never its destination; containment is about
// where the target path lives, not what a final-component link points to.
//
// The target must also be a STRICT descendant of the managed dir: a tampered
// ".claude/skills/." cleans to the managed dir itself, and RemoveAll there
// would wipe the whole catalog.
func resolveContainedTargetPath(projectPath, targetRel string) (string, error) {
	targetPath := resolveTargetPath(projectPath, targetRel)
	skillDir, ok := managedProjectSkillsDirForTarget(targetRel)
	if !ok {
		return "", fmt.Errorf("refusing to use path outside managed project skills dirs: %s", targetPath)
	}
	base := filepath.Join(projectPath, filepath.FromSlash(skillDir))
	if !isContainedIn(base, targetPath) {
		return "", fmt.Errorf("refusing to use path outside project skills dir: %s", targetPath)
	}
	if targetPath == filepath.Clean(base) {
		return "", fmt.Errorf("refusing to operate on the managed project skills dir itself: %s", targetPath)
	}

	resolvedProject, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return "", fmt.Errorf("refusing to use unresolvable project path %s: %w", projectPath, err)
	}
	relFromProject, err := filepath.Rel(filepath.Clean(projectPath), targetPath)
	if err != nil || relFromProject == "." || relFromProject == ".." ||
		strings.HasPrefix(relFromProject, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to use path outside the project: %s", targetPath)
	}
	current := resolvedProject
	components := strings.Split(relFromProject, string(os.PathSeparator))
	for i, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				// This component (and everything below it) does not exist yet;
				// the creation path will materialize real directories here.
				break
			}
			return "", fmt.Errorf("refusing to use unresolvable target path %s: %w", targetPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 && i < len(components)-1 {
			return "", fmt.Errorf("refusing to use target path with a symlinked ancestor component: %s", current)
		}
	}
	return targetPath, nil
}

// managedTargetBaseAndRel returns the canonical managed skills dir for
// targetRel plus the target's path relative to it, after full containment
// validation. It is the shared setup for the descriptor-relative (os.Root)
// mutation sinks below.
func managedTargetBaseAndRel(projectPath, targetRel string) (string, string, error) {
	targetPath, err := resolveContainedTargetPath(projectPath, targetRel)
	if err != nil {
		return "", "", err
	}
	skillDir, _ := managedProjectSkillsDirForTarget(targetRel)
	base := filepath.Join(projectPath, filepath.FromSlash(skillDir))
	rel, err := filepath.Rel(filepath.Clean(base), targetPath)
	if err != nil {
		return "", "", err
	}
	return filepath.Clean(base), rel, nil
}

func removeAttachmentTarget(projectPath string, attachment ProjectSkillAttachment) error {
	return safeRemoveManagedTarget(projectPath, attachment.TargetPath)
}

// safeRemoveManagedTarget removes targetRel (resolved against projectPath) only
// when it is contained in a managed project-skills dir. A non-managed,
// absolute, or "../"-escaping target is REFUSED and never removed. Every
// removal that operates on a manifest-derived TargetPath (attach, detach + the
// migration branches in attachSkillCandidate / reconcileProjectSkills) must
// route through here so a tampered manifest can't trigger deletion outside the
// project skills dir. Audit M3.
//
// The removal itself is descriptor-relative: the managed skills dir is opened
// with os.Root and the target removed via Root.RemoveAll, so even a path swap
// between validation and mutation (TOCTOU) cannot escape the managed dir —
// the kernel enforces no-traversal semantics on every operation inside Root.
func safeRemoveManagedTarget(projectPath, targetRel string) error {
	base, rel, err := managedTargetBaseAndRel(projectPath, targetRel)
	if err != nil {
		return fmt.Errorf("refusing to remove path outside managed project skills dirs: %w", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer root.Close()
	return root.RemoveAll(rel)
}

func buildAttachment(tool string, candidate SkillCandidate, mode string) ProjectSkillAttachment {
	skillDir, _ := GetProjectSkillsDir(tool)
	targetRel := buildProjectSkillTargetPath(skillDir, candidate.EntryName)
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

func validateAttachableSkillCandidate(candidate SkillCandidate) error {
	// Project-managed skills must be directory skills carrying SKILL.md or
	// a Claude Code plugin manifest (.claude-plugin/plugin.json) — the same
	// markers candidate discovery accepts.
	if candidate.Kind != "dir" {
		return fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
	}

	info, err := os.Stat(candidate.SourcePath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
	}

	skillMD := filepath.Join(candidate.SourcePath, "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		if os.IsNotExist(err) {
			if hasPluginManifest(candidate.SourcePath) {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
		}
		return err
	}

	return nil
}

// hasPluginManifest reports whether dir is a Claude Code plugin root
// (.claude-plugin/plugin.json).
func hasPluginManifest(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	return err == nil && !info.IsDir()
}

func resolveMaterializationSource(sourcePath, fallbackPath string) (string, error) {
	if strings.TrimSpace(sourcePath) != "" {
		if _, err := os.Stat(sourcePath); err == nil {
			return sourcePath, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if strings.TrimSpace(fallbackPath) != "" {
		if _, err := os.Stat(fallbackPath); err == nil {
			return fallbackPath, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

func attachSkillCandidate(projectPath, tool string, candidate SkillCandidate) (*ProjectSkillAttachment, error) {
	if !SupportsProjectSkills(tool) {
		return nil, fmt.Errorf("project skills are not supported for %s sessions", tool)
	}
	if candidate.Kind != "dir" {
		return nil, fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
	}

	manifest, err := LoadProjectSkillsManifest(projectPath)
	if err != nil {
		return nil, err
	}

	candidateID := buildSkillID(candidate.Source, candidate.Name)
	expectedDir, _ := GetProjectSkillsDir(tool)
	expectedTargetRel := buildProjectSkillTargetPath(expectedDir, candidate.EntryName)
	for i := range manifest.Skills {
		existing := manifest.Skills[i]
		if normalizeSkillToken(skillIDForAttachment(existing)) != normalizeSkillToken(candidateID) {
			continue
		}

		desiredTargetRel := existing.TargetPath
		if !targetPathUsesSkillDir(existing.TargetPath, expectedDir) {
			desiredTargetRel = expectedTargetRel
		}
		desiredTargetPath, err := resolveContainedTargetPath(projectPath, desiredTargetRel)
		if err != nil {
			return nil, err
		}
		currentTargetPath, err := resolveContainedTargetPath(projectPath, existing.TargetPath)
		if err != nil {
			return nil, err
		}

		for _, other := range manifest.Skills {
			if normalizeSkillToken(skillIDForAttachment(other)) == normalizeSkillToken(candidateID) {
				continue
			}
			if normalizeSkillToken(other.TargetPath) == normalizeSkillToken(desiredTargetRel) {
				return nil, fmt.Errorf("target already managed by %s", other.Name)
			}
		}
		if currentTargetPath != desiredTargetPath {
			if _, err := os.Lstat(desiredTargetPath); err == nil {
				return nil, fmt.Errorf("target already exists and is not managed: %s", desiredTargetPath)
			} else if !os.IsNotExist(err) {
				return nil, err
			}

			sourceToUse, err := resolveMaterializationSource(candidate.SourcePath, currentTargetPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("cannot migrate attached skill %s: source and current target are unavailable", existing.Name)
				}
				return nil, err
			}
			mode := ""
			if sourceToUse == currentTargetPath {
				mode, err = materializeSkillCopyOnly(projectPath, sourceToUse, desiredTargetRel)
			} else {
				if err := validateAttachableSkillCandidate(candidate); err != nil {
					return nil, err
				}
				mode, err = materializeSkill(projectPath, sourceToUse, desiredTargetRel)
			}
			if err != nil {
				return nil, err
			}
			// Audit M3: guard the manifest-derived currentTargetPath removal so a
			// tampered TargetPath can't delete outside the project skills dir.
			if err := safeRemoveManagedTarget(projectPath, existing.TargetPath); err != nil {
				_ = safeRemoveManagedTarget(projectPath, desiredTargetRel)
				return nil, err
			}
			existing.TargetPath = desiredTargetRel
			existing.Mode = mode
			existing.AttachedAt = time.Now().Format(time.RFC3339)
		} else {
			if _, err := os.Stat(currentTargetPath); err == nil {
				return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyAttached, candidate.Name)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			sourceToUse, err := resolveMaterializationSource(candidate.SourcePath, "")
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("cannot rematerialize attached skill %s: source path is unavailable", existing.Name)
				}
				return nil, err
			}
			if err := validateAttachableSkillCandidate(candidate); err != nil {
				return nil, err
			}
			mode, err := materializeSkill(projectPath, sourceToUse, existing.TargetPath)
			if err != nil {
				return nil, err
			}
			existing.Mode = mode
			existing.AttachedAt = time.Now().Format(time.RFC3339)
		}

		existing.SourcePath = candidate.SourcePath
		existing.EntryName = candidate.EntryName
		manifest.Skills[i] = normalizeAttachment(existing)
		if err := SaveProjectSkillsManifest(projectPath, manifest); err != nil {
			return nil, err
		}
		updated := manifest.Skills[i]
		return &updated, nil
	}

	if err := validateAttachableSkillCandidate(candidate); err != nil {
		return nil, err
	}

	attachment := buildAttachment(tool, candidate, "")
	targetPath, err := resolveContainedTargetPath(projectPath, attachment.TargetPath)
	if err != nil {
		return nil, err
	}

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

	mode, err := materializeSkill(projectPath, candidate.SourcePath, attachment.TargetPath)
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

// AttachSkillToProject resolves and attaches one skill into the runtime-specific project skills dir.
func AttachSkillToProject(projectPath, tool, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	candidate, err := ResolveSkillCandidate(skillRef, sourceName)
	if err != nil {
		return nil, err
	}
	return attachSkillCandidate(projectPath, tool, *candidate)
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
func ApplyProjectSkills(projectPath, tool string, desired []SkillCandidate) error {
	if !SupportsProjectSkills(tool) {
		return fmt.Errorf("project skills are not supported for %s sessions", tool)
	}

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

	expectedDir, _ := GetProjectSkillsDir(tool)
	desiredByID := make(map[string]SkillCandidate, len(desired))
	desiredTargetByID := make(map[string]string, len(desired))
	desiredTargetOwner := make(map[string]string, len(desired))
	orderedIDs := make([]string, 0, len(desired))
	for _, candidate := range desired {
		if candidate.Kind != "dir" {
			return fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
		}
		id := normalizeSkillToken(buildSkillID(candidate.Source, candidate.Name))
		if _, exists := desiredByID[id]; exists {
			continue
		}

		desiredByID[id] = candidate
		orderedIDs = append(orderedIDs, id)

		targetRel := buildProjectSkillTargetPath(expectedDir, candidate.EntryName)
		if current, exists := currentByID[id]; exists && targetPathUsesSkillDir(current.TargetPath, expectedDir) {
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

	for _, id := range orderedIDs {
		targetRel := desiredTargetByID[id]
		targetKey := normalizeSkillToken(targetRel)
		targetPath, err := resolveContainedTargetPath(projectPath, targetRel)
		if err != nil {
			return err
		}

		currentTargetPath := ""
		if current, exists := currentByID[id]; exists {
			currentTargetPath, err = resolveContainedTargetPath(projectPath, current.TargetPath)
			if err != nil {
				return err
			}
		}

		if existingOwner, exists := managedTargetOwner[targetKey]; exists && existingOwner != id {
			if _, keep := desiredByID[existingOwner]; keep {
				return fmt.Errorf("%w: %s and %s both map to %s", ErrSkillTargetConflict, existingOwner, id, targetRel)
			}
		}

		if _, err := os.Lstat(targetPath); err == nil {
			if currentTargetPath == targetPath {
				continue
			}
			if existingOwner, exists := managedTargetOwner[targetKey]; exists && existingOwner != id {
				if _, keep := desiredByID[existingOwner]; !keep {
					continue
				}
			}
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
		desiredTargetRel := desiredTargetByID[id]
		if current, exists := currentByID[id]; exists {
			currentTargetPath, err := resolveContainedTargetPath(projectPath, current.TargetPath)
			if err != nil {
				return err
			}
			desiredTargetPath, err := resolveContainedTargetPath(projectPath, desiredTargetRel)
			if err != nil {
				return err
			}
			if currentTargetPath != desiredTargetPath {
				sourceToUse, err := resolveMaterializationSource(candidate.SourcePath, currentTargetPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("cannot migrate attached skill %s: source and current target are unavailable", current.Name)
					}
					return err
				} else {
					mode := ""
					if sourceToUse == currentTargetPath {
						mode, err = materializeSkillCopyOnly(projectPath, sourceToUse, desiredTargetRel)
					} else {
						if err := validateAttachableSkillCandidate(candidate); err != nil {
							return err
						}
						mode, err = materializeSkill(projectPath, sourceToUse, desiredTargetRel)
					}
					if err != nil {
						return err
					}
					// Audit M3: guard the manifest-derived currentTargetPath removal.
					if err := safeRemoveManagedTarget(projectPath, current.TargetPath); err != nil {
						_ = safeRemoveManagedTarget(projectPath, desiredTargetRel)
						return err
					}
					current.TargetPath = desiredTargetRel
					current.Mode = mode
					current.AttachedAt = time.Now().Format(time.RFC3339)
				}
			} else if _, err := os.Stat(desiredTargetPath); err != nil {
				if !os.IsNotExist(err) {
					return err
				}
				sourceToUse, err := resolveMaterializationSource(candidate.SourcePath, "")
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("cannot rematerialize attached skill %s: source path is unavailable", current.Name)
					}
					return err
				} else {
					if err := validateAttachableSkillCandidate(candidate); err != nil {
						return err
					}
					mode, err := materializeSkill(projectPath, sourceToUse, desiredTargetRel)
					if err != nil {
						return err
					}
					current.Mode = mode
					current.AttachedAt = time.Now().Format(time.RFC3339)
				}
			}
			current.SourcePath = candidate.SourcePath
			current.EntryName = candidate.EntryName
			newManifest = append(newManifest, normalizeAttachment(current))
			continue
		}

		attachment := buildAttachment(tool, candidate, "")
		if _, err := resolveContainedTargetPath(projectPath, attachment.TargetPath); err != nil {
			return err
		}
		sourceToUse, err := resolveMaterializationSource(candidate.SourcePath, "")
		if err != nil {
			return err
		}
		if err := validateAttachableSkillCandidate(candidate); err != nil {
			return err
		}
		mode, err := materializeSkill(projectPath, sourceToUse, attachment.TargetPath)
		if err != nil {
			return err
		}
		attachment.Mode = mode
		newManifest = append(newManifest, normalizeAttachment(attachment))
	}

	manifest.Skills = newManifest
	return SaveProjectSkillsManifest(projectPath, manifest)
}
