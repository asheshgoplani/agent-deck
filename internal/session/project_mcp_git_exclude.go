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
func excludeProjectMCPFromGit(mcpFile string) {
	if err := addProjectMCPGitExclude(mcpFile); err != nil {
		mcpCatLog.Warn("project_mcp_git_exclude_failed",
			slog.String("path", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(mcpFile), "\n", ""), "\r", "")),
			slog.String("error", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
	}
}

func addProjectMCPGitExclude(mcpFile string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(mcpFile))
	if err != nil {
		return err
	}
	top, err := projectGit(dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil // not in a work tree (or a bare repo): nothing to do
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	rel, err := filepath.Rel(top, filepath.Join(dir, filepath.Base(mcpFile)))
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if strings.ContainsAny(rel, "\n\r") {
		return nil // an exclude line cannot hold a newline; never split it into patterns
	}
	if _, err := projectGit(top, "ls-files", "--error-unmatch", "--", rel); err == nil {
		return nil // tracked: the user chose to commit it
	}
	if _, err := projectGit(top, "check-ignore", "-q", "--no-index", "--", rel); err == nil {
		return nil // already ignored (.gitignore, global excludes, info/exclude)
	}
	excludePath, err := projectGit(top, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil || excludePath == "" {
		return fmt.Errorf("locate info/exclude: %w", err)
	}
	pattern := "/" + gitExcludeEscape(rel)
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
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o750); err != nil {
		return err
	}
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

// projectGitTimeout bounds each git call: a hung repo or a stalled network
// mount must not block a session start.
const projectGitTimeout = 5 * time.Second

// projectGit runs git in dir and returns its trimmed stdout. The project may
// be a cloned or unpacked repo whose .git/config names commands, so the
// config keys that run a program for these read-only calls are switched off.
func projectGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), projectGitTimeout)
	defer cancel()
	base := []string{"-C", dir, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}
	// #nosec G204 -- "git" is a fixed binary run with an argv (no shell); dir is
	// the session's project path; args are literal subcommands, with the one
	// repo-relative path passed only after "--".
	out, err := exec.CommandContext(ctx, "git", append(base, args...)...).Output()
	return strings.TrimSpace(string(out)), err
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
