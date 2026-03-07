package git

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/vcs"
	"github.com/stretchr/testify/require"
)

func TestSanitizeBranchForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple branch name",
			input:    "feature-branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with slash",
			input:    "feature/new-thing",
			expected: "feature-new-thing",
		},
		{
			name:     "branch with multiple slashes",
			input:    "user/feature/sub",
			expected: "user-feature-sub",
		},
		{
			name:     "branch with backslash",
			input:    "feature\\branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with colon",
			input:    "feature:branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with asterisk",
			input:    "feature*branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with question mark",
			input:    "feature?branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with quotes",
			input:    "feature\"branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with angle brackets",
			input:    "feature<branch>",
			expected: "feature-branch",
		},
		{
			name:     "branch with pipe",
			input:    "feature|branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with at symbol",
			input:    "feature@branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with hash",
			input:    "feature#123",
			expected: "feature-123",
		},
		{
			name:     "branch with space",
			input:    "feature branch",
			expected: "feature-branch",
		},
		{
			name:     "branch with multiple special chars",
			input:    "user/feature@v1.0",
			expected: "user-feature-v1.0",
		},
		// Edge cases for dash collapsing and trimming.
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "consecutive slashes become single dash",
			input:    "feature//name",
			expected: "feature-name",
		},
		{
			name:     "many consecutive special chars",
			input:    "feature////@@@name",
			expected: "feature-name",
		},
		{
			name:     "leading special chars trimmed",
			input:    "/feature",
			expected: "feature",
		},
		{
			name:     "trailing special chars trimmed",
			input:    "feature/",
			expected: "feature",
		},
		{
			name:     "leading and trailing special chars trimmed",
			input:    "///feature///",
			expected: "feature",
		},
		{
			name:     "all special characters becomes empty",
			input:    "////@@@###",
			expected: "",
		},
		{
			name:     "unicode characters preserved",
			input:    "feature/brañch",
			expected: "feature-brañch",
		},
		{
			name:     "chinese characters preserved",
			input:    "feature/功能分支",
			expected: "feature-功能分支",
		},
		{
			name:     "mixed dashes and special chars",
			input:    "feat--//--ure",
			expected: "feat-ure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := sanitizeBranchForPath(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestEscapeBranchForPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "branch with slash stays distinct",
			input:    "feature/new-thing",
			expected: "feature%2Fnew-thing",
		},
		{
			name:     "branch with dash remains literal",
			input:    "feature-new-thing",
			expected: "feature-new-thing",
		},
		{
			name:     "percent is escaped",
			input:    "feature%2Fnew-thing",
			expected: "feature%252Fnew-thing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := escapeBranchForPath(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}

	// Collision regression check for the reported branch pair.
	require.NotEqual(t,
		escapeBranchForPath("feature/foo"),
		escapeBranchForPath("feature-foo"),
	)
}

func TestResolveTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		vars     templateVars
		expected string
	}{
		{
			name:     "sibling pattern",
			template: "{repo-root}-{branch}",
			vars: templateVars{
				branch:    "feature-branch",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/Users/me/src/my-project-feature-branch",
		},
		{
			name:     "subdirectory pattern",
			template: "{repo-root}/.worktrees/{branch}",
			vars: templateVars{
				branch:    "feature-branch",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/Users/me/src/my-project/.worktrees/feature-branch",
		},
		{
			name:     "central location pattern",
			template: "/Users/me/worktrees/{repo-name}/{branch}",
			vars: templateVars{
				branch:    "feature-branch",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/Users/me/worktrees/my-project/feature-branch",
		},
		{
			name:     "pattern with session ID",
			template: "/tmp/wt/{repo-name}/{branch}-{session-id}",
			vars: templateVars{
				branch:    "feature",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/tmp/wt/my-project/feature-a1b2c3d4",
		},
		{
			name:     "relative path pattern",
			template: "../worktrees/{repo-name}/{branch}",
			vars: templateVars{
				branch:    "feature-branch",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/Users/me/src/worktrees/my-project/feature-branch",
		},
		{
			name:     "branch with slash gets sanitized",
			template: "{repo-root}/.worktrees/{branch}",
			vars: templateVars{
				branch:        "feature/new-thing",
				branchEscaped: "feature%2Fnew-thing",
				repoName:      "my-project",
				repoRoot:      "/Users/me/src/my-project",
				sessionID:     "a1b2c3d4",
			},
			expected: "/Users/me/src/my-project/.worktrees/feature-new-thing",
		},
		{
			name:     "escaped branch variable avoids collisions",
			template: "{repo-root}/wt/{branch-escaped}",
			vars: templateVars{
				branch:        "feature/foo",
				branchEscaped: "feature%2Ffoo",
				repoName:      "my-project",
				repoRoot:      "/Users/me/src/my-project",
				sessionID:     "a1b2c3d4",
			},
			expected: "/Users/me/src/my-project/wt/feature%2Ffoo",
		},
		{
			name:     "all variables used",
			template: "{repo-root}/../wt/{repo-name}/{branch}/{session-id}",
			vars: templateVars{
				branch:    "main",
				repoName:  "project",
				repoRoot:  "/home/user/src/project",
				sessionID: "12345678",
			},
			expected: "/home/user/src/wt/project/main/12345678",
		},
		{
			name:     "unknown variable left as-is",
			template: "{repo-root}/{unknown}/{branch}",
			vars: templateVars{
				branch:    "feature",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/Users/me/src/my-project/{unknown}/feature",
		},
		{
			name:     "path traversal in template is cleaned",
			template: "{repo-root}/../../../../tmp/{branch}",
			vars: templateVars{
				branch:    "feature",
				repoName:  "my-project",
				repoRoot:  "/Users/me/src/my-project",
				sessionID: "a1b2c3d4",
			},
			expected: "/tmp/feature",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := resolveTemplate(tc.template, tc.vars)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestWorktreePath(t *testing.T) {
	t.Parallel()

	t.Run("uses template when provided", func(t *testing.T) {
		t.Parallel()
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "sibling",
			RepoDir:   "/Users/me/src/my-project",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}/.worktrees/{branch}",
		})
		expected := "/Users/me/src/my-project/.worktrees/feature-branch"
		require.Equal(t, expected, result)
	})

	t.Run("falls back to sibling when template empty", func(t *testing.T) {
		t.Parallel()
		result := WorktreePath(WorktreePathOptions{
			Branch:   "feature-branch",
			Location: "sibling",
			RepoDir:  "/Users/me/src/my-project",
		})
		expected := "/Users/me/src/my-project-feature-branch"
		require.Equal(t, expected, result)
	})

	t.Run("falls back to subdirectory when template empty", func(t *testing.T) {
		t.Parallel()
		result := WorktreePath(WorktreePathOptions{
			Branch:   "feature-branch",
			Location: "subdirectory",
			RepoDir:  "/Users/me/src/my-project",
		})
		expected := filepath.Join("/Users/me/src/my-project", ".worktrees", "feature-branch")
		require.Equal(t, expected, result)
	})

	t.Run("template overrides location preference", func(t *testing.T) {
		t.Parallel()
		// Even with "subdirectory" location, template takes precedence.
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "subdirectory",
			RepoDir:   "/Users/me/src/my-project",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}-{branch}",
		})
		expected := "/Users/me/src/my-project-feature-branch"
		require.Equal(t, expected, result)
	})

	t.Run("session ID included in path", func(t *testing.T) {
		t.Parallel()
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature",
			Location:  "sibling",
			RepoDir:   "/Users/me/src/project",
			SessionID: "abcd1234",
			Template:  "/tmp/wt/{repo-name}/{branch}-{session-id}",
		})
		expected := "/tmp/wt/project/feature-abcd1234"
		require.Equal(t, expected, result)
	})

	t.Run("empty repoDir falls back to GenerateWorktreePath", func(t *testing.T) {
		t.Parallel()
		// When repoDir is empty, fall back even if template is provided.
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "sibling",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}/.worktrees/{branch}",
		})
		// GenerateWorktreePath with empty repoDir produces "-feature-branch".
		expected := "-feature-branch"
		require.Equal(t, expected, result)
	})

	t.Run("root repoDir falls back to GenerateWorktreePath", func(t *testing.T) {
		t.Parallel()
		// When repoDir is "/", filepath.Base returns "/" which is invalid.
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "sibling",
			RepoDir:   "/",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}/.worktrees/{branch}",
		})
		// Falls back to GenerateWorktreePath.
		expected := "/-feature-branch"
		require.Equal(t, expected, result)
	})

	t.Run("parent dir repoDir falls back to GenerateWorktreePath", func(t *testing.T) {
		t.Parallel()
		// When repoDir is "..", filepath.Base returns ".." which is invalid.
		result := WorktreePath(WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "sibling",
			RepoDir:   "..",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}/.worktrees/{branch}",
		})
		// Falls back to GenerateWorktreePath.
		expected := "..-feature-branch"
		require.Equal(t, expected, result)
	})
}

func TestGitBackend_WorktreePath(t *testing.T) {
	t.Parallel()

	t.Run("uses template via backend", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		createTestRepo(t, dir)

		backend, err := NewGitBackend(dir)
		require.NoError(t, err)

		result := backend.WorktreePath(vcs.WorktreePathOptions{
			Branch:    "feature-branch",
			Location:  "sibling",
			SessionID: "a1b2c3d4",
			Template:  "{repo-root}/.worktrees/{branch}",
		})
		expected := filepath.Join(backend.RepoDir(), ".worktrees", "feature-branch")
		require.Equal(t, expected, result)
	})

	t.Run("falls back to location without template", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		createTestRepo(t, dir)

		backend, err := NewGitBackend(dir)
		require.NoError(t, err)

		result := backend.WorktreePath(vcs.WorktreePathOptions{
			Branch:   "feature-branch",
			Location: "sibling",
		})
		expected := backend.RepoDir() + "-feature-branch"
		require.Equal(t, expected, result)
	})
}

func TestGeneratePathID(t *testing.T) {
	t.Parallel()

	t.Run("returns 8-character hex string", func(t *testing.T) {
		t.Parallel()
		id := GeneratePathID()
		require.Len(t, id, 8)
		require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{8}$`), id)
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		t.Parallel()
		seen := make(map[string]bool)
		for range 100 {
			id := GeneratePathID()
			require.False(t, seen[id], "duplicate ID generated: %s", id)
			seen[id] = true
		}
	})
}
