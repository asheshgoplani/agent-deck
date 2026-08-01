package session

import (
	"bytes"
	"crypto/rand"
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

// loadManifest reads project attachment state through the pinned project
// descriptor. The manifest lives at the constant path .agent-deck/skills.toml,
// but a repo can still ship .agent-deck as a symlink; requireNoSymlinkAncestors
// refuses that instead of reading (or later writing) through it.
func (p *projectRoot) loadManifest() (*ProjectSkillsManifest, error) {
	dir, err := p.openPinnedDir(projectSkillsDirName, false)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
		}
		return nil, err
	}
	defer dir.Close()

	data, err := dir.ReadFile(projectSkillsManifest)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
		}
		return nil, fmt.Errorf("failed to read skills manifest: %w", err)
	}

	var manifest ProjectSkillsManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
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

// saveManifest writes project attachment state atomically through the pinned
// project descriptor (MkdirAll + WriteFile + Rename all Root-relative), so a
// shipped ".agent-deck -> /outside" cannot redirect the write.
func (p *projectRoot) saveManifest(manifest *ProjectSkillsManifest) error {
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

	dir, err := p.openPinnedDir(projectSkillsDirName, true)
	if err != nil {
		return fmt.Errorf("failed to open manifest directory: %w", err)
	}
	defer dir.Close()

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("failed to encode skills manifest: %w", err)
	}

	// Randomized name + O_CREATE|O_EXCL: a predictable, truncating temp path
	// can be pre-created as a hard link to a victim file, which the save would
	// then truncate. O_EXCL refuses to reuse any existing entry.
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("failed to write skills manifest: %w", err)
	}
	tmpName := fmt.Sprintf("%s.%x.tmp", projectSkillsManifest, nonce)
	tmpFile, err := dir.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to write skills manifest: %w", err)
	}
	if _, err := tmpFile.Write(buf.Bytes()); err != nil {
		_ = tmpFile.Close()
		_ = dir.Remove(tmpName)
		return fmt.Errorf("failed to write skills manifest: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = dir.Remove(tmpName)
		return fmt.Errorf("failed to write skills manifest: %w", err)
	}
	if err := dir.Rename(tmpName, projectSkillsManifest); err != nil {
		_ = dir.Remove(tmpName)
		return fmt.Errorf("failed to save skills manifest: %w", err)
	}
	return nil
}

// LoadProjectSkillsManifest reads project attachment state.
func LoadProjectSkillsManifest(projectPath string) (*ProjectSkillsManifest, error) {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectSkillsManifest{Skills: []ProjectSkillAttachment{}}, nil
		}
		return nil, err
	}
	defer p.Close()
	return p.loadManifest()
}

// SaveProjectSkillsManifest writes project attachment state atomically.
func SaveProjectSkillsManifest(projectPath string, manifest *ProjectSkillsManifest) error {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		return err
	}
	defer p.Close()
	return p.saveManifest(manifest)
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

// projectRoot is a single live descriptor pinned on the project directory.
// Every containment check AND every mutation in the attach / detach / apply /
// materialize / manifest paths runs through this one Root, so validation and
// mutation observe the same pinned directory and no already-validated path is
// ever reopened by name. That is the structural fix for the class of findings
// that kept moving between call sites: separating "validate a string" from
// "operate on a path" leaves a window (and a fresh traversal) every time.
type projectRoot struct {
	path string
	root *os.Root
	info os.FileInfo // the pinned project dir itself, for device identity
}

func openProjectRoot(projectPath string) (*projectRoot, error) {
	root, err := os.OpenRoot(projectPath)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &projectRoot{path: filepath.Clean(projectPath), root: root, info: info}, nil
}

