package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Auto-memory location, reproduced from Claude Code's own resolver.
//
// The rule was read out of the shipped binary (2.1.220) rather than guessed,
// because guessing it is what produced the defect this file exists to fix: the
// panel priced a 130-byte stub at ~123 tokens and told the user to edit it,
// while the file the session actually loads was 17,506 bytes of index sitting
// under a different project slug.
//
// The harness resolves the directory as, in order:
//
//  1. CLAUDE_COWORK_MEMORY_PATH_OVERRIDE, taken literally;
//  2. the autoMemoryDirectory setting, with a leading ~ expanded;
//  3. <memory config dir>/projects/<slug>/memory, where the memory config dir
//     is CLAUDE_CODE_REMOTE_MEMORY_DIR when set and otherwise the session's
//     Claude config dir, and the slug encodes the session's *canonical git
//     root* — not its working directory.
//
// The index file inside that directory is MEMORY.md.
//
// Point 3 is the one that matters in practice and the one that was wrong. A
// session whose cwd is /Users/x/.agent-deck/conductor/agent-deck loads the
// index at projects/-Users-x--agent-deck/memory/MEMORY.md, because
// /Users/x/.agent-deck is the enclosing git repository: every session anywhere
// in a repository shares one auto-memory index. The cwd-scoped slug that
// agent-deck used to derive is where *older* Claude Code versions kept it, so
// on a long-lived machine both directories exist and only one is loaded.
const (
	// autoMemIndexName is the index file inside the auto-memory directory.
	autoMemIndexName = "MEMORY.md"
	// autoMemDirName is the leaf directory name under the project slug.
	autoMemDirName = "memory"
	// autoMemOverrideEnv is taken literally, without ~ expansion.
	autoMemOverrideEnv = "CLAUDE_COWORK_MEMORY_PATH_OVERRIDE"
	// autoMemRemoteDirEnv replaces the config dir the projects/ tree is read
	// from, without changing the slug.
	autoMemRemoteDirEnv = "CLAUDE_CODE_REMOTE_MEMORY_DIR"
	// claudeSlugMaxUnits is the cap Claude Code applies to a project slug
	// before it appends a hash of the full path.
	claudeSlugMaxUnits = 200
)

// autoMemBasis says how an auto-memory location was arrived at. It exists so a
// row can say whether its figure rests on the harness's rule or on a guess.
type autoMemBasis uint8

const (
	// autoMemFromGitRoot: the slug encodes the project's canonical git root,
	// which is the rule Claude Code applies.
	autoMemFromGitRoot autoMemBasis = iota
	// autoMemFromWorkingDir: the project is in no git repository, so the rule
	// falls back to the working directory itself.
	autoMemFromWorkingDir
	// autoMemFromOverride: an environment variable or a settings key named the
	// directory outright.
	autoMemFromOverride
	// autoMemFromExistingIndex: the location the rule predicts holds no index,
	// and an index that does exist under a neighbouring candidate slug was
	// chosen instead. This is a guess and is labelled as one.
	autoMemFromExistingIndex
	// autoMemAbsent: no candidate exists. The predicted path is still reported
	// so the "nothing found" note can name where it looked.
	autoMemAbsent
)

// AutoMemory is where the session's auto-memory index was resolved to, and how
// sure that is.
//
// The provenance is part of the result rather than something the caller
// reconstructs: a number attached to the wrong file is worse than no number,
// and the only defence is for the resolution to carry its own confidence.
type AutoMemory struct {
	// Path is the MEMORY.md the session is taken to load. Empty when auto-memory
	// is off for this session.
	Path string
	// Dir is the auto-memory directory Path sits in.
	Dir string
	// ProjectRoot is the directory whose slug was used.
	ProjectRoot string
	// Basis says how Path was arrived at.
	Basis autoMemBasis
	// Reason is one sentence naming the rule that produced Path, for the row's
	// lever and for the caveat when Confident is false.
	Reason string
	// Confident reports that Path follows Claude Code's documented resolution
	// and the file is there. False means the row must not present its figure as
	// settled.
	Confident bool
	// Candidates are every MEMORY.md that was considered, in preference order,
	// starting with the one the rule predicts.
	Candidates []string
	// Superseded are candidate indexes that exist on disk but are not the loaded
	// one — typically an index an older Claude Code version wrote under the
	// cwd-scoped slug. They are reported so a user who finds one does not edit
	// a file the harness stopped reading.
	Superseded []string
	// Disabled reports that autoMemoryEnabled is false for this session, so no
	// auto-memory is loaded however many indexes exist on disk.
	Disabled bool
}

