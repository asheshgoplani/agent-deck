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

	candidates, err := findCandidates(repoDir, matcher)
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

func findCandidates(repoDir string, matcher *ignore.GitIgnore) ([]string, error) {
	var candidates []string
	err := filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoDir, path)
		if rel == "." {
			return nil
		}
		if info.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if matcher.MatchesPath(rel) {
			candidates = append(candidates, rel)
			if info.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return candidates, err
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
