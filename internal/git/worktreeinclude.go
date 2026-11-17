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
// The walk root treats a pattern with a separator in the middle as anchored at
// the repository root, which is the gitignore rule. The matcher is looser: it
// also accepts the same relative path under any parent (sub/config/local.json
// for config/local.json). Such a nested path is no longer discovered. Git
// ignores it through a different rule, so the anchored reading is the useful
// one, and it is what makes the narrow walk possible.
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
	if roots[0] == "." {
		// A full walk already covers every other root.
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

// walkIncludeRoots collects the paths under roots that match matcher. It stops
// at repository boundaries and never leaves the given roots.
func walkIncludeRoots(rootFS fs.FS, roots []string, matcher *ignore.GitIgnore) ([]string, error) {
	var candidates []string
	for _, root := range roots {
		if _, err := fs.Stat(rootFS, root); err != nil {
			continue
		}
		if insideNestedRepo(rootFS, root) {
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