// resolveAutoMemory locates the auto-memory index for a session.
//
// configDir must already be the session's *resolved* config dir — the same one
// every other memory layer is read from. Passing anything else is the defect
// this function was written to end.
func resolveAutoMemory(configDir, projectPath string, settings mergedSettings, env func(string) string, stat func(string) (os.FileInfo, error)) AutoMemory {
	out := AutoMemory{}
	if settings.AutoMemEnabled != nil && !*settings.AutoMemEnabled {
		out.Disabled = true
		out.Reason = "autoMemoryEnabled is false in this session's settings, so Claude Code neither reads nor writes an auto-memory index"
		return out
	}

	// 1 and 2: an outright override wins, and its slug is never computed.
	if dir, source := autoMemOverrideDir(settings, env); dir != "" {
		out.Dir = dir
		out.Path = filepath.Join(dir, autoMemIndexName)
		out.Basis = autoMemFromOverride
		out.Reason = "the auto-memory directory is set explicitly by " + source
		out.Candidates = []string{out.Path}
		out.Confident = fileExists(stat, out.Path)
		if !out.Confident {
			out.Basis = autoMemAbsent
		}
		return out
	}

	base := strings.TrimSpace(env(autoMemRemoteDirEnv))
	if base == "" {
		base = configDir
	}
	base = strings.TrimSpace(base)
	project := strings.TrimSpace(projectPath)
	if base == "" || project == "" {
		out.Basis = autoMemAbsent
		out.Reason = "neither a config directory nor a project path was resolved, so the auto-memory index could not be located at all"
		return out
	}
	if abs, err := filepath.Abs(project); err == nil {
		project = abs
	}

	// 3: the rule. The canonical git root, falling back to the working
	// directory when there is no repository.
	root, inRepo := canonicalGitRoot(project, stat)
	out.ProjectRoot = root
	primary := autoMemIndexFor(base, root)
	out.Dir = filepath.Dir(primary)
	out.Path = primary
	if inRepo {
		out.Basis = autoMemFromGitRoot
		out.Reason = "auto-memory is scoped to the git repository root (" + root + "), which every session inside the repository shares"
	} else {
		out.Basis = autoMemFromWorkingDir
		out.Reason = "auto-memory is scoped to the working directory, which is in no git repository"
	}

	// Candidates exist for one purpose: when the predicted index is absent, an
	// index that does exist under a neighbouring slug is a better answer than
	// silence — as long as it is labelled a guess. They are never allowed to
	// outrank the rule.
	out.Candidates = autoMemCandidates(base, root, project)
	if fileExists(stat, primary) {
		out.Confident = true
		out.Superseded = existingOthers(stat, out.Candidates, primary)
		return out
	}

	for _, candidate := range out.Candidates {
		if candidate == primary || !fileHasBytes(stat, candidate) {
			continue
		}
		out.Path = candidate
		out.Dir = filepath.Dir(candidate)
		out.Basis = autoMemFromExistingIndex
		out.Reason = "no index exists where Claude Code's rule puts it (" + primary +
			"), so this is the first index that does exist among the slugs this session could resolve to"
		out.Confident = false
		out.Superseded = existingOthers(stat, out.Candidates, candidate)
		return out
	}

	out.Basis = autoMemAbsent
	out.Confident = false
	return out
}

