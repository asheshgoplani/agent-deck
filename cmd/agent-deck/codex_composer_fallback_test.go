package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestCodexComposerFallbackAllowed: the composer fallback never bypasses the
// double-send refusal. It needs the explicit flag, is never used for a
// structured --json --wait, and only covers a provably missing identity.
func TestCodexComposerFallbackAllowed(t *testing.T) {
	contested := []error{
		errors.New("unresolved Codex submission for 019a; refusing a second send"),
		errors.New("Codex submission marker ownership does not match this instance"),
		errors.New("live Codex session identity is already owned by another session"),
		errors.New("Codex acceptance lock: timeout after 30s"),
		errors.New("exact rollout is unavailable for remote or sandboxed sessions"),
	}
	for _, err := range contested {
		if codexComposerFallbackAllowed(err, true, false) {
			t.Errorf("fallback allowed for %q", err)
		}
	}
	for _, err := range []error{errCodexIdentityUnavailable, errCodexGenerationUnavailable, fmt.Errorf("guard: %w", errCodexIdentityUnavailable)} {
		if codexComposerFallbackAllowed(err, false, false) {
			t.Errorf("fallback without the flag for %q", err)
		}
		if codexComposerFallbackAllowed(err, true, true) {
			t.Errorf("fallback for a structured --json --wait on %q", err)
		}
		if !codexComposerFallbackAllowed(err, true, false) {
			t.Errorf("flagged fallback refused for %q", err)
		}
	}
}
