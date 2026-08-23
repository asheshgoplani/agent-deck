package session

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGeneratedFileOrMigrateReplacesInodeAndRejectsUnsafeTargets(t *testing.T) {
	t.Run("hard link is isolated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "managed")
		backup := filepath.Join(dir, "backup")
		if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := writeGeneratedFileOrMigrate(path, "old", "new", 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(backup)
		if string(got) != "old" {
			t.Fatalf("hard-linked backup mutated to %q", got)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %o, want 640", info.Mode().Perm())
		}
	})

	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "managed")
			if kind == "directory" {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeGeneratedFileOrMigrate(path, "old", "new", 0o644); err == nil {
				t.Fatal("unsafe target silently accepted")
			}
		})
	}
}

func TestWriteGeneratedFileOrMigratePreservesEditedAndNewerAssets(t *testing.T) {
	for _, content := range []string{"user edited", "new"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "managed")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeGeneratedFileOrMigrate(path, "old", "new", 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != content {
			t.Fatalf("content %q clobbered to %q", content, got)
		}
	}
}

func TestWriteGeneratedFileOrMigrateRollbackCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalExchange := exchangeGeneratedFiles
	exchangeGeneratedFiles = func(string, string) error { return errors.New("injected exchange failure") }
	t.Cleanup(func() { exchangeGeneratedFiles = originalExchange })
	if err := writeGeneratedFileOrMigrate(path, "old", "new", 0o644); err == nil {
		t.Fatal("expected replacement error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatalf("old complete file changed to %q on failure", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "managed" {
		t.Fatalf("temporary artifacts left after failure: %v", entries)
	}
}

func TestWriteGeneratedFileOrMigratePublishesOnlyCompleteContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	old := strings.Repeat("o", 1<<20)
	newContent := strings.Repeat("n", 1<<20)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	originalExchange := exchangeGeneratedFiles
	ready := make(chan struct{})
	release := make(chan struct{})
	exchangeGeneratedFiles = func(from, to string) error {
		close(ready)
		<-release
		return exchangeGeneratedFile(from, to)
	}
	t.Cleanup(func() { exchangeGeneratedFiles = originalExchange })
	done := make(chan error, 1)
	go func() { done <- writeGeneratedFileOrMigrate(path, old, newContent, 0o644) }()
	<-ready
	before, _ := os.ReadFile(path)
	if string(before) != old {
		t.Fatal("observer saw partial content before atomic publication")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != newContent {
		t.Fatal("observer did not see complete replacement")
	}
}

