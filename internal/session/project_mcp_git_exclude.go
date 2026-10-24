package session

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// excludeProjectMCPFromGit keeps an agent-deck-written project MCP config
// (.mcp.json, .cursor/mcp.json, opencode.json) out of `git add -A`: it carries
// MCP env values, which are often tokens. When mcpFile lies in a git work tree
// and is neither tracked nor already ignored, its repo-relative path is added
// once to the repository's info/exclude (shared by linked worktrees). A
// tracked file and .gitignore are never touched. Every failure is logged and
// swallowed: this is hygiene, not a reason to fail a session start.
//
// projectRoot is the session's project directory. mcpFile must resolve inside
// it, and git only ever runs with that directory as its working directory;
// the one file written outside it is info/exclude, which must resolve inside
// the repository's (common) git directory.
func excludeProjectMCPFromGit(projectRoot, mcpFile string) {
	if err := addProjectMCPGitExclude(projectRoot, mcpFile); err != nil {
		mcpCatLog.Warn("project_mcp_git_exclude_failed",
			slog.String("path", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(mcpFile), "\n", ""), "\r", "")),
			slog.String("error", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
	}
}

func addProjectMCPGitExclude(projectRoot, mcpFile string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	root, dir, err := resolveProjectMCPDir(projectRoot, mcpFile)
	if err != nil {
		return err
	}
	base := filepath.Base(mcpFile)

	// Repo-relative path: git reports dir's prefix inside its work tree, so
	// the top-level (which may be an ancestor of the project root, e.g. a
	// session started in a subdirectory) is never touched on disk.
	out, err := projectGit(root, dir, nil, gitOpWorkTreePrefix)
	if err != nil {
		return nil // not in a work tree (or no repo at all): nothing to do
	}
	lines := strings.SplitN(out, "\n", 2)
	if len(lines) != 2 || lines[0] != "true" {
		return nil // bare repo or inside a .git directory
	}
	rel := strings.TrimSuffix(lines[1], "\n") + base
	if strings.ContainsAny(rel, "\n\r\x00") {
		return nil // an exclude line cannot hold a newline; never split it into patterns
	}

	// Tracked? Look the path up in the index (any stage), the path going to
	// git on stdin, relative to the working directory.
	var lookup strings.Builder
	for _, stage := range []string{"0", "1", "2", "3"} {
		lookup.WriteString(":" + stage + ":./" + base + "\n")
	}
	out, err = projectGit(root, dir, []byte(lookup.String()), gitOpIndexLookup)
	if err != nil {
		return fmt.Errorf("index lookup: %w", err)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" && !strings.HasSuffix(line, " missing") {
			return nil // tracked: the user chose to commit it
		}
	}

	// Already ignored (.gitignore, global excludes, info/exclude)? Exit 0
	// means ignored, 1 means not ignored, anything else is a failure.
	if _, err := projectGit(root, dir, []byte(base+"\x00"), gitOpCheckIgnore); err == nil {
		return nil
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		return fmt.Errorf("check-ignore: %w", err)
	}

	out, err = projectGit(root, dir, nil, gitOpExcludePath)
	if err != nil {
		return fmt.Errorf("locate info/exclude: %w", err)
	}
	paths := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(paths) != 2 {
		return fmt.Errorf("locate info/exclude: unexpected git output")
	}
	commonDir, excludePath, err := checkExcludeWithinGitDir(paths[0], paths[1])
	if err != nil {
		return err
	}

	pattern := "/" + gitExcludeEscape(rel)
	infoDir := filepath.Dir(excludePath)
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		return err
	}
	// info may be a symlink: what it resolves to must stay inside the git dir.
	realInfo, err := filepath.EvalSymlinks(infoDir)
	if err != nil {
		return err
	}
	if _, ok := pathWithin(commonDir, realInfo); !ok {
		return fmt.Errorf("info/exclude resolves outside the git directory")
	}
	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimRight(line, "\r") == pattern {
			return nil
		}
	}
	var buf bytes.Buffer
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("# agent-deck: project MCP config may hold secrets\n")
	buf.WriteString(pattern + "\n")
	f, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	mcpCatLog.Info("project_mcp_git_excluded",
		slog.String("pattern", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(pattern), "\n", ""), "\r", "")),
		slog.String("exclude", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(excludePath), "\n", ""), "\r", "")))
	return nil
}

