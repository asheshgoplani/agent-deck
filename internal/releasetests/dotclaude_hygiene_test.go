package releasetests

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// allowedDotClaudeFiles is the complete list of files the repo may carry
// under .claude/. Claude Code loads .claude/commands/*.md as slash commands
// and agents read .claude/plans/ as current state, so anything else under
// .claude/ reaches a contributor's coding agent as instructions. The old
// /restart command shipped here ran `git add -A` and pushed.
var allowedDotClaudeFiles = map[string]bool{
	".claude/release-tests.yaml": true,
}

// dotClaudeFiles lists the repo files under .claude/, from git's index when
// git can read the checkout, else from the working tree.
func dotClaudeFiles(t *testing.T, root string) []string {
	t.Helper()
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--", ".claude")
		if out, err := cmd.Output(); err == nil {
			var files []string
			for _, f := range strings.Split(string(out), "\x00") {
				if f != "" {
					files = append(files, f)
				}
			}
			if len(files) > 0 {
				t.Logf("listed .claude/ from the git index (%d files)", len(files))
				return files
			}
		}
	}
	var files []string
	dir := filepath.Join(root, ".claude")
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Gitignored local state (see .gitignore), never tracked.
			if rel == ".claude/worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == ".claude/scheduled_tasks.lock" || rel == ".claude/settings.local.json" {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", dir, err)
	}
	t.Logf("listed .claude/ from the working tree (%d files)", len(files))
	return files
}

// TestDotClaudeHoldsOnlyReleaseManifest pins that .claude/ carries no
// slash commands, plans or other session notes, only the release-test
// manifest that TestManifestIsValidYAML reads.
func TestDotClaudeHoldsOnlyReleaseManifest(t *testing.T) {
	root := repoRoot(t)
	files := dotClaudeFiles(t, root)
	sawManifest := false
	for _, f := range files {
		if allowedDotClaudeFiles[f] {
			sawManifest = true
			continue
		}
		t.Errorf("%s: only %v may live under .claude/; move session notes and slash commands out of the repo", f, keys(allowedDotClaudeFiles))
	}
	if !sawManifest {
		t.Errorf(".claude/release-tests.yaml not found among %v: the listing is broken, so this check proves nothing", files)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
