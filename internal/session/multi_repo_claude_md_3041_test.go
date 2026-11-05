package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression for #3041: a multi-repo launch whose additional path is a
// directory named ".claude" (for example ~/.claude) must not overwrite the
// user's CLAUDE.md or settings.json in that directory.
func TestMultiRepoClaudeDirAdditionalPathKeepsUserFiles_3041(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))

	proj := filepath.Join(root, "proj")
	userClaude := filepath.Join(root, ".claude")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	require.NoError(t, os.MkdirAll(userClaude, 0o755))
	const sentinel = "SENTINEL-original\n"
	const settingsSentinel = `{"permissions":{"deny":["Bash(sentinel)"]}}`
	userMD := filepath.Join(userClaude, "CLAUDE.md")
	userSettings := filepath.Join(userClaude, "settings.json")
	require.NoError(t, os.WriteFile(userMD, []byte(sentinel), 0o644))
	require.NoError(t, os.WriteFile(userSettings, []byte(settingsSentinel), 0o644))
	// A child deny rule makes the parent settings.json non-empty, so it is written.
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".claude", "settings.json"),
		[]byte(`{"permissions":{"deny":["Bash(rm -rf *)"]}}`), 0o644))

	// Mirror the launch path (cmd/agent-deck/remote_creation_catalog.go):
	// one symlink per repo, named by DeduplicateDirnames, then the context.
	parent := filepath.Join(root, "multi-repo-worktrees", "id")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	allPaths := []string{proj, userClaude}
	names := DeduplicateDirnames(allPaths)
	repoNames := make([]string, 0, len(allPaths))
	for i, p := range allPaths {
		link := filepath.Join(parent, names[i])
		_ = os.Symlink(p, link)
		repoNames = append(repoNames, filepath.Base(link))
	}
	_ = ApplyMultiRepoClaudeContext("claude", true, filepath.Join(root, "claude.json"), parent, repoNames)

	got, err := os.ReadFile(userMD)
	require.NoError(t, err)
	assert.Equal(t, sentinel, string(got), "user CLAUDE.md was overwritten")
	gotSettings, err := os.ReadFile(userSettings)
	require.NoError(t, err)
	assert.Equal(t, settingsSentinel, string(gotSettings), "user settings.json was overwritten")

	// The generated file lives in a real directory agent-deck owns.
	fi, err := os.Lstat(filepath.Join(parent, ".claude"))
	require.NoError(t, err)
	assert.True(t, fi.IsDir(), "<parent>/.claude must be a real directory, not a link")
	gen, err := os.ReadFile(filepath.Join(parent, ".claude", "CLAUDE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(gen), "# Multi-Repo Session")
}

// Defense in depth: even if <parent>/.claude is a symlink, the writers refuse.
func TestWriteMultiRepoParentClaudeMDRefusesSymlinkedClaudeDir_3041(t *testing.T) {
	root := t.TempDir()
	userClaude := filepath.Join(root, "user", ".claude")
	require.NoError(t, os.MkdirAll(userClaude, 0o755))
	const sentinel = "SENTINEL-original\n"
	userMD := filepath.Join(userClaude, "CLAUDE.md")
	require.NoError(t, os.WriteFile(userMD, []byte(sentinel), 0o644))

	parent := filepath.Join(root, "parent")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.Symlink(userClaude, filepath.Join(parent, ".claude")))

	assert.Error(t, WriteMultiRepoParentClaudeMD(parent, []string{".claude", "proj"}))
	got, err := os.ReadFile(userMD)
	require.NoError(t, err)
	assert.Equal(t, sentinel, string(got))
}

// A CLAUDE.md that agent-deck did not generate is never overwritten.
func TestWriteMultiRepoParentClaudeMDKeepsForeignFile_3041(t *testing.T) {
	parent := t.TempDir()
	claudeDir := filepath.Join(parent, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	const sentinel = "my own notes\n"
	md := filepath.Join(claudeDir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(md, []byte(sentinel), 0o644))

	assert.Error(t, WriteMultiRepoParentClaudeMD(parent, []string{"proj"}))
	got, err := os.ReadFile(md)
	require.NoError(t, err)
	assert.Equal(t, sentinel, string(got))

	// Our own previously generated file is still refreshed.
	require.NoError(t, os.WriteFile(md, []byte("# Multi-Repo Session\n\nold\n"), 0o644))
	require.NoError(t, WriteMultiRepoParentClaudeMD(parent, []string{"proj"}))
	got, err = os.ReadFile(md)
	require.NoError(t, err)
	assert.Contains(t, string(got), "`proj/`")
}

func TestDeduplicateDirnamesReservedNames_3041(t *testing.T) {
	got := DeduplicateDirnames([]string{"/a/proj", "/home/u/.claude", "/b/dot-claude", "/c/x", "/d/x-1", "/e/x", "/f/.Claude"})
	assert.Equal(t, []string{"proj", "dot-claude", "dot-claude-1", "x", "x-1", "x-2", "dot-claude-2"}, got)
	seen := map[string]bool{}
	for _, n := range got {
		assert.False(t, seen[n], "duplicate name %q", n)
		seen[n] = true
	}
}

// The real-directory guard on <parent>/.claude is what protects settings.json
// and stops a new CLAUDE.md appearing in a linked user dir that has none
// (sessions created before the fix keep their .claude link).
func TestMultiRepoWritersRefuseSymlinkedClaudeDirWithoutClaudeMD_3041(t *testing.T) {
	root := t.TempDir()
	userClaude := filepath.Join(root, "user", ".claude")
	require.NoError(t, os.MkdirAll(userClaude, 0o755))
	const settingsSentinel = `{"permissions":{"allow":["Bash(sentinel)"]}}`
	userSettings := filepath.Join(userClaude, "settings.json")
	require.NoError(t, os.WriteFile(userSettings, []byte(settingsSentinel), 0o644))

	parent := filepath.Join(root, "parent")
	proj := filepath.Join(parent, "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".claude", "settings.json"),
		[]byte(`{"permissions":{"deny":["Bash(rm -rf *)"]}}`), 0o644))
	require.NoError(t, os.Symlink(userClaude, filepath.Join(parent, ".claude")))

	assert.Error(t, WriteMultiRepoParentClaudeMD(parent, []string{"proj"}))
	assert.Error(t, WriteMultiRepoParentSettings(parent, []string{"proj"}))

	_, err := os.Lstat(filepath.Join(userClaude, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(err), "CLAUDE.md was created in the user dir")
	got, err := os.ReadFile(userSettings)
	require.NoError(t, err)
	assert.Equal(t, settingsSentinel, string(got), "user settings.json was overwritten")
}
