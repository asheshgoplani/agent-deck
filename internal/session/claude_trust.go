package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// PreAcceptClaudeTrust adds a `projects[parentDir].hasTrustDialogAccepted = true`
// entry to the Claude config at claudeJSONPath, preserving all existing
// top-level fields and project entries.
//
// Why this exists (#1149): multi-repo worktree parent dirs live at synthetic
// paths under ~/.agent-deck/multi-repo-worktrees/ with no .claude/ or .git
// markers, so Claude Code prompts "do you trust this directory?" on every
// launch. The trust state is keyed by the literal parentDir string in
// ~/.claude.json — pre-seeding the entry skips the prompt the same way that
// accepting it in the UI would.
//
// If claudeJSONPath does not exist, it is created with the new entry. If it
// exists but is malformed, an error is returned without touching the file.
func PreAcceptClaudeTrust(claudeJSONPath, parentDir string) error {
	if claudeJSONPath == "" {
		return fmt.Errorf("claudeJSONPath is empty")
	}
	if parentDir == "" {
		return fmt.Errorf("parentDir is empty")
	}

	cfg := map[string]any{}
	if data, err := os.ReadFile(claudeJSONPath); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("parse %s: %w", claudeJSONPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", claudeJSONPath, err)
	}

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[parentDir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[parentDir] = entry
	cfg["projects"] = projects

	if err := os.MkdirAll(filepath.Dir(claudeJSONPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", claudeJSONPath, err)
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	if err := atomicfile.WriteFile(claudeJSONPath, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", claudeJSONPath, err)
	}
	return nil
}

// WriteMultiRepoParentClaudeMD writes a .claude/CLAUDE.md to parentDir telling
// Claude that this is a multi-repo session, which subdirectories contain real
// repos, how to scope commands, and @path imports for each child project's
// CLAUDE.md (front-loading full project context at session start).
//
// Why (#1149): without this hint Claude treats parentDir as a single project
// and runs build/test commands at the parent — where there is no makefile,
// no package.json, no .git — so every project-specific command fails until
// the user manually cds in. The generated file is plain markdown that Claude
// reads on session start.
func WriteMultiRepoParentClaudeMD(parentDir string, repoNames []string) error {
	if parentDir == "" {
		return fmt.Errorf("parentDir is empty")
	}
	if len(repoNames) == 0 {
		return fmt.Errorf("repoNames is empty")
	}

	claudeDir := filepath.Join(parentDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", claudeDir, err)
	}

	var b strings.Builder
	b.WriteString("# Multi-Repo Session\n\n")
	b.WriteString("This directory is an agent-deck multi-repo worktree parent. ")
	b.WriteString("It contains one subdirectory per project repository — there is no source code at this level.\n\n")
	b.WriteString("## Repositories\n\n")
	for _, name := range repoNames {
		fmt.Fprintf(&b, "- `%s/`\n", name)
	}
	b.WriteString("\n## Command scoping\n\n")
	b.WriteString("Every Bash call must be scoped to its target project:\n\n")
	b.WriteString("- Git operations: `git -C <project-dir> <command>`\n")
	b.WriteString("- Non-git commands: `cd <project-dir> && <command>`\n")

	// @path imports for child CLAUDE.md files.
	var imports []string
	for _, name := range repoNames {
		mdPath := findChildClaudeMD(parentDir, name)
		if mdPath != "" {
			imports = append(imports, mdPath)
		}
	}
	if len(imports) > 0 {
		b.WriteString("\n## Project instructions\n\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "@%s\n", imp)
		}
	}

	mdPath := filepath.Join(claudeDir, "CLAUDE.md")
	tmp := mdPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, mdPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", mdPath, err)
	}

	// Remove legacy root-level CLAUDE.md from initial #1155 implementation.
	_ = os.Remove(filepath.Join(parentDir, "CLAUDE.md"))

	return nil
}

// findChildClaudeMD returns the relative path (from parentDir) to the child
// project's CLAUDE.md. Checks .claude/CLAUDE.md first, then root CLAUDE.md.
// Returns "" if neither exists.
func findChildClaudeMD(parentDir, repoName string) string {
	candidates := []string{
		filepath.Join(repoName, ".claude", "CLAUDE.md"),
		filepath.Join(repoName, "CLAUDE.md"),
	}
	for _, rel := range candidates {
		if _, err := os.Stat(filepath.Join(parentDir, rel)); err == nil {
			return rel
		}
	}
	return ""
}

// WriteMultiRepoParentSettings writes a .claude/settings.json to parentDir
// containing the intersection of all child projects' permissions.allow and the
// union of permissions.deny and permissions.ask.
//
// Intersection for allow ensures no command is auto-approved in the parent that
// would be unsafe in any child project. Union for deny/ask ensures safety
// boundaries from any child are always enforced.
func WriteMultiRepoParentSettings(parentDir string, repoNames []string) error {
	if parentDir == "" {
		return fmt.Errorf("parentDir is empty")
	}
	if len(repoNames) == 0 {
		return fmt.Errorf("repoNames is empty")
	}

	claudeDir := filepath.Join(parentDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", claudeDir, err)
	}

	type permissions struct {
		Allow []string `json:"allow,omitempty"`
		Deny  []string `json:"deny,omitempty"`
		Ask   []string `json:"ask,omitempty"`
	}
	type settingsFile struct {
		Permissions *permissions `json:"permissions,omitempty"`
	}

	var allAllow []map[string]bool
	denySet := map[string]bool{}
	askSet := map[string]bool{}

	for _, name := range repoNames {
		childSettings := findChildSettings(parentDir, name)
		if childSettings == "" {
			// If any child has no settings, intersection of allow is empty.
			allAllow = append(allAllow, map[string]bool{})
			continue
		}

		data, err := os.ReadFile(childSettings)
		if err != nil {
			allAllow = append(allAllow, map[string]bool{})
			continue
		}

		var sf settingsFile
		if err := json.Unmarshal(data, &sf); err != nil || sf.Permissions == nil {
			allAllow = append(allAllow, map[string]bool{})
			continue
		}

		allowSet := map[string]bool{}
		for _, a := range sf.Permissions.Allow {
			allowSet[a] = true
		}
		allAllow = append(allAllow, allowSet)

		for _, d := range sf.Permissions.Deny {
			denySet[d] = true
		}
		for _, a := range sf.Permissions.Ask {
			askSet[a] = true
		}
	}

	// Intersection of allow: only entries present in ALL child projects.
	var allowIntersection []string
	if len(allAllow) > 0 {
		for entry := range allAllow[0] {
			inAll := true
			for i := 1; i < len(allAllow); i++ {
				if !allAllow[i][entry] {
					inAll = false
					break
				}
			}
			if inAll {
				allowIntersection = append(allowIntersection, entry)
			}
		}
	}
	sort.Strings(allowIntersection)

	var denyList []string
	for d := range denySet {
		denyList = append(denyList, d)
	}
	sort.Strings(denyList)

	var askList []string
	for a := range askSet {
		askList = append(askList, a)
	}
	sort.Strings(askList)

	// Only write if there's something to configure.
	if len(allowIntersection) == 0 && len(denyList) == 0 && len(askList) == 0 {
		return nil
	}

	sf := settingsFile{
		Permissions: &permissions{
			Allow: allowIntersection,
			Deny:  denyList,
			Ask:   askList,
		},
	}

	out, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", settingsPath, err)
	}
	return nil
}

// findChildSettings returns the absolute path to a child project's
// .claude/settings.json, or "" if it doesn't exist.
func findChildSettings(parentDir, repoName string) string {
	p := filepath.Join(parentDir, repoName, ".claude", "settings.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ApplyMultiRepoClaudeContext is the single integration point called from
// home.go after creating a multi-repo parent dir. It pre-accepts the trust
// dialog, writes a parent .claude/CLAUDE.md describing the layout (with @path
// imports for child project instructions), and generates a .claude/settings.json
// with the intersection of allowed permissions across all child projects.
//
// Only acts when tool == "claude" AND multiRepoEnabled is true. For any other
// combination it is a no-op, leaving claudeJSONPath untouched.
//
// repoNames is the list of subdirectory names inside parentDir (one per
// repo). The caller is responsible for deriving them from the multi-repo
// worktree result (see DeduplicateDirnames + CreateMultiRepoWorktrees).
func ApplyMultiRepoClaudeContext(tool string, multiRepoEnabled bool, claudeJSONPath, parentDir string, repoNames []string) error {
	if tool != "claude" || !multiRepoEnabled {
		return nil
	}
	if err := PreAcceptClaudeTrust(claudeJSONPath, parentDir); err != nil {
		return fmt.Errorf("pre-accept trust: %w", err)
	}
	// Sort for stable output across runs (map iteration in callers).
	sorted := append([]string(nil), repoNames...)
	sort.Strings(sorted)
	if err := WriteMultiRepoParentClaudeMD(parentDir, sorted); err != nil {
		return fmt.Errorf("write parent CLAUDE.md: %w", err)
	}
	if err := WriteMultiRepoParentSettings(parentDir, sorted); err != nil {
		return fmt.Errorf("write parent settings.json: %w", err)
	}
	return nil
}
