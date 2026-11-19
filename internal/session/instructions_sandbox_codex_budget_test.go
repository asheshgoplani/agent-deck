package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sandboxed codex merges the host's configured developer_instructions too
// (codexIdentityOverride). The fit and the final size check must measure the
// same sum, so a file reported ok never drops the whole -c override.
func TestSandboxCodexConfiguredDevInstrReportMatchesSpawn(t *testing.T) {
	home := identityTestEnv(t)
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "developer_instructions = \"" + strings.Repeat("c", 300) + "\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	inst := identityTestInstance("codex")
	inst.Command = "codex"
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "img"}
	inst.ProjectPath = filepath.Join(home, "proj")
	_ = os.MkdirAll(inst.ProjectPath, 0o755)
	if _, ok := inst.codexIdentityOverride(codexHome, inst.ProjectPath); !ok {
		t.Fatalf("precondition: the override must be delivered without files")
	}
	var bad []string
	rf := filepath.Join(home, "r.md")
	for size := 50; size <= 4000; size += 50 {
		if err := os.WriteFile(rf, []byte(strings.Repeat("r", size)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		inst.InstructionsFiles = []string{rf}
		_, st := inst.InstructionsFilesReport()
		_, ok := inst.codexIdentityOverride(codexHome, inst.ProjectPath)
		if st[0].State == InstructionsStateOK && !ok {
			bad = append(bad, fmt.Sprint(size))
		}
	}
	if len(bad) > 0 {
		t.Fatalf("file of %s B reported ok but the -c override is dropped", strings.Join(bad, "/"))
	}
}