func TestWriteGeneratedFileOrMigrateRestoresEditAtPublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalExchange := exchangeGeneratedFiles
	first := true
	exchangeGeneratedFiles = func(from, to string) error {
		if first {
			first = false
			if err := os.WriteFile(to, []byte("user edited"), 0o644); err != nil {
				return err
			}
		}
		return exchangeGeneratedFile(from, to)
	}
	t.Cleanup(func() { exchangeGeneratedFiles = originalExchange })
	if err := writeGeneratedFileOrMigrate(path, "old", "new", 0o644); err == nil {
		t.Fatal("expected concurrent-edit error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user edited" {
		t.Fatalf("concurrent edit clobbered: got %q", got)
	}
}

// These fingerprints were captured from the generated assets in fffbef46^,
// the release immediately before the guidance change. Unlike reconstructing
// history by reversing current edits, they fail if production invents even one
// byte of the prior release's content.
var priorReleaseInstructionSHA256 = map[string]struct{ shared, perName string }{
	ConductorAgentClaude: {"17959a9f4cbf343fe0d7cba850c74f039ad2bbffd4e521a70631a3ebdb905f57", "d32b4fa87cc009a0b9adc033b6242beaf4a3f69f9f6d229a4e8bdf7d340881e2"},
	ConductorAgentCodex:  {"cce189efca2f2c8ccedfaee4dd74bc5669fcc85e1a4d441ed7f4e49f598bd366", "b41724c824f993f53173e1fa9b28a57fe230c8bd1f2b231a25118249fbbb7d83"},
	ConductorAgentHermes: {"2c836f29873758c1f4fb093fe1a6f4360f33f1e22eae1bad9a573bdebef884bf", "664185cb9c657fa0164bb22d31db8862435874cd1da23c707a7a6ad8553a515b"},
}

func TestGeneratedConductorInstructionsMigrateExactPriorTemplate(t *testing.T) {
	for _, agent := range []string{ConductorAgentClaude, ConductorAgentCodex, ConductorAgentHermes} {
		t.Run(agent, func(t *testing.T) {
			setupSessionXDGPathEnv(t)
			spec, err := GetConductorAgentSpec(agent)
			if err != nil {
				t.Fatal(err)
			}

			base, err := ConductorDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(base, 0o755); err != nil {
				t.Fatal(err)
			}
			sharedPath := filepath.Join(base, spec.InstructionsFileName)
			oldShared := renderConductorInstructionsTemplate(previousConductorInstructionsTemplate(conductorSharedClaudeMDTemplate), "", DefaultProfile, spec)
			if got := fmtHash(oldShared); got != priorReleaseInstructionSHA256[agent].shared {
				t.Fatalf("prior shared fixture hash = %s, want released asset %s", got, priorReleaseInstructionSHA256[agent].shared)
			}
			if err := os.WriteFile(sharedPath, []byte(oldShared), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := InstallSharedConductorInstructions(agent, ""); err != nil {
				t.Fatal(err)
			}
			shared, err := os.ReadFile(sharedPath)
			if err != nil {
				t.Fatal(err)
			}
			wantShared := renderConductorInstructionsTemplate(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
			if !matchesTemplateContent(string(shared), wantShared) {
				t.Fatal("shared generated instructions were not upgraded")
			}

			name := "upgrade-" + agent
			nameDir, err := ConductorNameDir(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(nameDir, 0o755); err != nil {
				t.Fatal(err)
			}
			perNameTemplate := conductorPerNameClaudeMDTemplate
			if agent == ConductorAgentHermes {
				perNameTemplate = conductorPerNameHermesMDTemplate
			}
			perNamePath := filepath.Join(nameDir, spec.InstructionsFileName)
			oldPerName := renderConductorInstructionsTemplate(previousConductorInstructionsTemplate(perNameTemplate), name, DefaultProfile, spec)
			if got := fmtHash(oldPerName); got != priorReleaseInstructionSHA256[agent].perName {
				t.Fatalf("prior per-name fixture hash = %s, want released asset %s", got, priorReleaseInstructionSHA256[agent].perName)
			}
			if err := os.WriteFile(perNamePath, []byte(oldPerName), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := SetupConductorWithAgent(name, DefaultProfile, agent, true, true, "", "", "", "", nil, ""); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(perNamePath)
			if err != nil {
				t.Fatal(err)
			}
			want := renderConductorInstructionsTemplate(perNameTemplate, name, DefaultProfile, spec)
			if !matchesTemplateContent(string(got), want) {
				t.Fatal("per-conductor generated instructions were not upgraded")
			}
		})
	}
}

func fmtHash(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

// TestSetupConductorWithAgent_PreservesEditsAndMetaOnRerun verifies the
// clobber-hardening: re-running setup over an existing conductor preserves an
// in-place-edited per-name CLAUDE.md and the user-state meta.json fields that
// aren't re-passed as flags.
func TestSetupConductorWithAgent_PreservesEditsAndMetaOnRerun(t *testing.T) {
	setupSessionXDGPathEnv(t)

	// First setup: rich user-state. clearOnCompact=false to exercise the
	// explicit-ClearOnCompact preservation path.
	if err := SetupConductorWithAgent(
		"alpha", "default", "claude",
		true,  // heartbeatEnabled
		false, // clearOnCompact (explicit disable)
		"first desc",
		"", "", "",
		map[string]string{"K": "V"},
		"my.env",
		7, // heartbeatIdleMinutes
	); err != nil {
		t.Fatalf("first setup: %v", err)
	}

	m1, err := LoadConductorMeta("alpha")
	if err != nil {
		t.Fatalf("LoadConductorMeta after first setup: %v", err)
	}
	firstCreatedAt := m1.CreatedAt

	// User edits the generated per-name CLAUDE.md.
	nameDir, _ := ConductorNameDir("alpha")
	claudePath := filepath.Join(nameDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("USER EDITED INSTRUCTIONS"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-run setup WITHOUT re-passing description/env/env-file/idle, and with
	// clearOnCompact=true (the default, i.e. flag not used to disable).
	if err := SetupConductorWithAgent(
		"alpha", "default", "claude",
		true, // heartbeatEnabled
		true, // clearOnCompact (default; must not wipe the prior explicit false)
		"",   // description not re-passed
		"", "", "",
		nil, // env not re-passed
		"",  // env-file not re-passed
		// heartbeatIdleMinutes not re-passed
	); err != nil {
		t.Fatalf("second setup: %v", err)
	}

	// Edited CLAUDE.md preserved.
	assertFileContains(t, claudePath, "USER EDITED INSTRUCTIONS")

	// meta.json user-state preserved.
	m2, err := LoadConductorMeta("alpha")
	if err != nil {
		t.Fatalf("LoadConductorMeta after second setup: %v", err)
	}
	if m2.Description != "first desc" {
		t.Fatalf("Description = %q, want preserved %q", m2.Description, "first desc")
	}
	if m2.Env["K"] != "V" {
		t.Fatalf("Env = %v, want preserved {K:V}", m2.Env)
	}
	if m2.EnvFile != "my.env" {
		t.Fatalf("EnvFile = %q, want preserved %q", m2.EnvFile, "my.env")
	}
	if m2.HeartbeatIdleMinutes != 7 {
		t.Fatalf("HeartbeatIdleMinutes = %d, want preserved 7", m2.HeartbeatIdleMinutes)
	}
	if m2.CreatedAt != firstCreatedAt {
		t.Fatalf("CreatedAt = %q, want preserved %q", m2.CreatedAt, firstCreatedAt)
	}
	if m2.ClearOnCompact == nil || *m2.ClearOnCompact != false {
		t.Fatalf("ClearOnCompact = %v, want preserved explicit false", m2.ClearOnCompact)
	}
}

func TestInstallSharedConductorInstructions_PreservesEditedRegularFile(t *testing.T) {
	setupSessionXDGPathEnv(t)
	if err := InstallSharedConductorInstructions("claude", ""); err != nil {
		t.Fatalf("first install: %v", err)
	}
	base, _ := ConductorDir()
	p := filepath.Join(base, "CLAUDE.md")
	if err := os.WriteFile(p, []byte("EDITED SHARED INSTRUCTIONS"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallSharedConductorInstructions("claude", ""); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	assertFileContains(t, p, "EDITED SHARED INSTRUCTIONS")
}

func TestInstallPolicyMD_PreservesEditedRegularFile(t *testing.T) {
	setupSessionXDGPathEnv(t)
	if err := InstallPolicyMD(""); err != nil {
		t.Fatalf("first install: %v", err)
	}
	base, _ := ConductorDir()
	p := filepath.Join(base, "POLICY.md")
	if err := os.WriteFile(p, []byte("EDITED POLICY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InstallPolicyMD(""); err != nil {
		t.Fatalf("re-install: %v", err)
	}
	assertFileContains(t, p, "EDITED POLICY")
}
