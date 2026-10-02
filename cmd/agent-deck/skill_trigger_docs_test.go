package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentDeckSkillDescription returns the frontmatter description line of the
// repo's agent-deck skill.
func agentDeckSkillDescription(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "skills", "agent-deck", "SKILL.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	parts := strings.SplitN(string(content), "---", 3)
	if len(parts) < 3 {
		t.Fatalf("%s has no YAML frontmatter", path)
	}
	for _, line := range strings.Split(parts[1], "\n") {
		if desc, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(desc)
		}
	}
	t.Fatalf("%s frontmatter has no description", path)
	return ""
}

// Conductors and workers find the skill through this vocabulary, so the
// description must keep it as positive triggers while the not-for clause
// handles the near misses (plain git worktrees, plain tmux, in-conversation
// subagents).
func TestAgentDeckSkillDescriptionKeepsTriggerTerms(t *testing.T) {
	t.Parallel()

	desc := agentDeckSkillDescription(t)
	notFor := strings.Index(desc, "Not for")
	if notFor < 0 {
		t.Fatalf("description must keep its not-for clause; got %q", desc)
	}
	positive := desc[:notFor]
	for _, term := range []string{`"agent-deck"`, `"session"`, `"sub-agent"`, `"MCP attach"`, `"git worktree"`} {
		if !strings.Contains(positive, term) {
			t.Errorf("description must list %s as a positive trigger before the not-for clause; got %q", term, desc)
		}
	}
}

// launch accepts --parent/-p as an explicit override of the automatic parent
// (cmd/agent-deck/launch_cmd.go); the skill must not claim otherwise.
func TestAgentDeckSkillDoesNotDenyLaunchParentFlag(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "skills", "agent-deck")
	stale := "`launch` does not accept `-parent`"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), stale) {
			t.Errorf("%s claims %s, but launch accepts --parent/-p as an explicit override", path, stale)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
