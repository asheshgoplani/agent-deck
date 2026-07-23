package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func setupClaudeHomeSkillSource(t *testing.T) {
	t.Helper()
	withIsolatedHomeAndConfig(t, "")
	store := t.TempDir()
	writeSkillDir(t, store, "alpha", "alpha", "test skill")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"store": {Path: store, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("save skill source: %v", err)
	}
}

func TestAttachSkillToClaudeHomeMaterializesManagedSkill(t *testing.T) {
	setupClaudeHomeSkillSource(t)
	claudeHome := filepath.Join(t.TempDir(), "claude-home")

	attachment, err := AttachSkillToClaudeHome(claudeHome, "store/alpha", "")
	if err != nil {
		t.Fatalf("attach home skill: %v", err)
	}
	if got, want := attachment.TargetPath, "skills/alpha"; got != want {
		t.Fatalf("target_path=%q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("materialized home skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, ".agent-deck", "skills.toml")); err != nil {
		t.Fatalf("home skill manifest: %v", err)
	}
	if !healthyManagedClaudeHomeSkillAttachment(claudeHome, "store/alpha") {
		t.Fatal("materialized Claude home skill is not healthy")
	}
}

func TestAttachSkillToClaudeHomeHealsMissingManagedTarget(t *testing.T) {
	setupClaudeHomeSkillSource(t)
	claudeHome := filepath.Join(t.TempDir(), "claude-home")
	if _, err := AttachSkillToClaudeHome(claudeHome, "store/alpha", ""); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	target := filepath.Join(claudeHome, "skills", "alpha")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove managed link: %v", err)
	}

	if _, err := AttachSkillToClaudeHome(claudeHome, "store/alpha", ""); err != nil {
		t.Fatalf("heal home skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		t.Fatalf("healed home skill: %v", err)
	}
}

func TestAttachSkillToClaudeHomePreservesForeignTarget(t *testing.T) {
	setupClaudeHomeSkillSource(t)
	claudeHome := filepath.Join(t.TempDir(), "claude-home")
	target := filepath.Join(claudeHome, "skills", "alpha")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create foreign target: %v", err)
	}
	marker := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if _, err := AttachSkillToClaudeHome(claudeHome, "store/alpha", ""); err == nil {
		t.Fatal("expected foreign target conflict")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("foreign target changed: data=%q err=%v", data, err)
	}
}

func TestAttachSkillToClaudeHomeRejectsUnsafeHome(t *testing.T) {
	setupClaudeHomeSkillSource(t)
	for _, home := range []string{
		"relative",
		string(os.PathSeparator),
		filepath.Join(t.TempDir(), "link") + "/../shared",
	} {
		if _, err := AttachSkillToClaudeHome(home, "store/alpha", ""); err == nil {
			t.Fatalf("expected unsafe home %q to be rejected", home)
		}
	}
}

func TestAttachSkillToClaudeHomeConcurrentWritersPreserveManifest(t *testing.T) {
	withIsolatedHomeAndConfig(t, "")
	store := t.TempDir()
	const skillCount = 16
	for i := 0; i < skillCount; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		writeSkillDir(t, store, name, name, "concurrent test skill")
	}
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"store": {Path: store, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("save skill source: %v", err)
	}
	claudeHome := filepath.Join(t.TempDir(), "claude-home")
	start := make(chan struct{})
	errs := make(chan error, skillCount)
	var wg sync.WaitGroup
	for i := 0; i < skillCount; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := AttachSkillToClaudeHome(claudeHome, "store/"+name, "")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent attach: %v", err)
		}
	}
	manifest, err := loadClaudeHomeSkillsManifest(claudeHome)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got := len(manifest.Skills); got != skillCount {
		t.Fatalf("manifest entries=%d want %d", got, skillCount)
	}
}
