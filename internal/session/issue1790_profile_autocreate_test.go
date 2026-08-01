package session

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1790: CLAUDE_CONFIG_DIR-derived profile names must never be
// silently auto-created.
//
// Before this fix, NewStorageWithProfile resolved the effective profile via
// GetEffectiveProfile — which infers a name from CLAUDE_CONFIG_DIR (e.g.
// ~/.claude-work -> "work") — and then unconditionally MkdirAll'd
// that profile's directory and opened/created its state.db. A user with an
// explicitly configured default profile (containing real sessions) who ran
// so much as `agent-deck ls` from a shell that happened to export
// CLAUDE_CONFIG_DIR (a common dual-account alias pattern) got a brand new,
// completely empty profile materialised with no message — reading as total
// data loss. Real repro (2026-07-28): four empty profiles auto-created 19
// seconds after a binary upgrade, one per CLAUDE_CONFIG_DIR value used in
// any shell on the machine, none of them the configured default.
//
// The fix (per #1790's own "Expected" spec): only a profile the user
// selected explicitly (-p flag or AGENTDECK_PROFILE) may be auto-created on
// first use. A profile name merely inferred from CLAUDE_CONFIG_DIR must
// already exist, or NewStorageWithProfile warns on stderr and falls back to
// the configured default profile instead of creating anything — it does not
// hard-error, since a hard error would make agent-deck refuse to start from
// any shell exporting an unrelated CLAUDE_CONFIG_DIR.

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it. Used to assert on the #1790 fallback warning
// without coupling tests to exact wording beyond the substrings that matter.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

// withCleanProfileEnv clears AGENTDECK_PROFILE (TestMain sets it to "_test"
// process-wide) and CLAUDE_CONFIG_DIR so each test starts from a known,
// source-less resolution and can set up its own scenario.
func withCleanProfileEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
}

func TestNewStorageWithProfile_InferredUnknownProfile_FallsBackAndWarns(t *testing.T) {
	withCleanProfileEnv(t)

	// ~/.claude-issue1790missing infers profile "issue1790missing", which
	// does not exist and was never created by this test.
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-issue1790missing")

	var storage *Storage
	var err error
	stderr := captureStderr(t, func() {
		storage, err = NewStorageWithProfile("")
	})
	if err != nil {
		t.Fatalf("expected fallback (no error) for an inferred profile that does not exist, got: %v", err)
	}
	defer func() { _ = storage.Close() }()

	if storage.Profile() == "issue1790missing" {
		t.Fatalf("storage must NOT open the unrecognized inferred profile directly, got %q", storage.Profile())
	}
	if !strings.Contains(stderr, "issue1790missing") {
		t.Errorf("fallback warning should name the inferred profile, got: %q", stderr)
	}

	exists, existsErr := ProfileExists("issue1790missing")
	if existsErr != nil {
		t.Fatalf("ProfileExists check failed: %v", existsErr)
	}
	if exists {
		t.Error("profile must NOT have been auto-created by the fallback resolution")
	}
}

func TestNewStorageWithProfile_InferredUnknownProfile_WarningListsKnownProfiles(t *testing.T) {
	withCleanProfileEnv(t)

	// Seed a real, known profile so the warning message has something to list.
	knownProfile := "issue1790known"
	if seedStorage, seedErr := NewStorageWithProfile(knownProfile); seedErr != nil {
		t.Fatalf("failed to seed known profile: %v", seedErr)
	} else {
		_ = seedStorage.Close()
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-issue1790stillmissing")

	var storage *Storage
	var err error
	stderr := captureStderr(t, func() {
		storage, err = NewStorageWithProfile("")
	})
	if err != nil {
		t.Fatalf("expected fallback (no error), got: %v", err)
	}
	defer func() { _ = storage.Close() }()

	if !strings.Contains(stderr, knownProfile) {
		t.Errorf("warning should list the known profile %q, got: %q", knownProfile, stderr)
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

	resolved, resolveErr := ResolveProfileForStorage("")
	if resolveErr != nil {
		t.Fatalf("expected ResolveProfileForStorage to fall back (no error) for an unknown inferred profile, got: %v", resolveErr)
	}
	if resolved == "issue1790resolvefn" {
		t.Fatalf("ResolveProfileForStorage must not return the unrecognized inferred name directly, got %q", resolved)
	}

	// A caller that pre-resolves via ResolveProfileForStorage and then
	// passes the resulting (already-safe fallback) name onward must agree
	// with what NewStorageWithProfile("") on the same env would open.
	storage, storageErr := NewStorageWithProfile("")
	if storageErr != nil {
		t.Fatalf("expected NewStorageWithProfile to also fall back (no error) for the same unknown inferred profile, got: %v", storageErr)
	}
	defer func() { _ = storage.Close() }()

	if storage.Profile() != resolved {
		t.Errorf("NewStorageWithProfile opened %q, ResolveProfileForStorage resolved %q; must match", storage.Profile(), resolved)
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

// TestResolveProfileForStorage_ExistsCheckFailure_FailsClosed pins F8: a
// genuine ProfileExists I/O error (as opposed to "does not exist") must
// propagate as an error rather than silently falling back to
// auto-create/open behavior. Simulated via an unwritable GetProfileDir root
// so os.Stat inside ProfileExists returns a non-NotExist error.
func TestResolveProfileForStorage_ExistsCheckFailure_FailsClosed(t *testing.T) {
	withCleanProfileEnv(t)
	profileName := "issue1790existserr"
	t.Setenv("CLAUDE_CONFIG_DIR", "/home/u/.claude-"+profileName)

	profileDir, err := GetProfileDir(profileName)
	if err != nil {
		t.Fatalf("GetProfileDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(profileDir), 0700); err != nil {
		t.Fatalf("MkdirAll parent: %v", err)
	}
	// Make the profile's own directory path a regular file so os.Stat on
	// <profileDir>/state.db fails with ENOTDIR, not "not exist" — a genuine
	// I/O error ProfileExists must propagate rather than swallow.
	if err := os.WriteFile(profileDir, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(profileDir) })

	if _, err := ResolveProfileForStorage(""); err == nil {
		t.Fatal("expected ResolveProfileForStorage to fail closed on a ProfileExists I/O error, got nil")
	}
}
