package session

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
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
	trackedOp, ok := projectMCPTrackedOp(base)
	if !ok {
		return nil // not a project MCP config name agent-deck writes
	}

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

	// Tracked (any index entry: staged, conflicted or intent-to-add)? The
	// file name is a literal in the argv and resolves in the working
	// directory. Exit 0 means tracked, 1 untracked. --sparse keeps a sparse
	// index from being expanded (expansion reads tree objects); a file inside
	// a sparse directory then counts as untracked, which only adds a harmless
	// exclude line. What actually stops a partial clone's lazy fetch (which
	// obeys the repo's own config, e.g. core.sshCommand) on any object read
	// is GIT_NO_LAZY_FETCH=1, set by projectGit.
	if _, err := projectGit(root, dir, nil, trackedOp); err == nil {
		return nil // tracked: the user chose to commit it
	} else if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		return fmt.Errorf("ls-files: %w", err)
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
	// O_NOFOLLOW: a symlinked info/exclude is refused, never followed out of
	// the git directory, for the read as well as the append.
	f, err := os.OpenFile(excludePath, os.O_RDWR|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open info/exclude: %w", err)
	}
	// Only a regular file: reading a FIFO (or a device) would block or worse.
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return fmt.Errorf("open info/exclude: %w", err)
	}
	existing, err := io.ReadAll(io.LimitReader(f, maxGitExcludeBytes+1))
	if err == nil && len(existing) > maxGitExcludeBytes {
		err = fmt.Errorf("larger than %d bytes", maxGitExcludeBytes)
	}
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("read info/exclude: %w", err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimRight(line, "\r") == pattern {
			return f.Close()
		}
	}
	var buf bytes.Buffer
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("# agent-deck: project MCP config may hold secrets\n")
	buf.WriteString(pattern + "\n")
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
	gitOpWorkTreePrefix  projectGitOp = iota
	gitOpTrackedClaude                // .mcp.json
	gitOpTrackedCursor                // mcp.json (in .cursor/)
	gitOpTrackedOpenCode              // opencode.json
	gitOpCheckIgnore
	gitOpExcludePath
)

// maxGitExcludeBytes bounds how much of info/exclude is read.
const maxGitExcludeBytes = 4 << 20

// projectMCPTrackedOp maps the base name of a project MCP config agent-deck
// writes to the ls-files operation naming it literally.
func projectMCPTrackedOp(base string) (projectGitOp, bool) {
	switch base {
	case ".mcp.json":
		return gitOpTrackedClaude, true
	case "mcp.json":
		return gitOpTrackedCursor, true
	case "opencode.json":
		return gitOpTrackedOpenCode, true
	}
	return 0, false
}

func projectGitArgv(op projectGitOp) ([]string, bool) {
	// The project may be a cloned or unpacked repo whose .git/config names
	// commands, so the config keys that run a program for these read-only
	// calls are switched off.
	switch op {
	case gitOpWorkTreePrefix:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "rev-parse", "--is-inside-work-tree", "--show-prefix"}, true
	case gitOpTrackedClaude:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "ls-files", "--sparse", "--error-unmatch", "--", ".mcp.json"}, true
	case gitOpTrackedCursor:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "ls-files", "--sparse", "--error-unmatch", "--", "mcp.json"}, true
	case gitOpTrackedOpenCode:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "ls-files", "--sparse", "--error-unmatch", "--", "opencode.json"}, true
	case gitOpCheckIgnore:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "check-ignore", "-q", "--no-index", "--stdin", "-z"}, true
	case gitOpExcludePath:
		return []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "rev-parse", "--path-format=absolute", "--git-common-dir", "--git-path", "info/exclude"}, true
	}
	return nil, false
}

// gitLocalEnvVars is what `git rev-parse --local-env-vars` lists (plus
// GIT_NAMESPACE and GIT_INTERNAL_SUPER_PREFIX): variables that point git at
// another repository, index, object store or config. Inherited, they could
// redirect these calls (and the info/exclude write) away from the project.
var gitLocalEnvVars = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_INTERNAL_SUPER_PREFIX":        true,
}

// projectGitEnv returns env without the repo-local git variables (and the
// GIT_CONFIG_KEY_<n>/GIT_CONFIG_VALUE_<n> pairs), plus GIT_NO_LAZY_FETCH=1 so
// a partial clone never fetches a missing object (which would run the repo's
// own transport config; honoured by git >= 2.44), and GIT_TERMINAL_PROMPT=0
// so git never waits on a credential prompt.
func projectGitEnv(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if gitLocalEnvVars[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") ||
			name == "GIT_NO_LAZY_FETCH" || name == "GIT_TERMINAL_PROMPT" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
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
	// root; caller-derived paths go only on stdin; file names are literals.
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = projectGitEnv(childenv.ForLaunch(""))
	out, err := cmd.Output()
	return strings.TrimSuffix(string(out), "\n"), err
}

// validProjectGitDir returns dir cleaned when it is an absolute path inside
// the absolute root, and an error otherwise. It is purely lexical: whether the
// directory exists is left to the chdir when git starts. A relative or
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
	// No stat here: a missing or non-directory path already fails when the
	// child changes into it, and projectGit returns that error.
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