// openPinnedChildDir opens ONE component below parent as a pinned child root.
//
// This is what makes validation and mutation inseparable. Checking a name and
// then operating on that name again leaves a window in which the entry can be
// swapped for a symlink that still resolves INSIDE the project (".claude/skills
// -> ..", which os.Root permits because it never leaves the root) — the check
// passes and the mutation lands on the project root. Here the entry is
// inspected with Lstat, opened, and the OPENED DESCRIPTOR is then verified to
// be the very inode that was inspected (os.SameFile). A swap in between
// changes the identity and is refused; everything afterwards runs against the
// descriptor, which no rename can redirect.
//
// The component must be a real directory (never a symlink) on the same
// filesystem as the project, so a mount planted at a managed dir cannot
// redirect removal or materialization onto foreign content either.
func (p *projectRoot) openPinnedChildDir(parent *os.Root, name string, create bool) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		if !os.IsNotExist(err) || !create {
			return nil, err
		}
		if err := parent.Mkdir(name, 0o755); err != nil && !os.IsExist(err) {
			return nil, err
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return nil, err
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to use symlinked directory component: %s", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("refusing to use non-directory component: %s", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	pinned, err := child.Stat(".")
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	if !os.SameFile(info, pinned) {
		_ = child.Close()
		return nil, fmt.Errorf("refusing to use directory component that changed identity during validation: %s", name)
	}
	if !sameFilesystem(p.info, pinned) {
		_ = child.Close()
		return nil, fmt.Errorf("refusing to use directory component on a different filesystem: %s", name)
	}
	return child, nil
}

