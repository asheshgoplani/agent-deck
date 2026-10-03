package session

import (
	"os"
	"path/filepath"
	"testing"
)

// staleInstructionsBackups lists the <file>.bak-* siblings of file in dir.
func staleInstructionsBackups(t *testing.T, dir, file string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, file+".bak-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// Issue #2435: switching a conductor's agent must never destroy a
// hand-edited instructions file left by the previous agent. A diverged file is
// moved aside to <file>.bak-<timestamp> with its content intact.
func TestSetupConductorWithAgent_SwitchKeepsHandEditedInstructions_Issue2435(t *testing.T) {
	cases := []struct {
		from, to string
		file     string
	}{
		{ConductorAgentClaude, ConductorAgentPi, "CLAUDE.md"},
		{ConductorAgentPi, ConductorAgentClaude, "AGENTS.md"},
		{ConductorAgentCodex, ConductorAgentHermes, "AGENTS.md"},
		{ConductorAgentHermes, ConductorAgentCodex, "HERMES.md"},
	}
	for _, tc := range cases {
		t.Run(tc.from+"_to_"+tc.to, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			name := "edited-" + tc.from
			if err := SetupConductorWithAgent(name, "default", tc.from, true, true, "", "", "", "", nil, ""); err != nil {
				t.Fatalf("setup %s: %v", tc.from, err)
			}
			dir, _ := ConductorNameDir(name)
			path := filepath.Join(dir, tc.file)
			generated, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read generated %s: %v", tc.file, err)
			}
			edited := string(generated) + "\n## My role\n\nHand-written notes that must survive.\n"
			if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := SetupConductorWithAgent(name, "default", tc.to, true, true, "", "", "", "", nil, ""); err != nil {
				t.Fatalf("switch %s -> %s: %v", tc.from, tc.to, err)
			}

			backups := staleInstructionsBackups(t, dir, tc.file)
			if len(backups) != 1 {
				t.Fatalf("want exactly one %s.bak-* after switching away from a hand-edited file, got %v", tc.file, backups)
			}
			saved, err := os.ReadFile(backups[0])
			if err != nil {
				t.Fatalf("read backup: %v", err)
			}
			if string(saved) != edited {
				t.Errorf("backup %s does not hold the hand-edited content", filepath.Base(backups[0]))
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Errorf("%s should be moved aside, not left for the new %s agent (err=%v)", tc.file, tc.to, err)
			}
		})
	}
}

// Issue #2435 companion: an untouched generated instructions file (current or
// an older template generation) is still removed on an agent switch, and no
// backup is made for it. This is the pre-existing behaviour.
func TestSetupConductorWithAgent_SwitchRemovesUntouchedInstructions_Issue2435(t *testing.T) {
	cases := []struct {
		from, to string
		file     string
	}{
		{ConductorAgentClaude, ConductorAgentCodex, "CLAUDE.md"},
		{ConductorAgentPi, ConductorAgentClaude, "AGENTS.md"},
		{ConductorAgentHermes, ConductorAgentPi, "HERMES.md"},
	}
	for _, tc := range cases {
		for _, olderGeneration := range []bool{false, true} {
			label := tc.from + "_to_" + tc.to
			if olderGeneration {
				label += "_older_generation"
			}
			t.Run(label, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				name := "untouched-" + tc.from
				if err := SetupConductorWithAgent(name, "default", tc.from, true, true, "", "", "", "", nil, ""); err != nil {
					t.Fatalf("setup %s: %v", tc.from, err)
				}
				dir, _ := ConductorNameDir(name)
				path := filepath.Join(dir, tc.file)
				if olderGeneration {
					spec, _ := GetConductorAgentSpec(tc.from)
					gens := renderConductorInstructionsGenerations(conductorPerNameTemplateFor(tc.from), name, DefaultProfile, spec)
					if err := os.WriteFile(path, []byte(gens[0]), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				if err := SetupConductorWithAgent(name, "default", tc.to, true, true, "", "", "", "", nil, ""); err != nil {
					t.Fatalf("switch %s -> %s: %v", tc.from, tc.to, err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("untouched generated %s should be removed on switch (err=%v)", tc.file, err)
				}
				if backups := staleInstructionsBackups(t, dir, tc.file); len(backups) != 0 {
					t.Errorf("untouched generated %s should not be backed up, got %v", tc.file, backups)
				}
			})
		}
	}
}

// A previous agent's instructions file that is a symlink (set via
// --instructions-md) points at the user's own file elsewhere; dropping the
// link on a switch must never touch the target.
func TestSetupConductorWithAgent_SwitchLeavesSymlinkTargetIntact_Issue2435(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	custom := filepath.Join(t.TempDir(), "my-rules.md")
	const body = "# my own rules\n"
	if err := os.WriteFile(custom, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "linked"
	if err := SetupConductorWithAgent(name, "default", ConductorAgentClaude, true, true, "", custom, "", "", nil, ""); err != nil {
		t.Fatalf("setup claude with custom instructions: %v", err)
	}
	if err := SetupConductorWithAgent(name, "default", ConductorAgentCodex, true, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("switch to codex: %v", err)
	}
	got, err := os.ReadFile(custom)
	if err != nil || string(got) != body {
		t.Fatalf("symlink target changed or removed: %q, %v", got, err)
	}
	dir, _ := ConductorNameDir(name)
	if backups := staleInstructionsBackups(t, dir, "CLAUDE.md"); len(backups) != 0 {
		t.Errorf("a symlinked CLAUDE.md should not be backed up, got %v", backups)
	}
}
