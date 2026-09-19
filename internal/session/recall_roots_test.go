package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecallClaudeRoots_ProfilesDedupeAndScratch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	work := filepath.Join(home, ".claude-work")
	for _, d := range []string{filepath.Join(home, ".claude", "projects"), filepath.Join(work, "projects")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "settings.json"), []byte(`{"cleanupPeriodDays": 3650}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlinked alias of the personal dir must not become a second root.
	alias := filepath.Join(home, "claude-alias")
	if err := os.Symlink(filepath.Join(home, ".claude"), alias); err != nil {
		t.Fatal(err)
	}
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"personal": {Claude: ProfileClaudeSettings{ConfigDir: "~/.claude"}},
		"work":     {Claude: ProfileClaudeSettings{ConfigDir: work}},
	}}
	cfg.Claude.ConfigDir = alias
	cfg.Groups = map[string]GroupSettings{"g": {Claude: GroupClaudeSettings{ConfigDir: "~/.claude-work"}}}
	withConfig(t, cfg)
	// Three worker-scratch generations, two with the symlinked projects,
	// one without a projects entry at all.
	scratch := workerScratchDirRoot()
	for i, withProjects := range []bool{true, true, false} {
		gen := filepath.Join(scratch, "gen-"+string(rune('a'+i)))
		if err := os.MkdirAll(gen, 0o755); err != nil {
			t.Fatal(err)
		}
		if withProjects {
			if err := os.Symlink(filepath.Join(home, ".claude", "projects"), filepath.Join(gen, "projects")); err != nil {
				t.Fatal(err)
			}
		}
	}
	roots := RecallClaudeRoots()
	byDir := map[string]string{}
	for _, r := range roots {
		byDir[r.Dir] = r.Profile
	}
	if len(roots) != 4 {
		t.Fatalf("roots = %+v", roots)
	}
	if byDir[filepath.Join(home, ".claude")] != "personal" || byDir[work] != "work" {
		t.Fatalf("profile labels: %v", byDir)
	}
	if _, ok := byDir[alias]; ok {
		t.Fatalf("symlinked alias became its own root: %v", byDir)
	}
	var scratchRoots, workRetention int
	for _, r := range roots {
		if filepath.Dir(r.Dir) == scratch {
			scratchRoots++
			if r.Profile != "" {
				t.Fatalf("scratch root carries a profile: %+v", r)
			}
		}
		if r.Dir == work {
			workRetention = r.RetentionDays
		}
	}
	if scratchRoots != 2 || workRetention != 3650 {
		t.Fatalf("scratch roots %d, work retention %d", scratchRoots, workRetention)
	}
	for _, r := range roots {
		if r.Dir == filepath.Join(home, ".claude") && r.RetentionDays != recallDefaultRetentionDays {
			t.Fatalf("default retention: %+v", r)
		}
	}
}

func TestValidateRecallTranscriptPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	work := filepath.Join(home, ".claude-work")
	if err := os.MkdirAll(filepath.Join(work, "projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	withConfig(t, &UserConfig{Profiles: map[string]ProfileSettings{"work": {Claude: ProfileClaudeSettings{ConfigDir: work}}}})
	gen := filepath.Join(workerScratchDirRoot(), "gen-1")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "projects"), filepath.Join(gen, "projects")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude", "projects", "escape")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		ok   bool
	}{
		{filepath.Join(home, ".claude", "projects", "p", "t.jsonl"), true},
		{filepath.Join(work, "projects", "p", "t.jsonl"), true},
		{filepath.Join(gen, "projects", "p", "t.jsonl"), true}, // scratch symlink into the work profile
		{filepath.Join(home, ".claude-spoof", "t.jsonl"), false},
		{filepath.Join(home, "elsewhere", "t.jsonl"), false},
		{filepath.Join(home, ".claude", "projects", "escape", "t.jsonl"), false}, // link out of every root
		{"relative/t.jsonl", false},
		{"", false},
		{filepath.Join(home, ".claude", "..", "etc", "passwd"), false},
	}
	for _, c := range cases {
		if _, ok := ValidateRecallTranscriptPath(c.path); ok != c.ok {
			t.Errorf("%q: ok=%v want %v", c.path, ok, c.ok)
		}
	}
}