// openPinnedDir walks rel component by component, pinning each directory by
// descriptor identity. The returned root is the deepest directory; the caller
// operates on entries inside it by NAME ONLY, never by reassembled path.
func (p *projectRoot) openPinnedDir(rel string, create bool) (*os.Root, error) {
	current := p.root
	owned := false
	for _, component := range strings.Split(filepath.Clean(rel), string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		next, err := p.openPinnedChildDir(current, component, create)
		if err != nil {
			if owned {
				_ = current.Close()
			}
			return nil, err
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	if !owned {
		return nil, fmt.Errorf("refusing to pin the project root itself")
	}
	return current, nil
}

// openTargetParent validates a managed target and returns the PINNED directory
// that contains it plus the final entry name. Every managed-target operation
// (exists checks, RemoveAll, Symlink, copy) runs through this pair, so nothing
// downstream ever re-resolves a path.
func (p *projectRoot) openTargetParent(targetRel string, create bool) (*os.Root, string, error) {
	rel, err := p.validateManagedTarget(targetRel)
	if err != nil {
		return nil, "", err
	}
	dir, name := filepath.Split(rel)
	dir = filepath.Clean(dir)
	if name == "" {
		return nil, "", fmt.Errorf("refusing to operate on the managed project skills dir itself: %s", p.abs(rel))
	}
	parent, err := p.openPinnedDir(dir, create)
	if err != nil {
		return nil, "", err
	}
	return parent, name, nil
}

func (p *projectRoot) Close() {
	_ = p.root.Close()
}

// abs renders a Root-relative path for error messages and for computing
// symlink link text. It is never fed back to a path-based filesystem API.
func (p *projectRoot) abs(rel string) string {
	return filepath.Join(p.path, rel)
}

// projectRel converts an absolute or project-relative path into a path
// relative to the pinned project root, refusing anything that escapes it or
// designates the project root itself.
func (p *projectRoot) projectRel(path string) (string, error) {
	abs := resolveTargetPath(p.path, path)
	rel, err := filepath.Rel(p.path, abs)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to use path outside the project: %s", abs)
	}
	return rel, nil
}

// requireNoSymlinkAncestors walks rel component by component through the
// pinned project descriptor and refuses any EXISTING component that is a
// symlink — whether it points outside the project, back INSIDE it (a repo
// shipping ".claude/skills -> .." would otherwise expose the project root,
// .git included), or DANGLES (a dangling link ENOENTs under EvalSymlinks and
// an existence-based check would read that as "component absent"). Missing
// components are fine: the creation path materializes them as real
// directories. When allowFinalSymlink is set, the LAST component may be a
// symlink — pool-attached skills are symlinks inside the managed dir, and
// removal deletes the link, never its destination.
func (p *projectRoot) requireNoSymlinkAncestors(rel string, allowFinalSymlink bool) error {
	components := strings.Split(rel, string(os.PathSeparator))
	relSoFar := ""
	for i, component := range components {
		relSoFar = filepath.Join(relSoFar, component)
		info, err := p.root.Lstat(relSoFar)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("refusing to use unresolvable path %s: %w", p.abs(rel), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if allowFinalSymlink && i == len(components)-1 {
				return nil
			}
			return fmt.Errorf("refusing to use path with a symlinked ancestor component: %s", p.abs(relSoFar))
		}
	}
	return nil
}

// validateManagedTarget is the single containment gate for every managed-skill
// path. It returns the target's PROJECT-RELATIVE path, which is what all
// mutations use against the pinned descriptor — callers never receive an
// absolute path to re-traverse.
//
// The target must name a managed project-skills dir, must be a STRICT
// descendant of it (a tampered ".claude/skills/." cleans to the managed dir
// itself, and RemoveAll there would wipe the whole catalog), must not escape
// the project, and must have no symlinked ancestor component.
func (p *projectRoot) validateManagedTarget(targetRel string) (string, error) {
	targetPath := resolveTargetPath(p.path, targetRel)
	skillDir, ok := managedProjectSkillsDirForTarget(targetRel)
	if !ok {
		return "", fmt.Errorf("refusing to use path outside managed project skills dirs: %s", targetPath)
	}
	base := filepath.Clean(filepath.Join(p.path, filepath.FromSlash(skillDir)))
	if !isContainedIn(base, targetPath) {
		return "", fmt.Errorf("refusing to use path outside project skills dir: %s", targetPath)
	}
	if targetPath == base {
		return "", fmt.Errorf("refusing to operate on the managed project skills dir itself: %s", targetPath)
	}
	rel, err := p.projectRel(targetPath)
	if err != nil {
		return "", err
	}
	if err := p.requireNoSymlinkAncestors(rel, true); err != nil {
		return "", err
	}
	return rel, nil
}

// targetExists reports whether a validated managed target currently resolves
// to something, with the follow-the-link semantics the attach flow relies on:
// a broken link inside the project is "absent" (so reattach rematerializes),
// while a pool symlink whose destination legitimately lives outside the
// project is "present" (Root refuses to traverse the escape, which is itself
// proof the entry exists as an escaping link).
func (p *projectRoot) targetExists(targetRel string) (bool, error) {
	parent, name, err := p.openTargetParent(targetRel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer parent.Close()

	if _, err := parent.Stat(name); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	}

	// Root.Stat reports the escape before it reports whether the destination
	// exists, so an out-of-root link lands here whether it is a live pool
	// attachment or a dangling leftover. Resolve the link explicitly and let
	// the DESTINATION decide: a link that resolves (pool attachment, or a
	// foreign replacement the loadout layer must report rather than silently
	// overwrite) counts as present; a dangling link counts as ABSENT so the
	// attach flow rematerializes over it. This is a read-only metadata check
	// on the link's own destination — the same thing the previous os.Stat on
	// the target did — while every mutation stays descriptor-relative.
	info, lerr := parent.Lstat(name)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return false, nil
		}
		return false, lerr
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return true, nil
	}
	dest, rerr := parent.Readlink(name)
	if rerr != nil {
		return true, nil
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(p.physicalDirOf(targetRel), dest)
	}
	if _, err := os.Stat(filepath.Clean(dest)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return true, nil
	}
	return true, nil
}

// physicalDirOf returns the canonical, symlink-resolved directory that holds
// targetRel. Relative link text must be joined against the PHYSICAL directory
// (that is what the kernel does, and what materialize used when it computed
// the link text); joining against a lexical alias would resolve pool links to
// the wrong path on alias-accessed projects.
func (p *projectRoot) physicalDirOf(targetRel string) string {
	dir := filepath.Dir(resolveTargetPath(p.path, targetRel))
	if physical, err := filepath.EvalSymlinks(dir); err == nil {
		return physical
	}
	return dir
}

