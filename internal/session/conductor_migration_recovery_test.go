package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationPreservesBothConcurrentEdits(t *testing.T) {
	for _, secondEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "first edit", true: "two edits"}[secondEdit], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "managed")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			original := exchangeGeneratedFiles
			var recovery string
			calls := 0
			exchangeGeneratedFiles = func(from, to string) error {
				calls++
				if calls == 1 {
					recovery = from
					if err := os.WriteFile(to, []byte("edit A"), 0600); err != nil {
						return err
					}
				}
				if err := exchangeGeneratedFile(from, to); err != nil {
					return err
				}
				if calls == 1 && secondEdit {
					return os.WriteFile(to, []byte("edit B"), 0600)
				}
				return nil
			}
			t.Cleanup(func() { exchangeGeneratedFiles = original })
			err := writeGeneratedFileOrMigrate(path, "old", "new", 0600)
			if err == nil || !strings.Contains(err.Error(), recovery) {
				t.Fatalf("expected actionable publication conflict: %v", err)
			}
			wantVisible := "new"
			if secondEdit {
				wantVisible = "edit B"
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != wantVisible {
				t.Fatalf("visible content=%q error=%v, want %q", got, readErr, wantVisible)
			}
			got, readErr = os.ReadFile(recovery)
			if readErr != nil || string(got) != "edit A" {
				t.Fatalf("first edit not recoverable: %q %v", got, readErr)
			}
			if calls != 1 {
				t.Fatalf("unsafe rollback attempted: %d exchanges", calls)
			}
		})
	}
}

func TestMigrationRetainsOpenEditorInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	editor, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	original := exchangeGeneratedFiles
	var recovery string
	exchangeGeneratedFiles = func(from, to string) error { recovery = from; return exchangeGeneratedFile(from, to) }
	t.Cleanup(func() { exchangeGeneratedFiles = original })
	if err := writeGeneratedFileOrMigrate(path, "old", "new", 0600); err != nil {
		t.Fatal(err)
	}
	if err := editor.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.WriteAt([]byte("late editor contents"), 0); err != nil {
		t.Fatal(err)
	}
	if err := editor.Sync(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(recovery)
	if err != nil || string(got) != "late editor contents" {
		t.Fatalf("open editor writes lost: %q %v", got, err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("published file changed: %q %v", got, err)
	}
}

func TestMigrationDefaultRerunPreservesCustomSymlinks(t *testing.T) {
	setupSessionXDGPathEnv(t)
	custom := filepath.Join(t.TempDir(), "custom.md")
	if err := os.WriteFile(custom, []byte("custom instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallSharedConductorInstructions(ConductorAgentClaude, custom); err != nil {
		t.Fatal(err)
	}
	if err := InstallSharedConductorInstructions(ConductorAgentClaude, ""); err != nil {
		t.Fatal(err)
	}
	name := "custom-rerun"
	if err := SetupConductorWithAgent(name, DefaultProfile, ConductorAgentClaude, true, true, "", custom, "", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetupConductorWithAgent(name, DefaultProfile, ConductorAgentClaude, true, true, "", "", "", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	shared, _ := ConductorDir()
	perName, _ := ConductorNameDir(name)
	for _, dir := range []string{shared, perName} {
		target, err := os.Readlink(filepath.Join(dir, "CLAUDE.md"))
		if err != nil || target != custom {
			t.Fatalf("custom symlink changed: %q %v", target, err)
		}
	}
	raw, err := os.ReadFile(custom)
	if err != nil || string(raw) != "custom instructions" {
		t.Fatal("custom target modified")
	}
	dangling := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink("missing", dangling); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFileOrMigrate(dangling, "old", "new", 0600); err != nil {
		t.Fatalf("dangling user symlink must be preserved: %v", err)
	}
}
