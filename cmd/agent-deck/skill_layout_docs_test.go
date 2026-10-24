package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The repo ships exactly one skill: everything about Agent Deck is a
// reference file under skills/agent-deck, triggered from its SKILL.md
// (owner rule, ASK-104). A second SKILL.md under skills/ would be a sibling
// skill agents have to discover separately, and the plugin manifest would
// drift from the folder.
func TestRepoShipsOneAgentDeckSkill(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	for _, e := range entries {
		if e.Name() != "agent-deck" {
			t.Errorf("skills/%s: the repo ships one skill; fold it into skills/agent-deck/references", e.Name())
		}
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "SKILL.md" && path != filepath.Join(root, "agent-deck", "SKILL.md") {
			t.Errorf("%s: nested SKILL.md; make it a reference file of the agent-deck skill", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatalf("read marketplace.json: %v", err)
	}
	var manifest struct {
		Plugins []struct {
			Skills []string `json:"skills"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse marketplace.json: %v", err)
	}
	for _, p := range manifest.Plugins {
		if len(p.Skills) != 1 || p.Skills[0] != "./skills/agent-deck" {
			t.Errorf("marketplace.json plugin skills = %v, want [./skills/agent-deck]", p.Skills)
		}
	}
}

var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// Every relative link in the skill must resolve, so a folded or renamed
// reference cannot leave the SKILL.md table pointing at a missing file.
func TestAgentDeckSkillRelativeLinksResolve(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "skills", "agent-deck")
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
		for i, line := range strings.Split(string(content), "\n") {
			for _, m := range markdownLink.FindAllStringSubmatch(line, -1) {
				target, _, _ := strings.Cut(m[1], "#")
				if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), target)); err != nil {
					t.Errorf("%s:%d links %q, which does not exist", path, i+1, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// Each area folded into the skill keeps its own task evals, so a with/without
// skill run can show the reference still helps.
func TestAgentDeckSkillEvalsCoverEveryArea(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "skills", "agent-deck", "evals", "evals.json"))
	if err != nil {
		t.Fatalf("read evals.json: %v", err)
	}
	var doc struct {
		Evals []struct {
			ID       int    `json:"id"`
			Area     string `json:"area"`
			Prompt   string `json:"prompt"`
			Expected string `json:"expected_output"`
		} `json:"evals"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse evals.json: %v", err)
	}
	count := map[string]int{}
	seen := map[int]bool{}
	for _, e := range doc.Evals {
		if seen[e.ID] {
			t.Errorf("eval id %d is duplicated", e.ID)
		}
		seen[e.ID] = true
		if e.Prompt == "" || e.Expected == "" {
			t.Errorf("eval %d needs both prompt and expected_output", e.ID)
		}
		count[e.Area]++
	}
	for _, area := range []string{"core", "fleet", "session-share", "recall", "retro", "repro", "contributing"} {
		if count[area] < 2 {
			t.Errorf("area %q has %d task evals, want at least 2", area, count[area])
		}
	}
}

// removedSkillPath matches paths of skills that were folded into
// skills/agent-deck (ASK-104 fleet/session-share/retro/repro/recall,
// ASK-103 the .github contributor skill).
var removedSkillPath = regexp.MustCompile(`skills/(fleet|session-share|deck-retro|deck-repro)\b|\.\./(fleet|session-share|deck-retro|deck-repro)/|recall/SKILL\.md|\.github/skills/agent-deck-contributor`)

// removedSkillPathAllowed lists files that may still name a removed path:
// history, and the eval-smoke path filter, which needs a workflow-scope edit
// (pending owner review) before it can follow the move.
var removedSkillPathAllowed = map[string]string{
	"CHANGELOG.md": "",
	"cmd/agent-deck/skill_layout_docs_test.go": "",
	".github/workflows/eval-smoke.yml":         "      - '.github/skills/agent-deck-contributor/scripts/self-check.sh'",
}

// No file in the repo may point at a skill path that was folded away, so a
// moved script or reference cannot leave a dead link or a broken command.
func TestNoReferenceToRemovedSkillPaths(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".claude-worktrees", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		allowedLine, allowed := removedSkillPathAllowed[rel]
		if allowed && allowedLine == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 4<<20 {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil || strings.IndexByte(string(content), 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(content), "\n") {
			if !removedSkillPath.MatchString(line) || (allowed && line == allowedLine) {
				continue
			}
			t.Errorf("%s:%d names a removed skill path: %s", rel, i+1, strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
