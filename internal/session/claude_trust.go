package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	tmp := claudeJSONPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, claudeJSONPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", claudeJSONPath, err)
	}
	return nil
}

// WriteMultiRepoParentClaudeMD writes a CLAUDE.md to parentDir telling Claude
// that this is a multi-repo session and which subdirectories contain real
// repos. The first entry in repoNames becomes the default working
// subdirectory recommendation.
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
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", parentDir, err)
	}

	var b strings.Builder
	b.WriteString("# Multi-Repo Session\n\n")
	b.WriteString("This directory is an agent-deck multi-repo worktree parent. ")
	b.WriteString("It contains one subdirectory per project repository — there is no source code at this level.\n\n")
	b.WriteString("## Repositories\n\n")
	for _, name := range repoNames {
		fmt.Fprintf(&b, "- `%s/`\n", name)
	}
	b.WriteString("\n## Working directory\n\n")
	fmt.Fprintf(&b, "Default to `%s/` for project-specific commands (build, test, run). ", repoNames[0])
	b.WriteString("`cd` into the appropriate repository subdirectory before invoking `make`, `npm`, `cargo`, etc. — ")
	b.WriteString("each repo has its own toolchain configuration.\n")

	mdPath := filepath.Join(parentDir, "CLAUDE.md")
	tmp := mdPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, mdPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", mdPath, err)
	}
	return nil
}

// ApplyMultiRepoClaudeContext is the single integration point called from
// home.go after creating a multi-repo parent dir. It pre-accepts the trust
// dialog and writes a parent CLAUDE.md describing the layout — but only when
// tool == "claude" AND multiRepoEnabled is true. For any other combination
// it is a no-op, leaving claudeJSONPath untouched.
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
	return nil
}