// autoMemIndexFor builds the index path for one project root.
func autoMemIndexFor(base, root string) string {
	return filepath.Join(base, "projects", claudeProjectSlug(root), autoMemDirName, autoMemIndexName)
}

// autoMemCandidates lists the index paths a session could plausibly resolve to,
// in preference order: the rule's own answer first, then the working directory
// (where older Claude Code versions kept it), then each directory between the
// two, deepest first.
//
// The list is a fallback ladder, not a vote. Its later entries are only ever
// consulted when the first one does not exist.
func autoMemCandidates(base, root, project string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(dir string) {
		path := autoMemIndexFor(base, dir)
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	add(root)
	add(project)
	// Only the directories strictly between the two. When the root *is* the
	// project — a repository root, or no repository at all — there are none, and
	// walking further would invent candidates outside the session's tree.
	for dir := filepath.Dir(project); dir != root && isUnder(dir, root); dir = filepath.Dir(dir) {
		add(dir)
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	return out
}

// isUnder reports whether path is inside ancestor. It compares resolved relative
// paths rather than string prefixes, so /a/bc is not treated as living under
// /a/b.
func isUnder(path, ancestor string) bool {
	rel, err := filepath.Rel(ancestor, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// existingOthers returns the candidates other than chosen that exist on disk.
func existingOthers(stat func(string) (os.FileInfo, error), candidates []string, chosen string) []string {
	var out []string
	for _, c := range candidates {
		if c == chosen || !fileHasBytes(stat, c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// autoMemOverrideDir returns the directory an override names, and what named it.
//
// The environment variable is taken literally; the setting supports a leading ~.
// Both go through Claude Code's own validity check, so an override the harness
// would reject is not honoured here either.
func autoMemOverrideDir(settings mergedSettings, env func(string) string) (dir, source string) {
	if v := strings.TrimSpace(env(autoMemOverrideEnv)); v != "" {
		if normalized := normalizeAutoMemDir(v, false, env); normalized != "" {
			return normalized, autoMemOverrideEnv
		}
	}
	if v := strings.TrimSpace(settings.AutoMemDir); v != "" {
		if normalized := normalizeAutoMemDir(v, true, env); normalized != "" {
			src := "the autoMemoryDirectory setting"
			if settings.AutoMemDirSource != "" {
				src += " in " + settings.AutoMemDirSource
			}
			return normalized, src
		}
	}
	return "", ""
}

// normalizeAutoMemDir mirrors the harness's validation: a usable override is
// absolute, at least three characters, free of NUL, and has its trailing
// separators stripped. Anything else is ignored rather than guessed at.
func normalizeAutoMemDir(path string, expandTilde bool, env func(string) string) string {
	if expandTilde {
		path = expandHome(path, env)
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || len(clean) < 3 || strings.ContainsRune(clean, 0) {
		return ""
	}
	return clean
}

// canonicalGitRoot returns the directory whose slug scopes the session's
// auto-memory, and whether a git repository was found at all.
//
// It reproduces two steps. First the ancestor walk: from the project path up to
// and including the filesystem root, the first directory holding a .git entry
// wins. Then the worktree hop: when that .git is a *file* holding a gitdir:
// pointer — a linked worktree — the main working tree's root is used instead, so
// every worktree of a repository shares the repository's one auto-memory index.
// Any inconsistency in the pointer chain leaves the worktree itself as the root,
// which is what the harness does on the same failure.
func canonicalGitRoot(project string, stat func(string) (os.FileInfo, error)) (root string, inRepo bool) {
	dir := filepath.Clean(project)
	for {
		if fileOrDirExists(stat, filepath.Join(dir, ".git")) {
			return mainWorktreeRoot(dir, stat), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(project), false
		}
		dir = parent
	}
}

// mainWorktreeRoot resolves a linked worktree back to the repository it belongs
// to. A directory .git, or any unreadable or inconsistent pointer, returns dir
// unchanged.
//
// The pointer files are read from the real filesystem rather than through the
// injected stat, so a test that fakes the filesystem sees no worktree hop — the
// same answer it gets for a repository whose .git is a directory.
func mainWorktreeRoot(dir string, stat func(string) (os.FileInfo, error)) string {
	gitPath := filepath.Join(dir, ".git")
	info, err := stat(gitPath)
	if err != nil || info.IsDir() {
		return dir
	}
	raw, err := readBoundedRaw(gitPath, 1<<16)
	if err != nil {
		return dir
	}
	pointer := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(pointer, "gitdir:") {
		return dir
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(pointer, "gitdir:"))
	if gitDir == "" {
		return dir
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	commonRaw, err := readBoundedRaw(filepath.Join(gitDir, "commondir"), 1<<16)
	if err != nil {
		return dir
	}
	common := strings.TrimSpace(string(commonRaw))
	if common == "" {
		return dir
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	common = filepath.Clean(common)
	// The pointer must describe the layout it claims: <common>/worktrees/<name>
	// is where a linked worktree's git dir lives. Without this check a stray
	// gitdir: file would silently relocate a session's whole memory index.
	if filepath.Clean(filepath.Dir(gitDir)) != filepath.Join(common, "worktrees") {
		return dir
	}
	if filepath.Base(common) != ".git" {
		return common
	}
	return filepath.Dir(common)
}

// claudeProjectSlug encodes a path the way Claude Code names its per-project
// directory: every character that is not a letter or a digit becomes a dash.
//
// Two details matter and were previously missing. The replacement is over the
// whole character class, not just separators and dots, so an underscore or a
// space produces a dash. And a slug longer than 200 UTF-16 units is truncated
// there and given a dash plus a base-36 hash of the untruncated path, which is
// the only reason a deeply nested project's index can be found at all. Both are
// reproduced over UTF-16 code units because that is what the harness's regex and
// charCodeAt loop count.
func claudeProjectSlug(path string) string {
	units := utf16.Encode([]rune(path))
	slug := make([]byte, 0, len(units))
	for _, u := range units {
		switch {
		case u >= '0' && u <= '9', u >= 'A' && u <= 'Z', u >= 'a' && u <= 'z':
			slug = append(slug, byte(u))
		default:
			slug = append(slug, '-')
		}
	}
	if len(slug) <= claudeSlugMaxUnits {
		return string(slug)
	}
	return string(slug[:claudeSlugMaxUnits]) + "-" + strconv.FormatInt(claudeSlugHash(path), 36)
}

// claudeSlugHash is the 31-based rolling hash Claude Code appends to a truncated
// slug, over UTF-16 code units, wrapped to a signed 32-bit integer and taken
// absolute. The absolute value is computed in 64 bits so the one input that
// hashes to math.MinInt32 does not wrap back to itself.
func claudeSlugHash(path string) int64 {
	var h int32
	for _, u := range utf16.Encode([]rune(path)) {
		h = h*31 + int32(u)
	}
	if h < 0 {
		return -int64(h)
	}
	return int64(h)
}

// fileExists reports whether path is a readable non-directory.
func fileExists(stat func(string) (os.FileInfo, error), path string) bool {
	if path == "" {
		return false
	}
	info, err := stat(path)
	return err == nil && !info.IsDir()
}

// fileHasBytes reports whether path is a non-empty file. An empty index is not
// evidence of anything, so it never wins the fallback ladder.
func fileHasBytes(stat func(string) (os.FileInfo, error), path string) bool {
	if path == "" {
		return false
	}
	info, err := stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// fileOrDirExists reports whether path exists in any form, which is the test the
// git-root walk applies to .git — it is a directory in a normal clone and a file
// in a linked worktree or a submodule.
func fileOrDirExists(stat func(string) (os.FileInfo, error), path string) bool {
	_, err := stat(path)
	return err == nil
}
