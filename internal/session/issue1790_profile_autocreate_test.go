package session

import (
	"errors"
	"strings"
	"testing"
)

// Issue #1790: CLAUDE_CONFIG_DIR-derived profile names must never be
// silently auto-created.
//
// Before this fix, NewStorageWithProfile resolved the effective profile via
// GetEffectiveProfile — which infers a name from CLAUDE_CONFIG_DIR (e.g.
// ~/.claude-anything -> "anything") — and then unconditionally MkdirAll'd
// that profile's directory and opened/created its state.db. A user with an
// explicitly configured default profile (containing real sessions) who ran
// so much as `agent-deck ls` from a shell that happened to export
// CLAUDE_CONFIG_DIR (a common dual-account alias pattern) got a brand new,
// completely empty profile materialised with no message — reading as total
// data loss. Real repro (2026-07-28): four empty profiles auto-created 19
// seconds after a binary upgrade, one per CLAUDE_CONFIG_DIR value used in
// any shell on the machine, none of them the configured default.
//
// The fix: only a profile the user selected explicitly (-p flag or
// AGENTDECK_PROFILE) may be auto-created on first use. A profile name merely
// inferred from CLAUDE_CONFIG_DIR must already exist, or NewStorageWithProfile
// returns ErrInferredProfileNotFound with the known profile list instead of
// creating anything.

// withCleanProfileEnv clears AGENTDECK_PROFILE (TestMain sets it to "_test"
// process-wide) and CLAUDE_CONFIG_DIR so each test starts from a known,
// source-less resolution and can set up its own scenario.
func withCleanProfileEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
}

func TestNewStorageWithProfile_InferredUnknownProfile_Errors(t *testing.T) {
	withCleanProfileEnv(t)

	// ~/.claude-issue1790missing infers profile "issue1790missing", which
	// does not exist and was never created by this test.
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-issue1790missing")

	storage, err := NewStorageWithProfile("")
	if err == nil {
		if storage != nil {
			_ = storage.Close()
		}
		t.Fatal("expected an error for an inferred profile that does not exist, got nil")
	}
	if !errors.Is(err, ErrInferredProfileNotFound) {
		t.Errorf("error should wrap ErrInferredProfileNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "issue1790missing") {
		t.Errorf("error should name the inferred profile, got: %v", err)
	}

	exists, existsErr := ProfileExists("issue1790missing")
	if existsErr != nil {
		t.Fatalf("ProfileExists check failed: %v", existsErr)
	}
	if exists {
		t.Error("profile must NOT have been auto-created by the failed resolution")
	}
}

func TestNewStorageWithProfile_InferredUnknownProfile_ErrorListsKnownProfiles(t *testing.T) {
	withCleanProfileEnv(t)

	// Seed a real, known profile so the error message has something to list.
	knownProfile := "issue1790known"
	if seedStorage, seedErr := NewStorageWithProfile(knownProfile); seedErr != nil {
		t.Fatalf("failed to seed known profile: %v", seedErr)
	} else {
		_ = seedStorage.Close()
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-issue1790stillmissing")

	_, err := NewStorageWithProfile("")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), knownProfile) {
		t.Errorf("error should list the known profile %q, got: %v", knownProfile, err)
	}
}

// TestNewStorageWithProfile_InferredKnownProfile_Succeeds is the inverse:
// when the CLAUDE_CONFIG_DIR-inferred name DOES match an existing profile
// (the ordinary case for the cdw/cdp dual-account setup this inference was
// built for, #881), resolution must keep working exactly as before.
func TestNewStorageWithProfile_InferredKnownProfile_Succeeds(t *testing.T) {
	withCleanProfileEnv(t)

	profileName := "issue1790present"
	if seedStorage, seedErr := NewStorageWithProfile(profileName); seedErr != nil {
		t.Fatalf("failed to seed profile: %v", seedErr)
	} else {
		_ = seedStorage.Close()
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-"+profileName)

	storage, err := NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("expected success for an inferred profile that already exists, got: %v", err)
	}
	defer func() { _ = storage.Close() }()

	if storage.Profile() != profileName {
		t.Errorf("storage.Profile() = %q, want %q", storage.Profile(), profileName)
	}
}

