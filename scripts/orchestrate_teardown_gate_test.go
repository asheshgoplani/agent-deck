package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The teardown gate is the run's last evidence step, so its position in the
// skill is load-bearing: after cleanup (there is nothing to prove before the
// deletions run) and before the final report (a report shipped over residue is
// the failure this gate exists to prevent).
func TestOrchestrationSkillTeardownGateOrdering(t *testing.T) {
	repoRoot := filepath.Clean("..")
	skillBytes, err := os.ReadFile(filepath.Join(repoRoot, "skills", "orchestrate", "SKILL.md"))
	if err != nil {
		t.Fatalf("read orchestration skill: %v", err)
	}
	skill := string(skillBytes)

	cleanup := strings.Index(skill, "## Cleanup (successful tasks only)")
	gate := strings.Index(skill, "## Teardown gate")
	report := strings.Index(skill, "## Final report")
	if cleanup < 0 || gate < 0 || report < 0 {
		t.Fatalf("missing sections: cleanup=%d gate=%d report=%d", cleanup, gate, report)
	}
	if !(cleanup < gate && gate < report) {
		t.Fatalf("teardown gate must sit between cleanup and the final report: cleanup=%d gate=%d report=%d", cleanup, gate, report)
	}

	section := skill[gate:report]
	for _, required := range []string{
		`bash "$RUN_DIR/teardown-gate.sh" --repo`, // the conductor runs it from the run dir
		".agent-deck/tmp/<session-id>",            // the leak no other collector sees
		".needs-attention",                        // parked runs must not fail the gate
		"VERDICT: clean",                          // the only verdict that ships a report
	} {
		if !strings.Contains(section, required) {
			t.Errorf("teardown gate section is missing %q", required)
		}
	}
	if strings.Contains(section, "rm -rf") || strings.Contains(section, "worktree remove") {
		t.Error("the teardown gate reports residue; deletion stays with the cleanup child")
	}

	// A script the run directory never receives cannot be run from it.
	setup := skill[:cleanup]
	if !strings.Contains(setup, `references/teardown-gate.sh "$RUN_DIR/"`) {
		t.Error("run setup must copy teardown-gate.sh into the run directory")
	}

	gatePath := filepath.Join(repoRoot, "skills", "orchestrate", "references", "teardown-gate.sh")
	info, err := os.Stat(gatePath)
	if err != nil {
		t.Fatalf("stat teardown-gate.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("teardown-gate.sh must be executable")
	}
}
