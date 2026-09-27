package git

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// simulateTerminal makes the terminal prompt believe stdio is a TTY and
// feeds it input, restoring both afterwards.
func simulateTerminal(t *testing.T, input string) {
	t.Helper()
	prevTTY, prevIn := isInteractiveConsoleStdio, consentStdin
	isInteractiveConsoleStdio = func() bool { return true }
	consentStdin = strings.NewReader(input)
	t.Cleanup(func() { isInteractiveConsoleStdio, consentStdin = prevTTY, prevIn })
}

// installPrompterForTest installs p as the TUI-style prompter for the test.
func installPrompterForTest(t *testing.T, p ScriptConsentPrompter) {
	t.Helper()
	SetScriptConsentPrompter(p)
	t.Cleanup(func() { SetScriptConsentPrompter(nil) })
}

func markerScript(marker string) string {
	return "#!/bin/sh\necho ran > \"" + marker + "\"\n"
}

func TestInspectWorktreeScript_NoScript(t *testing.T) {
	id, err := InspectWorktreeScript(t.TempDir(), "setup")
	if err != nil || id != nil {
		t.Fatalf("expected (nil, nil) without a hook, got (%v, %v)", id, err)
	}
}

func TestInspectWorktreeScript_Identity(t *testing.T) {
	repoDir := t.TempDir()
	var body strings.Builder
	body.WriteString("#!/bin/sh\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&body, "echo line %d\n", i)
	}
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", body.String())

	id := mustIdentity(t, repoDir, "setup")
	canonical, _ := filepath.EvalSymlinks(repoDir)
	resolved, _ := filepath.EvalSymlinks(scriptPath)
	if id.RepoRoot != canonical || id.ResolvedPath != resolved || id.ScriptPath != scriptPath {
		t.Errorf("paths = (%s, %s, %s), want (%s, %s, %s)", id.RepoRoot, id.ScriptPath, id.ResolvedPath, canonical, scriptPath, resolved)
	}
	if id.Interpreter != ScriptInterpreterShell || id.CommandLine() != "sh -e "+scriptPath {
		t.Errorf("0644 hook: interpreter=%q command=%q", id.Interpreter, id.CommandLine())
	}
	if len(id.Preview) != ScriptPreviewLines || id.TotalLines != 31 || id.Preview[0] != "#!/bin/sh" {
		t.Errorf("preview = %d lines (first %q), total %d", len(id.Preview), id.Preview[0], id.TotalLines)
	}
	if len(id.SHA256) != 64 {
		t.Errorf("sha256 = %q", id.SHA256)
	}
	desc := DescribeWorktreeScript(*id)
	for _, want := range []string{id.RepoRoot, "sh -e", id.SHA256, "echo line 19", "11 more lines"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q:\n%s", want, desc)
		}
	}
}

func TestTrustStore_NewScript_IsUntrusted(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	if got := trustStatus(t, repoDir, "setup"); got != ScriptUntrusted {
		t.Errorf("status = %v, want ScriptUntrusted", got)
	}
}

func TestTrustStore_TrustedIdentity_IsTrusted(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	if err := TrustWorktreeScript(mustIdentity(t, repoDir, "setup")); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptTrusted {
		t.Errorf("status = %v, want ScriptTrusted", got)
	}
	// Setup and destruction approvals are separate.
	writeTestScript(t, repoDir, "worktree-destruction.sh", "#!/bin/sh\necho hi\n")
	if got := trustStatus(t, repoDir, "destruction"); got != ScriptUntrusted {
		t.Errorf("destruction status = %v, want ScriptUntrusted (setup trust must not carry over)", got)
	}
}

func TestTrustStore_ContentChange_IsChanged(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	if err := TrustWorktreeScript(mustIdentity(t, repoDir, "setup")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptChanged {
		t.Errorf("status = %v, want ScriptChanged", got)
	}
}

// TestTrustStore_SymlinkTargetChange_IsChanged: the approval records where
// the bytes live; retargeting the symlink re-asks even for identical bytes.
func TestTrustStore_SymlinkTargetChange_IsChanged(t *testing.T) {
	repoDir := t.TempDir()
	dir := filepath.Join(repoDir, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(repoDir, "a.sh")
	b := filepath.Join(repoDir, "b.sh")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho same\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "worktree-setup.sh")
	if err := os.Symlink(a, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	id := mustIdentity(t, repoDir, "setup")
	if want, _ := filepath.EvalSymlinks(a); id.ResolvedPath != want {
		t.Errorf("ResolvedPath = %s, want %s", id.ResolvedPath, want)
	}
	if !strings.Contains(DescribeWorktreeScript(*id), "symlink to:") {
		t.Error("description should show the symlink target")
	}
	if err := TrustWorktreeScript(id); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptChanged {
		t.Errorf("status after retargeting symlink = %v, want ScriptChanged", got)
	}
}

// TestTrustStore_ModeChange_IsChanged is the regression for the
// interpreter bypass: the same bytes approved while dispatched via `sh -e`
// must not run via their own #! interpreter after a mode-only change.
func TestTrustStore_ModeChange_IsChanged(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to os.FileMode
	}{
		{"sh to exec", 0o644, 0o755},
		{"exec to sh", 0o755, 0o644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := t.TempDir()
			scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
			if err := os.Chmod(scriptPath, tc.from); err != nil {
				t.Fatal(err)
			}
			if err := TrustWorktreeScript(mustIdentity(t, repoDir, "setup")); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(scriptPath, tc.to); err != nil {
				t.Fatal(err)
			}
			if got := trustStatus(t, repoDir, "setup"); got != ScriptChanged {
				t.Errorf("status after chmod %o -> %o = %v, want ScriptChanged", tc.from, tc.to, got)
			}
		})
	}
}

