package session

import (
	"errors"
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
	originalRename := renameGeneratedFile
	renameGeneratedFile = func(string, string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { renameGeneratedFile = originalRename })
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
	originalRename := renameGeneratedFile
	ready := make(chan struct{})
	release := make(chan struct{})
	renameGeneratedFile = func(from, to string) error {
		close(ready)
		<-release
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameGeneratedFile = originalRename })
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

func priorGeneratedInstructions(template string) string {
	template = strings.Replace(template,
		`| `+"`"+`agent-deck -p <PROFILE> status --json`+"`"+` | **Always triage with this compact count summary first:** `+"`"+`{"waiting": N, "running": N, "idle": N, "error": N, "stopped": N, "total": N}`+"`"+` |`,
		`| `+"`"+`agent-deck -p <PROFILE> status --json`+"`"+` | Get counts: `+"`"+`{"waiting": N, "running": N, "idle": N, "error": N, "stopped": N, "total": N}`+"`"+` |`, 1)
	template = strings.Replace(template,
		`| `+"`"+`agent-deck -p <PROFILE> list --json`+"`"+` | Expensive full inventory; use only when the user explicitly needs details for every profile session, never for status triage or polling |`,
		`| `+"`"+`agent-deck -p <PROFILE> list --json`+"`"+` | List all sessions with details (id, title, path, tool, status, group) |`, 1)
	template = strings.Replace(template,
		`| `+"`"+`agent-deck -p <PROFILE> session children --follow --until-done`+"`"+` | Block in one shell call while children run; emits every waiting/error transition and exits when all children are terminal |\n`, "", 1)
	template = strings.Replace(template,
		`For child work still in flight, wait with one blocking `+"`"+`agent-deck -p <PROFILE> session children --follow --until-done`+"`"+` call. Do not spend turns repeatedly calling `+"`"+`list --json`+"`"+` or `+"`"+`session children --json`+"`"+`.\n\n`, "", 1)
	template = strings.ReplaceAll(template,
		`4. Only if the compact counts require action, inspect the affected child through `+"`"+`session children`+"`"+`/`+"`"+`session show`+"`"+`; never use `+"`"+`list --json`+"`"+` for triage`,
		`4. Run `+"`"+`agent-deck -p {PROFILE} list --json`+"`"+` to know what sessions exist`)
	return template
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
			oldShared := renderConductorInstructionsTemplate(priorGeneratedInstructions(conductorSharedClaudeMDTemplate), "", DefaultProfile, spec)
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
			oldPerName := renderConductorInstructionsTemplate(priorGeneratedInstructions(perNameTemplate), name, DefaultProfile, spec)
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