// resolveProjectMCPDir returns the project root and mcpFile's directory, both
// absolute, cleaned and symlink-resolved, after checking the directory lies
// inside the root: lexically first, so nothing outside the root is ever
// stat'ed, and again once symlinks are resolved.
func resolveProjectMCPDir(projectRoot, mcpFile string) (root, dir string, err error) {
	if projectRoot == "" || mcpFile == "" {
		return "", "", fmt.Errorf("project git: empty project root or file")
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", "", err
	}
	absFile, err := filepath.Abs(mcpFile)
	if err != nil {
		return "", "", err
	}
	lexDir, ok := pathWithin(absRoot, filepath.Dir(absFile))
	if !ok {
		return "", "", fmt.Errorf("project git: MCP file is outside the project root")
	}
	if root, err = filepath.EvalSymlinks(absRoot); err != nil {
		return "", "", err
	}
	realDir, err := filepath.EvalSymlinks(lexDir)
	if err != nil {
		return "", "", err
	}
	if dir, ok = pathWithin(root, realDir); !ok {
		return "", "", fmt.Errorf("project git: MCP file resolves outside the project root")
	}
	return root, dir, nil
}

// checkExcludeWithinGitDir accepts the info/exclude path git reported only
// when it lies inside the (common) git directory git reported, so a crafted
// repository cannot point the append at an arbitrary file.
func checkExcludeWithinGitDir(commonDir, excludePath string) (string, string, error) {
	if !filepath.IsAbs(commonDir) || !filepath.IsAbs(excludePath) {
		return "", "", fmt.Errorf("locate info/exclude: git reported a relative path")
	}
	commonDir = filepath.Clean(commonDir)
	inside, ok := pathWithin(commonDir, excludePath)
	if !ok || inside == commonDir {
		return "", "", fmt.Errorf("info/exclude is outside the git directory")
	}
	rel, err := filepath.Rel(commonDir, inside)
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", "", fmt.Errorf("locate info/exclude: %w", err)
	}
	return real, filepath.Join(real, rel), nil
}

// pathWithin reports whether p is root or lies beneath it. Both must be
// absolute; they are cleaned, compared with filepath.Rel, and the result is
// rebuilt as root joined with the checked relative path.
func pathWithin(root, p string) (string, bool) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(p) {
		return "", false
	}
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(root, rel), true
}

// projectGitTimeout bounds each git call: a hung repo or a stalled network
// mount must not block a session start.
const projectGitTimeout = 5 * time.Second

// projectGitOp names one of the fixed git invocations projectGit may run.
// Every argv is a string literal: caller-derived paths reach git only as
// the working directory (validated) or on stdin.
type projectGitOp int

const (
	gitOpWorkTreePrefix projectGitOp = iota
	gitOpIndexLookup
	gitOpCheckIgnore
	gitOpExcludePath
)

func projectGitArgv(op projectGitOp) ([]string, bool) {
	// The project may be a cloned or unpacked repo whose .git/config names
	// commands, so the config keys that run a program for these read-only
	// calls are switched off.
	switch op {
	case gitOpWorkTreePrefix:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "rev-parse", "--is-inside-work-tree", "--show-prefix"}, true
	case gitOpIndexLookup:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "cat-file", "--batch-check"}, true
	case gitOpCheckIgnore:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "check-ignore", "-q", "--no-index", "--stdin", "-z"}, true
	case gitOpExcludePath:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "rev-parse", "--path-format=absolute", "--git-common-dir", "--git-path", "info/exclude"}, true
	}
	return nil, false
}

// projectGit runs one fixed git operation with dir as the working directory
// (validated to lie inside root) and stdin as input, returning stdout with
// its final newline removed.
func projectGit(root, dir string, stdin []byte, op projectGitOp) (string, error) {
	argv, ok := projectGitArgv(op)
	if !ok {
		return "", fmt.Errorf("project git: unknown operation")
	}
	workDir, err := validProjectGitDir(root, dir)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), projectGitTimeout)
	defer cancel()
	// #nosec G204 -- "git" is a fixed binary run with a literal argv (no
	// shell); the working directory is validated to lie inside the project
	// root; caller-derived paths go only on stdin.
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	return strings.TrimSuffix(string(out), "\n"), err
}

// validProjectGitDir returns dir cleaned when it is an absolute path inside
// the absolute root and names an existing directory, and an error otherwise.
// Containment is checked before the directory is stat'ed. A relative or
// option-shaped ("-…") value, or one holding a NUL or a line break, is never
// handed to git.
func validProjectGitDir(root, dir string) (string, error) {
	for _, p := range []string{root, dir} {
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\n\r") {
			return "", fmt.Errorf("project git: unusable directory")
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("project git: directory is not absolute")
		}
	}
	clean, ok := pathWithin(root, dir)
	if !ok {
		return "", fmt.Errorf("project git: directory is outside the project root")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("project git: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project git: not a directory")
	}
	return clean, nil
}

// gitExcludeEscape backslash-escapes characters that gitignore treats as
// pattern syntax, so the line matches exactly this one path.
func gitExcludeEscape(rel string) string {
	var b strings.Builder
	for i, r := range rel {
		switch r {
		case '*', '?', '[', '\\':
			b.WriteByte('\\')
		case '!', '#':
			if i == 0 {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	s := b.String()
	trimmed := strings.TrimRight(s, " ")
	return trimmed + strings.Repeat("\\ ", len(s)-len(trimmed))
}
