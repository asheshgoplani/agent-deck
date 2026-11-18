package git

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// ProcessWorktreeInclude copies gitignored files matching patterns in
// .worktreeinclude from repoDir into worktreePath.
// Returns nil if no .worktreeinclude exists.
//
// Matches Claude Code Desktop behavior:
// https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees
//
// The walk starts at the static directory prefix of each pattern, not at the
// repository root. A narrow pattern such as ".claude/rules/personal/" therefore
// costs one subtree walk. Repositories with large build output hold millions of
// ignored entries, and a full walk there takes about a minute.
func ProcessWorktreeInclude(repoDir, worktreePath string, stderr io.Writer) error {
	includePath := filepath.Join(repoDir, ".worktreeinclude")
	f, err := os.Open(includePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open .worktreeinclude: %w", err)
	}
	defer f.Close()

	patterns, err := parseWorktreeInclude(f)
	if err != nil {
		return fmt.Errorf("parse .worktreeinclude: %w", err)
	}
	if len(patterns) == 0 {
		return nil
	}

	patterns = anchorPatterns(patterns)
	matcher := ignore.CompileIgnoreLines(patterns...)

	candidates, err := findCandidates(repoDir, patterns, matcher)
	if err != nil {
		return err
	}

	gitignored, err := filterGitignored(repoDir, candidates)
	if err != nil {
		return err
	}

	for _, rel := range gitignored {
		src := filepath.Join(repoDir, rel)
		dst := filepath.Join(worktreePath, rel)

		srcInfo, err := os.Stat(src)
		if err != nil {
			continue
		}

		if !srcInfo.IsDir() {
			if _, err := os.Stat(dst); err == nil {
				continue
			}
		}

		if err := copyEntry(src, dst, srcInfo); err != nil {
			fmt.Fprintf(stderr, "worktreeinclude: failed to copy %s: %v\n", rel, err)
		}
	}

	return nil
}

func parseWorktreeInclude(r io.Reader) ([]string, error) {
	var patterns []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, scanner.Err()
}

// anchorPatterns applies the gitignore anchor rule to every pattern. A pattern
// with a separator in the middle is relative to the repository root, so it gets
// a leading "/", which the matcher treats as an anchor. Without this step the
// matcher also accepts the same relative path under any parent, and whether
// such a nested file is copied would depend on whether another pattern forced a
// full walk.
func anchorPatterns(patterns []string) []string {
	anchored := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		anchored = append(anchored, anchorPattern(pattern))
	}
	return anchored
}

// anchorPattern returns pattern with a leading "/" when gitignore anchors it at
// the repository root. Bare names, names with only a trailing separator and a
// leading "**/" keep matching at any depth.
func anchorPattern(pattern string) string {
	negate := strings.HasPrefix(pattern, "!")
	body := strings.TrimPrefix(pattern, "!")
	if strings.HasPrefix(body, "/") || strings.HasPrefix(body, "**/") {
		return pattern
	}
	if !strings.Contains(strings.TrimSuffix(body, "/"), "/") {
		return pattern
	}
	if negate {
		return "!/" + body
	}
	return "/" + body
}

// findCandidates returns the repository-relative paths that match patterns.
func findCandidates(repoDir string, patterns []string, matcher *ignore.GitIgnore) ([]string, error) {
	return walkIncludeRoots(os.DirFS(repoDir), walkRoots(patterns), matcher)
}

// walkRoots returns the repository-relative directories that hold a possible
// match. It returns ["."] when any pattern can match at any depth, such as a
// bare name like ".env" or a pattern whose first segment contains a glob.
func walkRoots(patterns []string) []string {
	var roots []string
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			// A negation cannot add a match, so it cannot widen the walk.
			continue
		}
		root, ok := staticWalkRoot(pattern)
		if !ok {
			return []string{"."}
		}
		roots = append(roots, root)
	}
	return pruneNestedRoots(roots)
}