// TestNewStorageWithProfile_ExplicitProfile_StillAutoCreates pins that the
// -p/--profile flag's documented "first use creates it" behavior is
// unaffected by this fix — only inference-sourced resolution is guarded.
func TestNewStorageWithProfile_ExplicitProfile_StillAutoCreates(t *testing.T) {
	withCleanProfileEnv(t)

	profileName := "issue1790explicit"
	exists, err := ProfileExists(profileName)
	if err != nil {
		t.Fatalf("ProfileExists check failed: %v", err)
	}
	if exists {
		t.Fatalf("test profile %q must not already exist", profileName)
	}

	storage, err := NewStorageWithProfile(profileName)
	if err != nil {
		t.Fatalf("explicit profile must still auto-create on first use, got error: %v", err)
	}
	defer func() { _ = storage.Close() }()

	exists, err = ProfileExists(profileName)
	if err != nil {
		t.Fatalf("ProfileExists check failed: %v", err)
	}
	if !exists {
		t.Error("explicit profile should have been auto-created")
	}
}

// TestNewStorageWithProfile_EnvProfile_StillAutoCreates pins that
// AGENTDECK_PROFILE-selected profiles (the CI / test-suite path, e.g.
// AGENTDECK_PROFILE=_test) keep auto-creating on first use — this fix only
// guards the CLAUDE_CONFIG_DIR-inference source, per the RISK note that
// AGENTDECK_PROFILE=_test must keep working.
func TestNewStorageWithProfile_EnvProfile_StillAutoCreates(t *testing.T) {
	withCleanProfileEnv(t)

	profileName := "issue1790envprofile"
	t.Setenv("AGENTDECK_PROFILE", profileName)

	exists, err := ProfileExists(profileName)
	if err != nil {
		t.Fatalf("ProfileExists check failed: %v", err)
	}
	if exists {
		t.Fatalf("test profile %q must not already exist", profileName)
	}

	storage, err := NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("AGENTDECK_PROFILE-selected profile must still auto-create on first use, got error: %v", err)
	}
	defer func() { _ = storage.Close() }()

	exists, err = ProfileExists(profileName)
	if err != nil {
		t.Fatalf("ProfileExists check failed: %v", err)
	}
	if !exists {
		t.Error("env-selected profile should have been auto-created")
	}
}

// TestResolveProfileForStorage_MatchesNewStorageWithProfile pins that
// ResolveProfileForStorage is the single source of truth NewStorageWithProfile
// defers to, and that any OTHER caller doing its own pre-resolution before
// opening storage (e.g. the web server bootstrap in cmd/agent-deck/main.go)
// gets the identical guarded behavior rather than a bare GetEffectiveProfile
// call that would silently reopen the #1790 hole via a second hop.
func TestResolveProfileForStorage_MatchesNewStorageWithProfile(t *testing.T) {
	withCleanProfileEnv(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-issue1790resolvefn")

	_, resolveErr := ResolveProfileForStorage("")
	if resolveErr == nil {
		t.Fatal("expected ResolveProfileForStorage to error for an unknown inferred profile")
	}
	if !errors.Is(resolveErr, ErrInferredProfileNotFound) {
		t.Errorf("ResolveProfileForStorage error should wrap ErrInferredProfileNotFound, got: %v", resolveErr)
	}

	// A caller that pre-resolves via ResolveProfileForStorage and then
	// passes the (never obtained, since it errored) name onward must not be
	// able to accidentally create the profile by re-deriving it a second
	// way — confirm NewStorageWithProfile("") on the same env agrees.
	storage, storageErr := NewStorageWithProfile("")
	if storageErr == nil {
		if storage != nil {
			_ = storage.Close()
		}
		t.Fatal("expected NewStorageWithProfile to also error for the same unknown inferred profile")
	}
	if !errors.Is(storageErr, ErrInferredProfileNotFound) {
		t.Errorf("NewStorageWithProfile error should wrap ErrInferredProfileNotFound, got: %v", storageErr)
	}
}

// --- getEffectiveProfileWithSource source classification ---

func TestGetEffectiveProfileWithSource_Classification(t *testing.T) {
	cases := []struct {
		name           string
		explicit       string
		envProfile     string
		claudeConfig   string
		wantSourceKind string
	}{
		{name: "explicit wins", explicit: "foo", wantSourceKind: ProfileSourceExplicit},
		{name: "env wins over inference", envProfile: "bar", claudeConfig: "/x/.claude-baz", wantSourceKind: ProfileSourceEnv},
		{name: "inferred from CLAUDE_CONFIG_DIR", claudeConfig: "/x/.claude-baz", wantSourceKind: ProfileSourceInferred},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCleanProfileEnv(t)
			if tc.envProfile != "" {
				t.Setenv("AGENTDECK_PROFILE", tc.envProfile)
			}
			if tc.claudeConfig != "" {
				t.Setenv("CLAUDE_CONFIG_DIR", tc.claudeConfig)
			}
			_, source := getEffectiveProfileWithSource(tc.explicit)
			if source != tc.wantSourceKind {
				t.Errorf("source = %q, want %q", source, tc.wantSourceKind)
			}
		})
	}
}
