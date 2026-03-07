package vcs

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// branchSanitizer replaces filesystem-unsafe characters with dashes.
var branchSanitizer = strings.NewReplacer(
	"/", "-",
	"\\", "-",
	":", "-",
	"*", "-",
	"?", "-",
	"\"", "-",
	"<", "-",
	">", "-",
	"|", "-",
	"@", "-",
	"#", "-",
	" ", "-",
)

// consecutiveDashes matches two or more consecutive dashes.
var consecutiveDashes = regexp.MustCompile(`-{2,}`)

// templateVars holds values for template substitution.
type templateVars struct {
	branch        string
	branchEscaped string
	repoName      string
	repoRoot      string
	sessionID     string
}

// WorktreePathOptions configures worktree path generation.
type WorktreePathOptions struct {
	Branch    string
	Location  string
	RepoDir   string
	SessionID string
	Template  string
}

// sanitizeBranchForPath converts a branch name to a safe path component.
func sanitizeBranchForPath(branch string) string {
	result := branchSanitizer.Replace(branch)
	result = consecutiveDashes.ReplaceAllString(result, "-")
	return strings.Trim(result, "-")
}

// escapeBranchForPath returns a reversible path-safe branch representation.
func escapeBranchForPath(branch string) string {
	return url.PathEscape(branch)
}

// resolveTemplate expands a path template with the given variables.
func resolveTemplate(template string, vars templateVars) string {
	sanitizedBranch := sanitizeBranchForPath(vars.branch)

	replacer := strings.NewReplacer(
		"{repo-name}", vars.repoName,
		"{repo-root}", vars.repoRoot,
		"{branch}", sanitizedBranch,
		"{branch-escaped}", vars.branchEscaped,
		"{session-id}", vars.sessionID,
	)
	resolved := replacer.Replace(template)

	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(vars.repoRoot, resolved)
	}

	return filepath.Clean(resolved)
}

// GenerateWorktreePath generates a worktree directory path based on the
// repository directory, branch name, and location strategy.
func GenerateWorktreePath(repoDir, branchName, location string) string {
	sanitized := branchName
	sanitized = strings.ReplaceAll(sanitized, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")

	if strings.Contains(location, "/") || strings.HasPrefix(location, "~") {
		expanded := location
		if strings.HasPrefix(expanded, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, expanded[2:])
			}
		} else if expanded == "~" {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = home
			}
		}
		repoName := filepath.Base(repoDir)
		return filepath.Join(expanded, repoName, sanitized)
	}

	switch location {
	case "subdirectory":
		return filepath.Join(repoDir, ".worktrees", sanitized)
	default:
		return repoDir + "-" + sanitized
	}
}

// WorktreePath generates a worktree path. If opts.Template is set, it expands
// the template with variables. Falls back to location-based strategy.
func WorktreePath(opts WorktreePathOptions) string {
	repoName := filepath.Base(opts.RepoDir)
	if opts.Template == "" || opts.RepoDir == "" || repoName == "." || repoName == "/" || repoName == ".." {
		return GenerateWorktreePath(opts.RepoDir, opts.Branch, opts.Location)
	}

	vars := templateVars{
		branch:        opts.Branch,
		branchEscaped: escapeBranchForPath(opts.Branch),
		repoName:      repoName,
		repoRoot:      opts.RepoDir,
		sessionID:     opts.SessionID,
	}
	return resolveTemplate(opts.Template, vars)
}

// GeneratePathID returns an 8-character random hex string for path uniqueness.
func GeneratePathID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