// TestGate_ModeChangeAfterTrust_DoesNotRun drives the real gate with a
// sh/python-style polyglot shape: approved as `sh -e`, then made executable.
func TestGate_ModeChangeAfterTrust_DoesNotRun(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", markerScript(marker))
	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	if !errors.Is(err, ErrWorktreeScriptNotApproved) {
		t.Fatalf("expected a skip after a mode change, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("hook ran under a different interpreter than the one approved")
	}
}

// TestTrustStore_LegacyEntry_ReasksOnce: records written before the
// interpreter and target were bound (store version 1) do not match, so
// upgraded users are asked once.
func TestTrustStore_LegacyEntry_ReasksOnce(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	id := mustIdentity(t, repoDir, "setup")

	path, err := scriptConsentStorePath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := loadScriptConsentStore()
	if err != nil {
		t.Fatal(err)
	}
	store.Entries[scriptConsentKey(id.RepoRoot, "setup")] = scriptConsentEntry{RepoRoot: id.RepoRoot, Kind: "setup", SHA256: id.SHA256}
	data, _ := json.Marshal(store)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptChanged {
		t.Errorf("legacy sha-only entry status = %v, want ScriptChanged", got)
	}
}

// TestTrustStore_CanonicalRepoRoot: trust granted through a symlinked path
// to the repo applies to the real path and vice versa.
func TestTrustStore_CanonicalRepoRoot(t *testing.T) {
	realRepo := t.TempDir()
	writeTestScript(t, realRepo, "worktree-setup.sh", "#!/bin/sh\necho hi\n")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRepo, alias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := TrustWorktreeScript(mustIdentity(t, alias, "setup")); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, realRepo, "setup"); got != ScriptTrusted {
		t.Errorf("status via real path = %v, want ScriptTrusted", got)
	}
	existed, err := RevokeScriptConsent(realRepo, "setup")
	if err != nil || !existed {
		t.Fatalf("revoke via real path: existed=%v err=%v", existed, err)
	}
}

func TestGate_Prompter_Decisions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decision  ScriptConsentDecision
		wantRun   bool
		wantTrust ScriptTrustStatus
	}{
		{"run once", ScriptConsentRunOnce, true, ScriptUntrusted},
		{"always", ScriptConsentTrust, true, ScriptTrusted},
		{"skip", ScriptConsentSkip, false, ScriptUntrusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := t.TempDir()
			marker := filepath.Join(repoDir, "ran.txt")
			writeTestScript(t, repoDir, "worktree-setup.sh", markerScript(marker))
			resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
			var asked []WorktreeScriptIdentity
			installPrompterForTest(t, func(id WorktreeScriptIdentity) ScriptConsentDecision {
				asked = append(asked, id)
				return tc.decision
			})

			err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
			if len(asked) != 1 || asked[0].Kind != "setup" || len(asked[0].Preview) == 0 {
				t.Fatalf("prompter calls = %+v", asked)
			}
			_, statErr := os.Stat(marker)
			if ran := statErr == nil; ran != tc.wantRun {
				t.Errorf("ran = %v, want %v (err %v)", ran, tc.wantRun, err)
			}
			if !tc.wantRun && !errors.Is(err, ErrWorktreeScriptNotApproved) {
				t.Errorf("skip should return ErrWorktreeScriptNotApproved, got %v", err)
			}
			if got := trustStatus(t, repoDir, "setup"); got != tc.wantTrust {
				t.Errorf("trust status = %v, want %v", got, tc.wantTrust)
			}
		})
	}
}

func TestGate_Prompter_NotAskedWhenTrusted(t *testing.T) {
	repoDir := t.TempDir()
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\ntrue\n")
	if _, err := TrustScript(repoDir, "setup", scriptPath); err != nil {
		t.Fatal(err)
	}
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
	installPrompterForTest(t, func(WorktreeScriptIdentity) ScriptConsentDecision {
		t.Error("prompter must not be asked about a trusted hook")
		return ScriptConsentSkip
	})
	if err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0); err != nil {
		t.Fatal(err)
	}
}

