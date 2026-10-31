package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conductorSessionsRowBefore2972 is the /sessions quick-command row as the
// shared template shipped it before issue #2972: no word about archived rows.
const conductorSessionsRowBefore2972 = "| `/sessions` | Run `agent-deck -p <PROFILE> list --json` and list active sessions with status |"

// Issue #2972 (3): `list --json` keeps archived sessions with
// "archived": true, so the shared template's /sessions guidance must tell
// the conductor to skip them (as the bridge's sessions command does since
// #2945), and a shared file written before that migrates in place.
func TestConductorSharedTemplate_SessionsSkipsArchivedRows(t *testing.T) {
	for _, agent := range []string{ConductorAgentClaude, ConductorAgentCodex, ConductorAgentHermes} {
		t.Run(agent, func(t *testing.T) {
			spec, err := GetConductorAgentSpec(agent)
			if err != nil {
				t.Fatal(err)
			}
			current := renderConductorInstructionsTemplate(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
			if strings.Contains(current, conductorSessionsRowBefore2972) {
				t.Fatal("the /sessions row still lists every list --json row, archived ones included")
			}
			var row string
			for _, line := range strings.Split(current, "\n") {
				if strings.HasPrefix(line, "| `/sessions` |") {
					row = line
				}
			}
			if !strings.Contains(row, "`\"archived\": true`") || !strings.Contains(row, "skip") {
				t.Fatalf("the /sessions row does not say to skip archived rows: %q", row)
			}

			// The shared file the previous release wrote is recognised as
			// generated and migrated, not left behind as user-edited.
			before := strings.Replace(current, row, conductorSessionsRowBefore2972, 1)
			gens := renderConductorInstructionsGenerations(conductorSharedClaudeMDTemplate, "", DefaultProfile, spec)
			if len(gens) == 0 || gens[0] != before {
				t.Fatal("the pre-#2972 shared file is not the newest reconstructed generation")
			}
			path := filepath.Join(t.TempDir(), spec.InstructionsFileName)
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
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
				t.Fatal("a pre-#2972 shared file was not migrated")
			}
		})
	}
}
