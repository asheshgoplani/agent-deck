package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Follow-up to #2721: only a plain `session output` counts as the conductor
// having read a child's result, so the shared template's reads (the
// command table row and /check) drop -q. The file the previous template
// wrote (no-op rule, -q reads; hash rendered from that source) is the newest
// reconstructed generation and migrates in place.
const noOpRuleQuietReadShared = "3c6262182ccb0c9a5dcbdd6177aebe60bad0d1a3bc092938daa851a9a26339cf"

func TestConductorReadsArePlainAndQuietGenerationMigrates(t *testing.T) {
	for _, agent := range []string{ConductorAgentClaude, ConductorAgentCodex, ConductorAgentHermes} {
		t.Run(agent, func(t *testing.T) {
			spec, err := GetConductorAgentSpec(agent)
			if err != nil {
				t.Fatal(err)
			}
			current := renderConductorInstructionsTemplate(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
			if strings.Contains(current, "session output <id_or_title> -q") || strings.Contains(current, "session output <name> -q") {
				t.Fatal("the conductor's reads must be plain session output, not -q")
			}
			if !strings.Contains(current, "session output <id_or_title>`") || !strings.Contains(current, "session output <name>` and summarize") {
				t.Fatal("the read row and /check must still run session output")
			}
			gens := renderConductorInstructionsGenerations(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
			// gens[0] is main before the /cmd quick command; the -q template
			// written before the plain reads is the one after it.
			if len(gens) < 2 || fmtHash(gens[1]) != noOpRuleQuietReadShared {
				t.Fatalf("second newest reconstructed generation is not the -q template that was written before")
			}
			path := filepath.Join(t.TempDir(), spec.InstructionsFileName)
			if err := os.WriteFile(path, []byte(gens[1]), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeGeneratedFileOrMigrate(path, gens, current, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !matchesTemplateContent(string(got), current) {
				t.Fatal("a -q conductor file was not migrated to the plain-read template")
			}
		})
	}
}
