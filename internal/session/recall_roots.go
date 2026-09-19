package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
)

// Recall containment (docs/recall.md). The transcript index reads every
// Claude config dir agent-deck can launch a session under, plus the
// worker-scratch root whose per-session homes symlink `projects` back into
// one of them. The same set is what ValidateTranscriptPath accepts, so the
// two never disagree about what is a transcript; recall adds the profile
// name that owns each root (the profile column) and the profile's
// retention setting.

// recallDefaultRetentionDays is Claude Code's cleanupPeriodDays default.
const recallDefaultRetentionDays = 30

// RecallClaudeRoots returns every Claude root the index should walk, each
// labelled with the agent-deck profile (account) that maps to it, or the
// config dir's base name when none does. Roots are deduplicated on their
// resolved path; the worker-scratch generations are one extra root whose
// symlinked `projects` the reader collapses.
func RecallClaudeRoots() []reader.Root {
	cfg, _ := LoadUserConfig()
	seen := map[string]bool{}
	var roots []reader.Root
	add := func(profile, dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(ExpandPath(dir))
		if !filepath.IsAbs(dir) {
			return
		}
		key := resolveCanonical(dir)
		if seen[key] {
			return
		}
		seen[key] = true
		if profile == "" {
			profile = filepath.Base(dir)
		}
		roots = append(roots, reader.Root{Harness: reader.HarnessClaude, Profile: profile, Dir: dir, RetentionDays: claudeRetentionDays(dir)})
	}
	if cfg != nil {
		for _, name := range ConfiguredAccountNames(cfg) {
			add(name, cfg.GetProfileClaudeConfigDir(name))
		}
		if cfg.Claude.ConfigDir != "" {
			add("", cfg.Claude.ConfigDir)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add("", filepath.Join(home, ".claude"))
	}
	add("", os.Getenv("CLAUDE_CONFIG_DIR"))
	if cfg != nil {
		for _, c := range cfg.Conductors {
			add("", c.Claude.ConfigDir)
		}
		for _, g := range cfg.Groups {
			add("", g.Claude.ConfigDir)
		}
	}
	return append(roots, workerScratchRoots()...)
}

// workerScratchRoots lists the worker-scratch homes: each generation is a
// root of its own so a scratch dir with a real (non-symlinked) projects
// tree is still seen. They carry no profile and no retention.
func workerScratchRoots() []reader.Root {
	scratch := workerScratchDirRoot()
	if scratch == "" {
		return nil
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var roots []reader.Root
	for _, n := range names {
		dir := filepath.Join(scratch, n)
		if _, err := os.Lstat(filepath.Join(dir, "projects")); err != nil {
			continue
		}
		roots = append(roots, reader.Root{Harness: reader.HarnessClaude, Profile: "", Dir: dir})
	}
	return roots
}

// claudeRetentionDays reads cleanupPeriodDays from <dir>/settings.json;
// the Claude Code default applies when the key is absent.
func claudeRetentionDays(dir string) int {
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return recallDefaultRetentionDays
	}
	var s struct {
		CleanupPeriodDays *int `json:"cleanupPeriodDays"`
	}
	if json.Unmarshal(data, &s) != nil || s.CleanupPeriodDays == nil || *s.CleanupPeriodDays < 0 {
		return recallDefaultRetentionDays
	}
	return *s.CleanupPeriodDays
}

// ValidateRecallTranscriptPath is the recall containment check: the path
// must sit under one of the recall roots (lexically and after symlink
// resolution), never under a spoofed sibling and never outside every root
// through a link. It is the same fail-closed shape as ValidateTranscriptPath
// and shares its root set, so a path recall indexes is one the hook
// handlers would accept too.
func ValidateRecallTranscriptPath(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") || !filepath.IsAbs(cleanPath) {
		return "", false
	}
	var roots []string
	for _, r := range RecallClaudeRoots() {
		roots = append(roots, r.Dir)
	}
	if len(roots) == 0 || !containedUnderAny(cleanPath, roots) {
		return "", false
	}
	realRoots := make([]string, 0, len(roots))
	for _, r := range roots {
		realRoots = append(realRoots, resolveCanonical(r))
	}
	if !containedUnderAny(resolveProbeTarget(cleanPath), realRoots) {
		return "", false
	}
	return cleanPath, true
}
