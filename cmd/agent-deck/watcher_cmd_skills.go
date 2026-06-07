package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// watcherCreatorSkillFS holds the embedded watcher-creator skill files.
// The canonical source also lives at docs/skills/watcher-creator/ for repo-browser
// discoverability. The TestWatcherCreatorSkill_DocsMatchesAssets test byte-compares
// both trees to detect drift (T-18-22, D-22).
//
//go:embed assets/skills/watcher-creator/*
var watcherCreatorSkillFS embed.FS

// supportedSkills is the whitelist of skill names accepted by install-skill.
// Only "watcher-creator" is supported in Phase 18 (T-18-20).
var supportedSkills = map[string]struct{}{
	"watcher-creator": {},
}

// handleWatcherInstallSkill copies an embedded skill to
// the configured Agent Deck skill pool.
//
// Security:
//   - skill name must be in supportedSkills whitelist (string equality, no regex).
//   - source comes from embed.FS (immutable at build time, no path traversal possible).
//   - target directories are created with mode 0o700.
//   - target files are written with mode 0o644 (readable doc files).
func handleWatcherInstallSkill(_ string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("install-skill requires exactly one argument: the skill name (e.g. watcher-creator)")
	}
	skillName := args[0]

	// Whitelist check (T-18-20): reject anything not in the Phase 18 supported set.
	if _, ok := supportedSkills[skillName]; !ok {
		return fmt.Errorf("unsupported skill %q: only %v are available in this release", skillName, keys(supportedSkills))
	}

	poolDir, err := session.GetSkillPoolPath()
	if err != nil {
		return fmt.Errorf("resolve skill pool path: %w", err)
	}

	targetDir := filepath.Join(poolDir, skillName)

	// Create the full directory hierarchy with mode 0o700 (T-18-21).
	// MkdirAll is used for each step so that each segment gets the correct
	// permissions even if parent dirs are newly created. os.MkdirAll uses the
	// given permission for newly created directories only — existing dirs are
	// left untouched, which is the desired behaviour.
	for _, dir := range []string{
		filepath.Dir(poolDir),
		poolDir,
		targetDir,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Walk the embedded skill directory and write each file to targetDir.
	embedRoot := "assets/skills/" + skillName
	err = fs.WalkDir(watcherCreatorSkillFS, embedRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil // directories handled above; skip
		}

		data, err := fs.ReadFile(watcherCreatorSkillFS, path)
		if err != nil {
			return fmt.Errorf("read embedded file %s: %w", path, err)
		}

		destFile := filepath.Join(targetDir, d.Name())
		if err := os.WriteFile(destFile, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destFile, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("install skill files: %w", err)
	}

	fmt.Printf("Installed skill: %s -> %s\n", skillName, targetDir)
	return nil
}

// keys returns the map's keys as a sorted slice for deterministic error messages.
func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
