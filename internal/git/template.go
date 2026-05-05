package git

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
// This sanitizes characters that are problematic in filesystem paths,
// collapses consecutive dashes, and trims leading/trailing dashes.
func sanitizeBranchForPath(branch string) string {
	result := branchSanitizer.Replace(branch)
	result = consecutiveDashes.ReplaceAllString(result, "-")
	return strings.Trim(result, "-")
}

// escapeBranchForPath returns a reversible path-safe branch representation.
// Unlike sanitizeBranchForPath, this avoids collisions:
// - feature/foo    -> feature%2Ffoo
// - feature-foo    -> feature-foo
func escapeBranchForPath(branch string) string {
	return url.PathEscape(branch)
}

// resolveTemplate expands a path template with the given variables.
// Returns the resolved absolute path.
//
// SECURITY NOTE: Templates are trusted input from the user's own config.toml file.
// No path containment validation is performed because the user controls both the
// template and the resulting worktree location. Malicious templates would be
// self-inflicted. The filepath.Clean call normalizes the path but does not
// restrict it to any particular directory.
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

	// Expand ~ to the user's home directory.
	if strings.HasPrefix(resolved, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			resolved = filepath.Join(home, resolved[2:])
		}
	} else if resolved == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			resolved = home
		}
	}

	// Handle relative paths - resolve relative to repo root.
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(vars.repoRoot, resolved)
	}

	return filepath.Clean(resolved)
}

// WorktreePath generates a worktree path. If opts.Template is set, it expands
// the template with variables:
//   - {repo-name}, {repo-root}, {session-id}
//   - {branch}: sanitized (human-friendly, may collide)
//   - {branch-escaped}: URL-escaped (collision-resistant, reversible)
//
// Unknown variables like {foo} are left as-is in the resolved path.
// Falls back to location-based strategy using opts.Location when template is
// empty or RepoDir is invalid.
func WorktreePath(opts WorktreePathOptions) string {
	// Fall back to default path generation when template is empty or repo path is invalid.
	// filepath.Base returns "." for empty/current dir, "/" for root, ".." for parent.
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
// Used to provide a unique identifier in worktree path templates via {session-id}.
func GeneratePathID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
