package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func setupCodexHomeSkillSource(t *testing.T) string {
	t.Helper()
	withIsolatedHomeAndConfig(t, "")
	store := t.TempDir()
	writeSkillDir(t, store, "alpha", "alpha", "test skill")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"store": {Path: store, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatalf("save skill source: %v", err)
	}
	return store
}

func TestAttachSkillToCodexHomeMaterializesManagedSkill(t *testing.T) {
	setupCodexHomeSkillSource(t)
	codexHome := filepath.Join(t.TempDir(), "codex-home")

	attachment, err := AttachSkillToCodexHome(codexHome, "store/alpha", "")
	if err != nil {
		t.Fatalf("attach home skill: %v", err)
	}
	if got, want := attachment.TargetPath, "skills/alpha"; got != want {
		t.Fatalf("target_path=%q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("materialized home skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexHome, ".agent-deck", "skills.toml")); err != nil {
		t.Fatalf("home skill manifest: %v", err)
	}
}

func TestAttachSkillToCodexHomeHealsMissingManagedTarget(t *testing.T) {
	setupCodexHomeSkillSource(t)
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	if _, err := AttachSkillToCodexHome(codexHome, "store/alpha", ""); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	target := filepath.Join(codexHome, "skills", "alpha")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove managed link: %v", err)
	}

	if _, err := AttachSkillToCodexHome(codexHome, "store/alpha", ""); err != nil {
		t.Fatalf("heal home skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		t.Fatalf("healed home skill: %v", err)
	}
}

func TestAttachSkillToCodexHomePreservesForeignTarget(t *testing.T) {
	setupCodexHomeSkillSource(t)
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	target := filepath.Join(codexHome, "skills", "alpha")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("create foreign target: %v", err)
	}
	marker := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if _, err := AttachSkillToCodexHome(codexHome, "store/alpha", ""); err == nil {
		t.Fatal("expected foreign target conflict")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("foreign target changed: data=%q err=%v", data, err)
	}
}

func TestAttachSkillToCodexHomeConcurrentWritersPreserveManifest(t *testing.T) {
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
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	start := make(chan struct{})
	errs := make(chan error, skillCount)
	var wg sync.WaitGroup
	for i := 0; i < skillCount; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := AttachSkillToCodexHome(codexHome, "store/"+name, "")
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
	manifest, err := loadCodexHomeSkillsManifest(codexHome)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got := len(manifest.Skills); got != skillCount {
		t.Fatalf("manifest entries=%d want %d", got, skillCount)
	}
}
