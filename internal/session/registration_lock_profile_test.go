package session

import (
	"path/filepath"
	"testing"
)

// sandboxRegistrationLocks points the locks dir and HOME (config.json) at
// fresh temp dirs so registrationLockPath touches nothing real.
func sandboxRegistrationLocks(t *testing.T) {
	t.Helper()
	withCleanProfileEnv(t)
	t.Setenv("HOME", t.TempDir())
	prev := agentDeckDirOverride
	agentDeckDirOverride = t.TempDir()
	t.Cleanup(func() { agentDeckDirOverride = prev })
}

func mustRegistrationLockPath(t *testing.T, profile string) string {
	t.Helper()
	p, err := registrationLockPath(profile)
	if err != nil {
		t.Fatalf("registrationLockPath(%q): %v", profile, err)
	}
	return filepath.Base(p)
}

// The registration lock must be keyed by the profile whose state.db the
// caller writes, not the raw -p string: `add` with no -p and `-p <same>` must
// exclude each other.
func TestRegistrationLockPath_EnvProfileMatchesExplicit(t *testing.T) {
	sandboxRegistrationLocks(t)
	t.Setenv("AGENTDECK_PROFILE", "work")

	if a, b := mustRegistrationLockPath(t, ""), mustRegistrationLockPath(t, "work"); a != b {
		t.Fatalf("AGENTDECK_PROFILE=work: lock for no -p = %q, for -p work = %q; want the same file", a, b)
	}
}

func TestRegistrationLockPath_NoProfileMatchesDefault(t *testing.T) {
	sandboxRegistrationLocks(t)

	if a, b := mustRegistrationLockPath(t, ""), mustRegistrationLockPath(t, DefaultProfile); a != b {
		t.Fatalf("no profile set: lock for no -p = %q, for -p default = %q; want the same file", a, b)
	}
}

func TestRegistrationLockPath_ConfigDefaultProfileMatchesExplicit(t *testing.T) {
	sandboxRegistrationLocks(t)
	if err := SaveConfig(&Config{DefaultProfile: "team", Version: 1}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	if a, b := mustRegistrationLockPath(t, ""), mustRegistrationLockPath(t, "team"); a != b {
		t.Fatalf("default_profile=team: lock for no -p = %q, for -p team = %q; want the same file", a, b)
	}
}