// targetEntryExists reports whether the entry itself exists (Lstat semantics:
// a symlink counts even when it dangles).
func (p *projectRoot) targetEntryExists(targetRel string) (bool, error) {
	parent, name, err := p.openTargetParent(targetRel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer parent.Close()

	if _, err := parent.Lstat(name); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

// openContainedSource validates a materialization SOURCE and returns it as an
// os.Root plus a root-relative path, so every source read is descriptor-
// relative too. A source is accepted only when it lives strictly inside a
// registered skill source root (sources.toml: the managed pool, claude-global,
// operator-registered dirs) or strictly inside one of the project's own
// managed skills dirs — the migration fallback that copies from the current,
// already containment-checked target.
//
// A FINAL-component symlink is followed one hop at a time and the destination
// is re-validated as a source in its own right. That is what makes migration
// from an existing pool-attached symlink work when the recorded source path is
// gone: the managed entry is a link into the pool, so reading it as a source
// means validating the POOL destination, not refusing the escape.
func (p *projectRoot) openContainedSource(sourcePath string) (*os.Root, string, string, error) {
	resolved, err := resolveSkillSourcePath(sourcePath)
	if err != nil {
		return nil, "", "", err
	}

	for hop := 0; hop < 8; hop++ {
		root, rel, abs, err := p.openSourceRootFor(resolved)
		if err != nil {
			return nil, "", "", err
		}
		info, lerr := root.Lstat(rel)
		if lerr != nil {
			root.Close()
			return nil, "", "", lerr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return root, rel, abs, nil
		}
		dest, rerr := root.Readlink(rel)
		root.Close()
		if rerr != nil {
			return nil, "", "", rerr
		}
		if !filepath.IsAbs(dest) {
			baseDir := filepath.Dir(abs)
			if physical, perr := filepath.EvalSymlinks(baseDir); perr == nil {
				baseDir = physical
			}
			dest = filepath.Join(baseDir, dest)
		}
		resolved = filepath.Clean(dest)
	}
	return nil, "", "", fmt.Errorf("refusing to materialize from source with too many symlink levels: %s", resolved)
}

// openSourceRootFor picks the root a resolved source belongs to. Project-local
// candidates are validated through the pinned project descriptor BEFORE being
// opened — a shipped ".agents/skills -> /outside" is refused rather than
// anchored — and are then opened RELATIVE to the project root using the
// constant managed-dir component, never by reassembled absolute path.
func (p *projectRoot) openSourceRootFor(resolved string) (*os.Root, string, string, error) {
	sources, err := LoadSkillSources()
	if err != nil {
		return nil, "", "", err
	}
	for _, def := range sources {
		rootPath := expandSkillPath(def.Path)
		if rootPath == "" || !filepath.IsAbs(rootPath) {
			continue
		}
		rootPath = filepath.Clean(rootPath)
		if resolved == rootPath || !isContainedIn(rootPath, resolved) {
			continue
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			return nil, "", "", err
		}
		rel, err := filepath.Rel(rootPath, resolved)
		if err != nil {
			root.Close()
			return nil, "", "", err
		}
		return root, rel, resolved, nil
	}

	for _, dir := range knownProjectSkillsDirs() {
		base := filepath.Clean(filepath.Join(p.path, filepath.FromSlash(dir)))
		if resolved == base || !isContainedIn(base, resolved) {
			continue
		}
		rel, err := p.projectRel(resolved)
		if err != nil {
			return nil, "", "", err
		}
		if err := p.requireNoSymlinkAncestors(rel, true); err != nil {
			return nil, "", "", err
		}
		// Pinned by descriptor identity, same as the target side: a source dir
		// swapped for a symlink after validation cannot be opened.
		root, err := p.openPinnedDir(filepath.FromSlash(dir), false)
		if err != nil {
			return nil, "", "", err
		}
		innerRel, err := filepath.Rel(base, resolved)
		if err != nil {
			root.Close()
			return nil, "", "", err
		}
		return root, innerRel, resolved, nil
	}

	return nil, "", "", fmt.Errorf("refusing to materialize from source outside registered skill sources: %s", resolved)
}

// copyFileIntoRoot copies srcRel (read descriptor-relative inside srcRoot, a
// registered skill source root) to dstRel inside dstRoot, so neither side of
// the copy can be redirected outside its root by a hostile path swap. A
// symlinked source file is followed only within srcRoot; escapes error.
func copyFileIntoRoot(dstRoot, srcRoot *os.Root, srcRel, dstRel string) error {
	srcFile, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	if dir := filepath.Dir(dstRel); dir != "." {
		if err := dstRoot.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	dstFile, err := dstRoot.OpenFile(dstRel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}

// copyDirIntoRoot mirrors copyDir but reads and writes descriptor-relative.
// Symlinked SOURCE entries are followed and copied as content like before,
// with one deliberate tightening: a source symlink may only resolve within
// the registered source root — an escape errors instead of being followed.
func copyDirIntoRoot(dstRoot, srcRoot *os.Root, srcRel, dstRel string) error {
	srcDir, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	entries, err := srcDir.ReadDir(-1)
	srcDir.Close()
	if err != nil {
		return err
	}
	if err := dstRoot.MkdirAll(dstRel, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		entrySrcRel := filepath.Join(srcRel, entry.Name())
		entryDstRel := filepath.Join(dstRel, entry.Name())

		// Stat (not Lstat): follows in-root symlinked source entries so their
		// CONTENT is copied, matching the previous EvalSymlinks behavior.
		info, err := srcRoot.Stat(entrySrcRel)
		if err != nil {
			return err
		}

		if info.IsDir() {
			if err := copyDirIntoRoot(dstRoot, srcRoot, entrySrcRel, entryDstRel); err != nil {
				return err
			}
		} else {
			if err := copyFileIntoRoot(dstRoot, srcRoot, entrySrcRel, entryDstRel); err != nil {
				return err
			}
		}
	}
	return nil
}

func copySkillIntoRoot(dstRoot, srcRoot *os.Root, srcRel, dstRel string) (string, error) {
	info, err := srcRoot.Stat(srcRel)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		if err := copyDirIntoRoot(dstRoot, srcRoot, srcRel, dstRel); err != nil {
			return "", err
		}
	} else {
		if err := copyFileIntoRoot(dstRoot, srcRoot, srcRel, dstRel); err != nil {
			return "", err
		}
	}
	return "copy", nil
}

// materialize places sourcePath at the validated managed target, entirely
// through the pinned project descriptor: MkdirAll, RemoveAll, Symlink and the
// copy fallback are all Root-relative, so a component swapped in after
// validation cannot redirect any of them. When copyOnly is set the symlink
// mode is skipped (the migration case, where the source IS the current
// target's content).
func (p *projectRoot) materialize(sourcePath, targetRel string, copyOnly bool) (string, error) {
	srcRoot, srcRel, srcAbs, err := p.openContainedSource(sourcePath)
	if err != nil {
		return "", err
	}
	defer srcRoot.Close()

	parent, name, err := p.openTargetParent(targetRel, true)
	if err != nil {
		return "", err
	}
	defer parent.Close()

	if err := parent.RemoveAll(name); err != nil {
		return "", err
	}

	if !copyOnly {
		// Link text is computed from the validated Root-relative target and the
		// source's own root; the link is CREATED descriptor-relative. The link
		// content may point at the pool outside the project — creating such a
		// link is fine, os.Root only forbids traversing through escapes.
		//
		// Correctness of the link is proven without re-Stat'ing the target by
		// path: the source exists (its root Stat'ed it below) and the link text
		// is verified to rejoin exactly to the source.
		if _, serr := srcRoot.Stat(srcRel); serr == nil {
			linkBase := p.physicalDirOf(targetRel)
			sourceTarget := srcAbs
			if resolvedSource, rerr := filepath.EvalSymlinks(srcAbs); rerr == nil {
				sourceTarget = resolvedSource
			}
			if linkText, rerr := filepath.Rel(linkBase, sourceTarget); rerr == nil &&
				filepath.Clean(filepath.Join(linkBase, linkText)) == filepath.Clean(sourceTarget) {
				if err := parent.Symlink(linkText, name); err == nil {
					return "symlink", nil
				}
			}
		}
	}

	return copySkillIntoRoot(parent, srcRoot, srcRel, name)
}

// removeManagedTarget validates and removes in one descriptor-relative step:
// the containment walk and the RemoveAll run against the SAME pinned project
// root, so there is no revalidated-by-name window between them. A non-managed,
// absolute, or "../"-escaping target is REFUSED and never removed (Audit M3).
func (p *projectRoot) removeManagedTarget(targetRel string) error {
	parent, name, err := p.openTargetParent(targetRel, false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("refusing to remove path outside managed project skills dirs: %w", err)
	}
	defer parent.Close()
	return parent.RemoveAll(name)
}

// materializeSkill / materializeSkillCopyOnly / safeRemoveManagedTarget are the
// path-based entry points kept for callers (and tests) that only have a project
// path. They open the project root once and delegate; nothing else in this file
// reopens a root per operation.
func materializeSkill(projectPath, sourcePath, targetRel string) (string, error) {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		return "", err
	}
	defer p.Close()
	return p.materialize(sourcePath, targetRel, false)
}

func materializeSkillCopyOnly(projectPath, sourcePath, targetRel string) (string, error) {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		return "", err
	}
	defer p.Close()
	return p.materialize(sourcePath, targetRel, true)
}

func safeRemoveManagedTarget(projectPath, targetRel string) error {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer p.Close()
	return p.removeManagedTarget(targetRel)
}

func resolveTargetPath(projectPath, targetPath string) string {
	if filepath.IsAbs(targetPath) {
		return filepath.Clean(targetPath)
	}
	return filepath.Clean(filepath.Join(projectPath, filepath.FromSlash(targetPath)))
}

// resolveContainedTargetPath is the read-only façade over
// projectRoot.validateManagedTarget for callers that only need to know whether
// a manifest- or candidate-derived target is acceptable. It returns the
// absolute path for comparison and messages ONLY — every mutation goes through
// the pinned descriptor via projectRoot, never through this path.
func resolveContainedTargetPath(projectPath, targetRel string) (string, error) {
	p, err := openProjectRoot(projectPath)
	if err != nil {
		return "", fmt.Errorf("refusing to use unresolvable project path %s: %w", projectPath, err)
	}
	defer p.Close()
	rel, err := p.validateManagedTarget(targetRel)
	if err != nil {
		return "", err
	}
	return p.abs(rel), nil
}

func removeAttachmentTarget(p *projectRoot, attachment ProjectSkillAttachment) error {
	return p.removeManagedTarget(attachment.TargetPath)
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

// resolveMaterializationSource picks the source to materialize from: the
// recorded source path when it still exists, otherwise the CURRENT managed
// target (fallbackRel, project-relative), which is checked through the pinned
// descriptor rather than by path. usedFallback tells the caller it is copying
// the existing target's own content rather than a fresh source.
func (p *projectRoot) resolveMaterializationSource(sourcePath, fallbackRel string) (source string, usedFallback bool, err error) {
	if strings.TrimSpace(sourcePath) != "" {
		if _, err := os.Stat(sourcePath); err == nil {
			return sourcePath, false, nil
		} else if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	if strings.TrimSpace(fallbackRel) != "" {
		exists, err := p.targetExists(fallbackRel)
		if err != nil {
			return "", false, err
		}
		if exists {
			return p.abs(fallbackRel), true, nil
		}
	}
	return "", false, os.ErrNotExist
}

func attachSkillCandidate(projectPath, tool string, candidate SkillCandidate) (*ProjectSkillAttachment, error) {
	if !SupportsProjectSkills(tool) {
		return nil, fmt.Errorf("project skills are not supported for %s sessions", tool)
	}
	if candidate.Kind != "dir" {
		return nil, fmt.Errorf("%w: %s", ErrSkillUnsupportedKind, candidate.Name)
	}

	p, err := openProjectRoot(projectPath)
	if err != nil {
		return nil, err
	}
	defer p.Close()

	manifest, err := p.loadManifest()
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
		desiredTargetRelPath, err := p.validateManagedTarget(desiredTargetRel)
		if err != nil {
			return nil, err
		}
		currentTargetRelPath, err := p.validateManagedTarget(existing.TargetPath)
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
		if currentTargetRelPath != desiredTargetRelPath {
			if exists, err := p.targetEntryExists(desiredTargetRelPath); err != nil {
				return nil, err
			} else if exists {
				return nil, fmt.Errorf("target already exists and is not managed: %s", p.abs(desiredTargetRelPath))
			}

			sourceToUse, usedCurrentTarget, err := p.resolveMaterializationSource(candidate.SourcePath, currentTargetRelPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("cannot migrate attached skill %s: source and current target are unavailable", existing.Name)
				}
				return nil, err
			}
			mode := ""
			if usedCurrentTarget {
				mode, err = p.materialize(sourceToUse, desiredTargetRel, true)
			} else {
				if err := validateAttachableSkillCandidate(candidate); err != nil {
					return nil, err
				}
				mode, err = p.materialize(sourceToUse, desiredTargetRel, false)
			}
			if err != nil {
				return nil, err
			}
			// Audit M3: guard the manifest-derived current-target removal so a
			// tampered TargetPath can't delete outside the project skills dir.
			if err := p.removeManagedTarget(existing.TargetPath); err != nil {
				_ = p.removeManagedTarget(desiredTargetRel)
				return nil, err
			}
			existing.TargetPath = desiredTargetRel
			existing.Mode = mode
			existing.AttachedAt = time.Now().Format(time.RFC3339)
		} else {
			if exists, err := p.targetExists(currentTargetRelPath); err != nil {
				return nil, err
			} else if exists {
				return nil, fmt.Errorf("%w: %s", ErrSkillAlreadyAttached, candidate.Name)
			}
			sourceToUse, _, err := p.resolveMaterializationSource(candidate.SourcePath, "")
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("cannot rematerialize attached skill %s: source path is unavailable", existing.Name)
				}
				return nil, err
			}
			if err := validateAttachableSkillCandidate(candidate); err != nil {
				return nil, err
			}
			mode, err := p.materialize(sourceToUse, existing.TargetPath, false)
			if err != nil {
				return nil, err
			}
			existing.Mode = mode
			existing.AttachedAt = time.Now().Format(time.RFC3339)
		}

		existing.SourcePath = candidate.SourcePath
		existing.EntryName = candidate.EntryName
		manifest.Skills[i] = normalizeAttachment(existing)
		if err := p.saveManifest(manifest); err != nil {
			return nil, err
		}
		updated := manifest.Skills[i]
		return &updated, nil
	}

	if err := validateAttachableSkillCandidate(candidate); err != nil {
		return nil, err
	}

	attachment := buildAttachment(tool, candidate, "")
	targetRelPath, err := p.validateManagedTarget(attachment.TargetPath)
	if err != nil {
		return nil, err
	}

	for _, existing := range manifest.Skills {
		if normalizeSkillToken(existing.TargetPath) == normalizeSkillToken(attachment.TargetPath) {
			return nil, fmt.Errorf("target already managed by %s", existing.Name)
		}
	}

	if exists, err := p.targetEntryExists(targetRelPath); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("target already exists and is not managed: %s", p.abs(targetRelPath))
	}

	mode, err := p.materialize(candidate.SourcePath, attachment.TargetPath, false)
	if err != nil {
		return nil, err
	}
	attachment.Mode = mode
	attachment = normalizeAttachment(attachment)

	manifest.Skills = append(manifest.Skills, attachment)
	if err := p.saveManifest(manifest); err != nil {
		_ = removeAttachmentTarget(p, attachment)
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
	p, err := openProjectRoot(projectPath)
	if err != nil {
		return nil, err
	}
	defer p.Close()

	manifest, err := p.loadManifest()
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
	if err := removeAttachmentTarget(p, removed); err != nil {
		return nil, err
	}

	manifest.Skills = append(manifest.Skills[:matchedIdx], manifest.Skills[matchedIdx+1:]...)
	if err := p.saveManifest(manifest); err != nil {
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

	p, err := openProjectRoot(projectPath)
	if err != nil {
		return err
	}
	defer p.Close()

	manifest, err := p.loadManifest()
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
		targetRelPath, err := p.validateManagedTarget(targetRel)
		if err != nil {
			return err
		}

		currentTargetRelPath := ""
		if current, exists := currentByID[id]; exists {
			currentTargetRelPath, err = p.validateManagedTarget(current.TargetPath)
			if err != nil {
				return err
			}
		}

		if existingOwner, exists := managedTargetOwner[targetKey]; exists && existingOwner != id {
			if _, keep := desiredByID[existingOwner]; keep {
				return fmt.Errorf("%w: %s and %s both map to %s", ErrSkillTargetConflict, existingOwner, id, targetRel)
			}
		}

		exists, err := p.targetEntryExists(targetRelPath)
		if err != nil {
			return err
		}
		if exists {
			if currentTargetRelPath == targetRelPath {
				continue
			}
			if existingOwner, owned := managedTargetOwner[targetKey]; owned && existingOwner != id {
				if _, keep := desiredByID[existingOwner]; !keep {
					continue
				}
			}
			return fmt.Errorf("target already exists and is not managed: %s", p.abs(targetRelPath))
		}
	}

	for _, attachment := range manifest.Skills {
		id := normalizeSkillToken(skillIDForAttachment(attachment))
		if _, keep := desiredByID[id]; keep {
			continue
		}
		if err := removeAttachmentTarget(p, attachment); err != nil {
			return err
		}
	}

	newManifest := make([]ProjectSkillAttachment, 0, len(desiredByID))
	for _, id := range orderedIDs {
		candidate := desiredByID[id]
		desiredTargetRel := desiredTargetByID[id]
		if current, exists := currentByID[id]; exists {
			currentTargetRelPath, err := p.validateManagedTarget(current.TargetPath)
			if err != nil {
				return err
			}
			desiredTargetRelPath, err := p.validateManagedTarget(desiredTargetRel)
			if err != nil {
				return err
			}
			if currentTargetRelPath != desiredTargetRelPath {
				sourceToUse, usedCurrentTarget, err := p.resolveMaterializationSource(candidate.SourcePath, currentTargetRelPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("cannot migrate attached skill %s: source and current target are unavailable", current.Name)
					}
					return err
				} else {
					mode := ""
					if usedCurrentTarget {
						mode, err = p.materialize(sourceToUse, desiredTargetRel, true)
					} else {
						if err := validateAttachableSkillCandidate(candidate); err != nil {
							return err
						}
						mode, err = p.materialize(sourceToUse, desiredTargetRel, false)
					}
					if err != nil {
						return err
					}
					// Audit M3: guard the manifest-derived current-target removal.
					if err := p.removeManagedTarget(current.TargetPath); err != nil {
						_ = p.removeManagedTarget(desiredTargetRel)
						return err
					}
					current.TargetPath = desiredTargetRel
					current.Mode = mode
					current.AttachedAt = time.Now().Format(time.RFC3339)
				}
			} else if exists, err := p.targetExists(desiredTargetRelPath); err != nil {
				return err
			} else if !exists {
				sourceToUse, _, err := p.resolveMaterializationSource(candidate.SourcePath, "")
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("cannot rematerialize attached skill %s: source path is unavailable", current.Name)
					}
					return err
				} else {
					if err := validateAttachableSkillCandidate(candidate); err != nil {
						return err
					}
					mode, err := p.materialize(sourceToUse, desiredTargetRel, false)
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
		if _, err := p.validateManagedTarget(attachment.TargetPath); err != nil {
			return err
		}
		sourceToUse, _, err := p.resolveMaterializationSource(candidate.SourcePath, "")
		if err != nil {
			return err
		}
		if err := validateAttachableSkillCandidate(candidate); err != nil {
			return err
		}
		mode, err := p.materialize(sourceToUse, attachment.TargetPath, false)
		if err != nil {
			return err
		}
		attachment.Mode = mode
		newManifest = append(newManifest, normalizeAttachment(attachment))
	}

	manifest.Skills = newManifest
	return p.saveManifest(manifest)
}