// staticWalkRoot returns the deepest directory of a pattern that holds no glob
// metacharacter. A directory-only pattern returns the directory itself; a file
// pattern returns the directory that holds the file.
//
// A pattern with a separator in the middle is anchored at the repository root,
// which is the gitignore rule. anchorPatterns applies the same rule to the
// matcher, so the walk and the matcher agree and the narrow walk is valid.
//
// ok is false when the pattern can match at any depth: a bare name, a negation,
// or one whose first path segment contains a glob.
func staticWalkRoot(pattern string) (string, bool) {
	if strings.HasPrefix(pattern, "!") {
		return "", false
	}
	anchored := strings.HasPrefix(pattern, "/")
	trimmed := strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")
	if trimmed == "" {
		return "", false
	}
	segments := strings.Split(trimmed, "/")
	globAt := -1
	for i, segment := range segments {
		if strings.ContainsAny(segment, "*?[\\") {
			globAt = i
			break
		}
	}
	switch {
	case globAt == 0:
		return "", false
	case globAt > 0:
		return strings.Join(segments[:globAt], "/"), true
	case len(segments) == 1 && !anchored:
		// A bare name matches at any depth.
		return "", false
	case len(segments) == 1:
		return ".", true
	default:
		return strings.Join(segments[:len(segments)-1], "/"), true
	}
}

// pruneNestedRoots removes duplicates and drops any root that sits inside
// another root.
func pruneNestedRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	sort.Strings(roots)
	if slices.Contains(roots, ".") {
		// A full walk already covers every other root. Check the whole list:
		// names that start below "." in byte order (for example "-dash") sort
		// before it.
		return []string{"."}
	}
	var kept []string
	for _, root := range roots {
		nested := false
		for _, ancestor := range kept {
			if root == ancestor || strings.HasPrefix(root, ancestor+"/") {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, root)
		}
	}
	return kept
}

// insideNestedRepo reports whether root or one of its parents, up to but
// excluding the repository root, holds a .git entry. Such a root lives in a
// nested worktree or submodule, which the walk must not read.
func insideNestedRepo(rootFS fs.FS, root string) bool {
	for dir := root; dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
		if _, err := fs.Stat(rootFS, path.Join(dir, ".git")); err == nil {
			return true
		}
	}
	return false
}

// isGitDirectoryRoot reports whether root is the repository's .git directory or
// a path inside it. The previous repository-wide walk never read .git, and a
// copy under it would land on the new worktree's .git file.
func isGitDirectoryRoot(root string) bool {
	return root == ".git" || strings.HasPrefix(root, ".git/")
}

// pathCrossesSymlink reports whether root or one of its parents is a symbolic
// link. The previous repository-wide walk never descended into a link, and
// git check-ignore refuses a path whose leading components cross one, so such a
// root cannot produce a candidate that the copy stage can use.
func pathCrossesSymlink(rootFS fs.FS, root string) bool {
	parent := "."
	for _, segment := range strings.Split(root, "/") {
		if segment == "" || segment == "." {
			continue
		}
		entries, err := fs.ReadDir(rootFS, parent)
		if err != nil {
			return true
		}
		found := false
		for _, entry := range entries {
			if entry.Name() != segment {
				continue
			}
			found = true
			if entry.Type()&fs.ModeSymlink != 0 {
				return true
			}
			break
		}
		if !found {
			return true
		}
		parent = path.Join(parent, segment)
	}
	return false
}

// walkIncludeRoots collects the paths under roots that match matcher. It stops
// at repository boundaries and never leaves the given roots.
func walkIncludeRoots(rootFS fs.FS, roots []string, matcher *ignore.GitIgnore) ([]string, error) {
	var candidates []string
	for _, root := range roots {
		if isGitDirectoryRoot(root) {
			continue
		}
		if _, err := fs.Stat(rootFS, root); err != nil {
			continue
		}
		if insideNestedRepo(rootFS, root) || pathCrossesSymlink(rootFS, root) {
			continue
		}
		err := fs.WalkDir(rootFS, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p == "." {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return fs.SkipDir
				}
				// Don't descend into nested worktrees or submodules — each has
				// its own .git entry. Otherwise the walk recurses into
				// agent-deck's own worktree output dir (e.g. .worktrees/) and
				// copies an ever-growing nested forest of every other
				// worktree's files into the new one.
				if _, statErr := fs.Stat(rootFS, path.Join(p, ".git")); statErr == nil {
					return fs.SkipDir
				}
			}
			if matcher.MatchesPath(p) {
				candidates = append(candidates, p)
				if d.IsDir() {
					return fs.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

func filterGitignored(repoDir string, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = repoDir
	cmd.Stdin = strings.NewReader(strings.Join(candidates, "\x00"))
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("git check-ignore: %w", err)
	}

	var result []string
	for _, entry := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if entry != "" {
			result = append(result, entry)
		}
	}
	return result, nil
}

func copyEntry(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// #nosec G703 -- src/dst are agent-deck-managed worktree paths; not derived
	// from external/untrusted input.
	return os.WriteFile(dst, data, mode)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		return copyFile(path, target, info.Mode())
	})
}
