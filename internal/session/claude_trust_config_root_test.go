package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A Claude session on an explicit config root runs with
// CLAUDE_CONFIG_DIR=<dir> and reads folder trust from <dir>/.claude.json
// (#2673). Loadout and conductor setup seeded only ~/.claude.json, so the
// plugins stayed untrusted and a conductor stalled on the trust prompt.

func TestLoadoutTrustSeed_ExplicitConfigRoot(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
skills = ["store/alpha"]
config_dir = "~/acct"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, "data"))
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	real, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	if !claudeTrustAccepted(t, filepath.Join(tmpHome, "acct", ".claude.json"), real) {
		t.Errorf("trust for %q not seeded in the session's config root acct/.claude.json", real)
	}
	if !claudeTrustAccepted(t, filepath.Join(tmpHome, ".claude.json"), real) {
		t.Errorf("trust for %q no longer seeded in ~/.claude.json", real)
	}
}

func TestConductorSetupTrustSeed_ExplicitConfigRoot(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[conductors.c1.claude]
config_dir = "~/acct"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, "data"))

	if err := SetupConductorWithAgent("c1", DefaultProfile, ConductorAgentClaude, true, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductorWithAgent: %v", err)
	}
	dir, err := ConductorNameDir("c1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if !claudeTrustAccepted(t, filepath.Join(tmpHome, "acct", ".claude.json"), dir) {
		t.Errorf("trust for conductor dir %q not seeded in its config root acct/.claude.json", dir)
	}
	if !claudeTrustAccepted(t, filepath.Join(tmpHome, ".claude.json"), dir) {
		t.Errorf("trust for conductor dir %q no longer seeded in ~/.claude.json", dir)
	}
}