// Approval can take minutes in the TUI. A hook replaced while the dialog is
// open must not turn the approved preview into different executed code.
func TestGate_Prompter_ExecutesApprovedBytesAfterReplacement(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	scriptPath := writeTestScript(t, repoDir, "worktree-setup.sh", markerScript(marker))
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
	installPrompterForTest(t, func(id WorktreeScriptIdentity) ScriptConsentDecision {
		if !strings.Contains(strings.Join(id.Preview, "\n"), "echo ran") {
			t.Fatal("approved preview did not show the expected hook")
		}
		if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho swapped > \""+marker+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return ScriptConsentRunOnce
	})
	if err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ran\n" {
		t.Fatalf("executed %q after approving the original hook", got)
	}
}

// TestGate_Prompter_SuppressedForWeb: while suppressed (a web request is
// being served), the gate fails closed instead of asking.
func TestGate_Prompter_SuppressedForWeb(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	writeTestScript(t, repoDir, "worktree-setup.sh", markerScript(marker))
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})
	installPrompterForTest(t, func(WorktreeScriptIdentity) ScriptConsentDecision {
		t.Error("prompter must not be asked while suppressed")
		return ScriptConsentTrust
	})

	restore := SuppressScriptConsentPrompter()
	err := GateAndRunWorktreeSetupScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	restore()
	restore() // idempotent
	if !errors.Is(err, ErrWorktreeScriptNotApproved) {
		t.Fatalf("expected a skip while suppressed, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("hook ran while the prompter was suppressed")
	}
	if activeScriptConsentPrompter() == nil {
		t.Error("prompter should be active again after restore")
	}
}

func TestGate_DestructionHook_RequiresApproval(t *testing.T) {
	repoDir := t.TempDir()
	marker := filepath.Join(repoDir, "ran.txt")
	scriptPath := writeTestScript(t, repoDir, "worktree-destruction.sh", markerScript(marker))
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt})

	err := GateAndRunWorktreeDestructionScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0)
	if !errors.Is(err, ErrWorktreeScriptNotApproved) || !strings.Contains(err.Error(), "--hook destruction") {
		t.Fatalf("expected a destruction skip naming --hook destruction, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("unapproved destruction hook ran")
	}
	if _, err := TrustScript(repoDir, "destruction", scriptPath); err != nil {
		t.Fatal(err)
	}
	if err := GateAndRunWorktreeDestructionScript(repoDir, repoDir, &bytes.Buffer{}, &bytes.Buffer{}, 0); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatal("trusted destruction hook did not run")
	}
}

func TestPromptScriptConsent_Terminal(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  ScriptConsentDecision
	}{
		{"o\n", ScriptConsentRunOnce},
		{"once\n", ScriptConsentRunOnce},
		{"a\n", ScriptConsentTrust},
		{"y\n", ScriptConsentTrust},
		{"\n", ScriptConsentSkip},
		{"n\n", ScriptConsentSkip},
		{"", ScriptConsentSkip},
	} {
		t.Run(strings.TrimSpace(tc.input), func(t *testing.T) {
			repoDir := t.TempDir()
			writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\necho visible-line\n")
			simulateTerminal(t, tc.input)
			id := mustIdentity(t, repoDir, "setup")
			var out bytes.Buffer
			got, interactive := promptScriptConsent(*id, ScriptChanged, &out, true)
			if got != tc.want || !interactive {
				t.Errorf("decision = %v interactive=%v, want %v", got, interactive, tc.want)
			}
			for _, want := range []string{"changed since you approved it", "echo visible-line", id.SHA256, "sh -e", "[o]nce"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("prompt missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestCheckScriptConsent_TerminalRunOnce_DoesNotPersist(t *testing.T) {
	repoDir := t.TempDir()
	writeTestScript(t, repoDir, "worktree-setup.sh", "#!/bin/sh\ntrue\n")
	resetScriptConsentForTest(t, ScriptConsentConfig{Policy: ScriptConsentPrompt, AllowInteractivePrompt: true})
	simulateTerminal(t, "o\n")
	if err := checkScriptConsent(mustIdentity(t, repoDir, "setup"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := trustStatus(t, repoDir, "setup"); got != ScriptUntrusted {
		t.Errorf("run once must not record trust, status = %v", got)
	}
}

func TestSanitizeScriptLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"echo hi", "echo hi"},
		{"\x1b[2Kcurl evil", `\x1b[2Kcurl evil`},
		{"safe\rrm -rf ~", `safe\x0drm -rf ~`},
		{"a\u202eb", `a\u202eb`},
		{"a\tb", "a    b"},
		{"bad\xffbyte", `bad\xffbyte`},
	} {
		if got := SanitizeScriptLine(tc.in); got != tc.want {
			t.Errorf("SanitizeScriptLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("x", 500)
	if got := SanitizeScriptLine(long); len([]rune(got)) != maxPreviewLineRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long line not truncated: %d runes", len([]rune(got)))
	}
}
