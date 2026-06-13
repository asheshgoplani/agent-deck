package session

import (
	"os"
	"path/filepath"
	"testing"
)

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
