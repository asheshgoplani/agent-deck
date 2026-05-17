// Issue #925 — expose the resolved-account / intended-config-dir hint to
// the claude subprocess via dedicated env vars so statusline scripts,
// custom prompts, telemetry and hooks can label/route on the user's
// intent rather than on agent-deck's worker-scratch implementation
// detail.
//
// Bug reporter: @bautrey. The user-visible problem is that
// `CLAUDE_CONFIG_DIR` in the spawn env may carry a worker-scratch path
// (issue #59 / #949) which is opaque to consumers; meanwhile the
// per-group `[groups.X.claude] config_dir` (or conductor / profile /
// global / default) that the user *intended* is invisible. This test
// locks down that buildClaudeCommand emits three new env vars carrying
// the resolved values, and that the resolved-config-dir hint reflects
// the priority-chain output (not the scratch override).
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpawnEnv_ExposesResolvedConfigDirHint_RegressionFor925 locks the
// spawn-env contract requested in issue #925:
//
//	AGENTDECK_RESOLVED_CONFIG_DIR=<resolved path>
//	AGENTDECK_RESOLVED_GROUP=<group path>
//	AGENTDECK_RESOLVED_SOURCE=<env|conductor|group|profile|global|default>
//
// Critical invariant: the RESOLVED_CONFIG_DIR carries the priority-chain
// output (what the user configured) — NOT the worker-scratch override.
// `CLAUDE_CONFIG_DIR` may be swapped to scratch (#922/#949), but the
// hint must remain stable so consumers can identify the intended account.
func TestSpawnEnv_ExposesResolvedConfigDirHint_RegressionFor925(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Intended profile dir resolved via the env-level priority chain.
	profile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	inst := &Instance{
		ID:          "00000000-0000-0000-0000-000000000925",
		Tool:        "claude",
		Title:       "issue-925",
		ProjectPath: filepath.Join(home, "proj"),
		GroupPath:   "projects/devops",
	}

	// Prepare worker-scratch so CLAUDE_CONFIG_DIR in spawn env will be
	// the scratch path while the RESOLVED_CONFIG_DIR hint must stay at
	// the intended profile.
	scratch, err := inst.EnsureWorkerScratchConfigDir(profile)
	if err != nil {
		t.Fatalf("setup scratch: %v", err)
	}
	if scratch == "" || scratch == profile {
		t.Fatalf("setup: expected non-empty scratch dir distinct from profile; scratch=%q profile=%q", scratch, profile)
	}
	inst.WorkerScratchConfigDir = scratch

	cmd := inst.buildClaudeCommand("claude")

	wantHint := "AGENTDECK_RESOLVED_CONFIG_DIR=" + profile
	if !strings.Contains(cmd, wantHint) {
		t.Errorf("spawn cmd must contain %q (intended resolved dir, not the scratch override);\ngot: %s", wantHint, cmd)
	}

	// The hint must NEVER carry the worker-scratch path — that would
	// make the env var useless for the "which account is this?"
	// statusline use case the feature exists to support.
	scratchHint := "AGENTDECK_RESOLVED_CONFIG_DIR=" + scratch
	if strings.Contains(cmd, scratchHint) {
		t.Errorf("AGENTDECK_RESOLVED_CONFIG_DIR must reflect the priority-chain resolved dir, not the worker-scratch path;\ngot cmd containing %q: %s", scratchHint, cmd)
	}

	wantGroup := "AGENTDECK_RESOLVED_GROUP=projects/devops"
	if !strings.Contains(cmd, wantGroup) {
		t.Errorf("spawn cmd must contain %q;\ngot: %s", wantGroup, cmd)
	}

	// With CLAUDE_CONFIG_DIR env set, the instance-chain resolver returns
	// source="env" (conductor/group beat env, but neither is configured
	// here so env wins). See resolveClaudeConfigDir.
	wantSource := "AGENTDECK_RESOLVED_SOURCE=env"
	if !strings.Contains(cmd, wantSource) {
		t.Errorf("spawn cmd must contain %q;\ngot: %s", wantSource, cmd)
	}
}
