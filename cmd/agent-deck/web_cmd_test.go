package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// noopMutator implements web.SessionMutator with stubs that never run. It's
// only used to verify that buildWebServer wires whatever is passed in.
type noopMutator struct{}

func (noopMutator) CreateSession(string, string, string, string) (string, error) {
	return "", nil
}
func (noopMutator) StartSession(string) error            { return nil }
func (noopMutator) StopSession(string) error             { return nil }
func (noopMutator) RestartSession(string) error          { return nil }
func (noopMutator) DeleteSession(string) error           { return nil }
func (noopMutator) ForkSession(string) (string, error)   { return "", nil }
func (noopMutator) CreateGroup(string, string) (string, error) {
	return "", nil
}
func (noopMutator) RenameGroup(string, string) error { return nil }
func (noopMutator) DeleteGroup(string) error         { return nil }

// Compile-time guard that ui.WebMutator continues to satisfy
// web.SessionMutator. Catches accidental signature drift between the two
// packages — the kind of break that would otherwise only surface at runtime.
var _ web.SessionMutator = (*ui.WebMutator)(nil)

// withTempHomeAndConfig is the same fixture used by internal/session tests:
// point HOME at a temp dir, optionally write config.toml, and clear the
// session.LoadUserConfig cache. Restores HOME and clears the cache on cleanup.
func withTempHomeAndConfig(t *testing.T, contents string) {
	t.Helper()
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	t.Cleanup(func() {
		os.Setenv("HOME", originalHome)
		session.ClearUserConfigCache()
	})
	session.ClearUserConfigCache()

	if contents != "" {
		agentDeckDir := filepath.Join(tempDir, ".agent-deck")
		if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
}

func TestResolveMutations_DefaultsToTrue(t *testing.T) {
	withTempHomeAndConfig(t, "")
	if !resolveMutationsEnabled(false) {
		t.Errorf("resolveMutationsEnabled(false) = false, want true with empty config")
	}
}

func TestResolveMutations_TOMLDisables(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = false
`)
	if resolveMutationsEnabled(false) {
		t.Errorf("resolveMutationsEnabled(false) = true, want false when TOML disables mutations")
	}
}

func TestResolveMutations_ReadOnlyForcesOff(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = true
`)
	if resolveMutationsEnabled(true) {
		t.Errorf("resolveMutationsEnabled(true) = true, want false (--read-only must override TOML true)")
	}
}

func TestResolveMutations_ReadOnlyAndTOMLFalse(t *testing.T) {
	withTempHomeAndConfig(t, `
[web]
mutations_enabled = false
`)
	if resolveMutationsEnabled(true) {
		t.Errorf("resolveMutationsEnabled(true) = true, want false when both are off")
	}
}

// TestBuildWebServer_WiresMutator is a regression guard for the bootstrap-side
// of the v1.7.71 "web mutations are disabled / mutations not available" bug.
//
// Prior to the fix, cmd/agent-deck/main.go built the web server but never
// called server.SetMutator. The unit-test suite passed because every handler
// test injects a fakeMutator directly; the integration was never exercised.
// At runtime every POST/PATCH/DELETE returned 503 NOT_IMPLEMENTED.
//
// This test locks the contract: buildWebServer MUST forward whatever
// mutator the caller passes to the returned Server, so that deleting the
// SetMutator call in main.go fails CI rather than silently shipping.
func TestBuildWebServer_WiresMutator(t *testing.T) {
	withTempHomeAndConfig(t, "")

	server, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("buildWebServer: %v", err)
	}
	if !server.HasMutator() {
		t.Fatal("buildWebServer returned a Server with no mutator wired — main.go's POST/PATCH/DELETE handlers will 503 NOT_IMPLEMENTED at runtime")
	}
}

// TestBuildWebServer_NilMutator_StaysUnwired verifies the test-only escape
// hatch: passing nil leaves HasMutator() false. Documents that production
// callers must pass a real mutator; the nil branch exists for tests that
// don't exercise mutations.
func TestBuildWebServer_NilMutator_StaysUnwired(t *testing.T) {
	withTempHomeAndConfig(t, "")

	server, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0"}, nil, nil)
	if err != nil {
		t.Fatalf("buildWebServer: %v", err)
	}
	if server.HasMutator() {
		t.Fatal("buildWebServer wired a mutator when nil was passed — nil should be a no-op for the test escape hatch")
	}
}
