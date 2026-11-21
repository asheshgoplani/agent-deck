package git

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// ProcessWorktreeInclude copies gitignored files matching patterns in
// .worktreeinclude from repoDir into worktreePath.
// Returns nil if no .worktreeinclude exists.
//
// Matches Claude Code Desktop behavior:
// https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees
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

// includeWalkRoots returns the directories that can contain a match.
// walkAll is set when a pattern can match at any depth, so the whole
// checkout has to be visited. Anchored patterns such as
// ".claude/rules/personal/" only need their own prefix.
func includeWalkRoots(patterns []string) (roots []string, walkAll bool) {
	seen := map[string]bool{}
	for _, pattern := range patterns {
		root, anchored := anchoredIncludeRoot(pattern)
		if !anchored {
			return nil, true
		}
		if root == "" {
			root = "."
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots, false
}

// anchoredIncludeRoot reports the longest directory prefix that contains no
// wildcard. A pattern with no slash matches in every directory, so it is not
// anchored. A negation or a "**" also forces a full walk.
func anchoredIncludeRoot(pattern string) (string, bool) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "!") || strings.Contains(pattern, "**") {
		return "", false
	}
	body := strings.Trim(pattern, "/")
	if !strings.Contains(body, "/") {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(body, "/") {
		if part == "" || strings.ContainsAny(part, "*?[") {
			break
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", false
	}
	return filepath.Join(parts...), true
}

// walkDir is filepath.Walk. Tests replace it to see which trees are visited.
var walkDir = filepath.Walk

func findCandidates(repoDir string, patterns []string, matcher *ignore.GitIgnore) ([]string, error) {
	roots, walkAll := includeWalkRoots(patterns)
	if walkAll || len(roots) == 0 {
		roots = []string{"."}
	}
	var candidates []string
	for _, root := range roots {
		start := repoDir
		if root != "." {
			start = filepath.Join(repoDir, root)
			if _, err := os.Stat(start); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
		}
		err := walkDir(start, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, relErr := filepath.Rel(repoDir, path)
			if relErr != nil || rel == "." {
				return nil
			}
			if info.IsDir() {
				if rel == ".git" {
					return filepath.SkipDir
				}
				// Don't descend into nested worktrees or submodules — each has its
				// own .git entry. Otherwise the walk recurses into agent-deck's own
				// worktree output dir (e.g. .worktrees/) and copies an ever-growing
				// nested forest of every other worktree's files into the new one.
				if path != repoDir {
					if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
						return filepath.SkipDir
					}
				}
			}
			if matcher.MatchesPath(rel) {
				candidates = append(candidates, rel)
				if info.IsDir() {
					return filepath.SkipDir
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
