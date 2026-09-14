package session

// Tests for PreviewSwitch — the read-only switch preview/capability model.
//
// Regression fixtures for:
//   - same-harness Claude→Claude: native-resume capability
//   - cross-harness Claude→Codex: transcript-tail capability
//   - unsupported combinations: Hermes source, Pi source, Codex→Claude,
//     Codex→Codex account switch, unknown target harness
//   - refusal conditions: remote source, no session ID for cross-harness,
//     unknown target account
//   - account status: configured vs unknown
//   - nil/edge cases
//
// NO live sessions are started. NO files are mutated. NO credentials
// are read. All tests run with fixture config only.

import (
	"strings"
	"testing"
)

// switchPreviewConfig returns a *UserConfig with two named Claude accounts
// and one named Codex account, for use in preview tests.
func switchPreviewConfig(t *testing.T) *UserConfig {
	t.Helper()
	home := withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"

[profiles.workcodex.codex]
config_dir = "~/.codex-work"
`)
	_ = home
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// switchPreviewClaude returns a minimal Claude instance for preview tests.
func switchPreviewClaude(t *testing.T, account string, sid string) *Instance {
	t.Helper()
	inst := &Instance{
		ID:              "preview-test",
		Title:           "preview-test",
		ProjectPath:     t.TempDir(),
		Tool:            "claude",
		Account:         account,
		ClaudeSessionID: sid,
	}
	return inst
}

// switchPreviewCodex returns a minimal Codex instance for preview tests.
func switchPreviewCodex(t *testing.T) *Instance {
	t.Helper()
	return &Instance{
		ID:             "codex-preview-test",
		Title:          "codex-preview-test",
		ProjectPath:    t.TempDir(),
		Tool:           "codex",
		CodexSessionID: "codex-session-abc",
	}
}

// ─── Same-harness Claude → Claude ────────────────────────────────────────────

// TestPreviewSwitch_ClaudeToClaudeNativeResume is the core same-harness case.
// Switching a Claude session from one named account to another must resolve as
// native-resume: the conversation file is copied byte-for-byte and the session
// resumes with `claude --resume <id>`.
func TestPreviewSwitch_ClaudeToClaudeNativeResume(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-555555555555")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "claude",
		Account: "work",
	})

	if preview.Capability != CapabilityNativeResume {
		t.Errorf("capability = %q, want %q", preview.Capability, CapabilityNativeResume)
	}
	if preview.Refusal != nil {
		t.Errorf("unexpected refusal: %+v", preview.Refusal)
	}
	if preview.Execution != ExecutionSupported {
		t.Errorf("execution = %q, want %q", preview.Execution, ExecutionSupported)
	}
	if len(preview.Fidelity.Inclusions) == 0 {
		t.Error("native-resume fidelity must list inclusions")
	}
	// Native-resume has no fidelity loss.
	if len(preview.Fidelity.Exclusions) != 0 {
		t.Errorf("native-resume fidelity must have no exclusions, got: %v", preview.Fidelity.Exclusions)
	}
	if preview.TargetAccountStat != AccountStatusConfigured {
		t.Errorf("target account status = %q, want %q", preview.TargetAccountStat, AccountStatusConfigured)
	}
	if preview.TargetAccount != "work" {
		t.Errorf("target account = %q, want \"work\"", preview.TargetAccount)
	}
}

// TestPreviewSwitch_ClaudeToClaudeDefaultTarget: omitting the harness defaults
// to the source harness (same-harness switch).
func TestPreviewSwitch_ClaudeToClaudeDefaultTarget(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-aaaaaaaaaaaa")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Account: "work",
		// Harness intentionally omitted
	})

	if preview.TargetHarness != "claude" {
		t.Errorf("target harness = %q, want \"claude\" (defaulted from source)", preview.TargetHarness)
	}
	if preview.Capability != CapabilityNativeResume {
		t.Errorf("capability = %q, want %q", preview.Capability, CapabilityNativeResume)
	}
	if preview.Refusal != nil {
		t.Errorf("unexpected refusal: %+v", preview.Refusal)
	}
}

// TestPreviewSwitch_ClaudeToClaudeFreshSession: a Claude session with no
// conversation on disk yet must still get CapabilityNativeResume — a fresh
// session has nothing to migrate, but the switch is valid.
func TestPreviewSwitch_ClaudeToClaudeFreshSession(t *testing.T) {
	cfg := switchPreviewConfig(t)
	// No SID, no conversation on disk.
	inst := switchPreviewClaude(t, "personal", "")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Account: "work",
	})

	if preview.Capability != CapabilityNativeResume {
		t.Errorf("capability = %q, want %q", preview.Capability, CapabilityNativeResume)
	}
	// No refusal for fresh native-resume switch.
	if preview.Refusal != nil {
		t.Errorf("fresh native-resume must not be refused, got: %+v", preview.Refusal)
	}
}

// ─── Cross-harness Claude → Codex ────────────────────────────────────────────

// TestPreviewSwitch_ClaudeToCodexTranscriptTail is the cross-harness handoff
// case. Claude→Codex must produce transcript-tail capability with no refusal
// when a source session ID is known.
func TestPreviewSwitch_ClaudeToCodexTranscriptTail(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-555555555555")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "codex",
	})

	if preview.Capability != CapabilityTranscriptTail {
		t.Errorf("capability = %q, want %q", preview.Capability, CapabilityTranscriptTail)
	}
	if preview.Refusal != nil {
		t.Errorf("unexpected refusal: %+v", preview.Refusal)
	}
	if preview.Execution != ExecutionPlanned {
		t.Errorf("execution = %q, want %q", preview.Execution, ExecutionPlanned)
	}
	// Transcript-tail must list exclusions (honest fidelity disclosure).
	if len(preview.Fidelity.Exclusions) == 0 {
		t.Error("transcript-tail fidelity must list exclusions (fidelity loss)")
	}
	// Cross-harness plans must disclose that native state and attachments are
	// not transferred, rather than claiming full continuity.
	excludedText := strings.Join(preview.Fidelity.Exclusions, " ")
	if !strings.Contains(excludedText, "native") || !strings.Contains(excludedText, "attachment") {
		t.Errorf("transcript-tail exclusions must mention native state and attachments, got: %v", preview.Fidelity.Exclusions)
	}
}

// TestPreviewSwitch_ClaudeToCodexNoSessionID: Claude→Codex with no source
// session ID must be refused — there is no transcript to hand off.
func TestPreviewSwitch_ClaudeToCodexNoSessionID(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "") // no session ID

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "codex",
	})

	if preview.Capability != CapabilityTranscriptTail {
		// Capability is still transcript-tail in concept.
		t.Errorf("capability = %q, want %q", preview.Capability, CapabilityTranscriptTail)
	}
	if preview.Refusal == nil {
		t.Fatal("must be refused when source has no session ID")
	}
	if preview.Refusal.Code != "no-session-id" {
		t.Errorf("refusal code = %q, want \"no-session-id\"", preview.Refusal.Code)
	}
}

// ─── Remote session refusal ──────────────────────────────────────────────────

// TestPreviewSwitch_RemoteSourceRefused: an SSH-hosted session must be refused
// before any capability determination (#1851: its transcript is on the remote
// host, not locally accessible).
func TestPreviewSwitch_RemoteSourceRefused(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := &Instance{
		ID:              "ssh-session",
		Title:           "remote-work",
		ProjectPath:     "/remote/placeholder/path",
		Tool:            "claude",
		Account:         "personal",
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
		SSHHost:         "myserver.example.com",
	}

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Account: "work",
	})

	if preview.Refusal == nil {
		t.Fatal("remote source must be refused")
	}
	if preview.Refusal.Code != "remote" {
		t.Errorf("refusal code = %q, want \"remote\"", preview.Refusal.Code)
	}
	if preview.Capability != CapabilityUnsupported {
		t.Errorf("capability = %q, want CapabilityUnsupported for remote", preview.Capability)
	}
	if preview.SourceIsRemote != true {
		t.Error("SourceIsRemote must be true for SSH sessions")
	}
	// The error message must name the host.
	if !strings.Contains(preview.Refusal.Message, "myserver.example.com") {
		t.Errorf("refusal message must name the SSH host, got: %s", preview.Refusal.Message)
	}
}

// ─── Unknown target account refusal ──────────────────────────────────────────

// TestPreviewSwitch_UnknownTargetAccount: a named account that has no
// config.toml block must be refused with the list of configured accounts.
func TestPreviewSwitch_UnknownTargetAccount(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-555555555555")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Account: "nonexistent-account",
	})

	if preview.Refusal == nil {
		t.Fatal("unknown account must be refused")
	}
	if preview.Refusal.Code != "unknown-account" {
		t.Errorf("refusal code = %q, want \"unknown-account\"", preview.Refusal.Code)
	}
	// Refusal message must list the accounts that DO exist.
	for _, want := range []string{"work", "personal"} {
		if !strings.Contains(preview.Refusal.Message, want) {
			t.Errorf("refusal message must list configured account %q, got: %s", want, preview.Refusal.Message)
		}
	}
	if preview.TargetAccountStat != AccountStatusUnknown {
		t.Errorf("TargetAccountStat = %q, want AccountStatusUnknown", preview.TargetAccountStat)
	}
}

// ─── Unsupported tool combinations ───────────────────────────────────────────

// TestPreviewSwitch_HermesSourceUnsupported: Hermes sessions have no account
// abstraction or export path — any switch target must be unsupported.
func TestPreviewSwitch_HermesSourceUnsupported(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := &Instance{
		ID:          "hermes-session",
		Title:       "hermes-session",
		ProjectPath: t.TempDir(),
		Tool:        "hermes",
	}

	for _, target := range []SwitchPreviewTarget{
		{Harness: "claude", Account: "work"},
		{Harness: "codex"},
		{Harness: "hermes"},
	} {
		preview := PreviewSwitch(cfg, inst, target)
		if preview.Capability != CapabilityUnsupported {
			t.Errorf("hermes source → %q: capability = %q, want %q",
				target.Harness, preview.Capability, CapabilityUnsupported)
		}
		if preview.Refusal == nil {
			t.Errorf("hermes source → %q: must have a refusal", target.Harness)
		}
		if preview.Refusal != nil && preview.Refusal.Code != "unsupported-tool" {
			t.Errorf("hermes source → %q: refusal code = %q, want \"unsupported-tool\"",
				target.Harness, preview.Refusal.Code)
		}
	}
}

// TestPreviewSwitch_PiSourceCrossHarnessPlan: Pi can transfer to another
// supported harness through its exact instance-scoped exporter.
func TestPreviewSwitch_PiSourceCrossHarnessPlan(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := &Instance{
		ID:          "pi-session",
		Title:       "pi-session",
		ProjectPath: t.TempDir(),
		Tool:        "pi",
	}

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "claude",
		Account: "work",
	})
	if preview.Capability != CapabilityTranscriptTail {
		t.Errorf("pi source → claude: capability = %q, want %q", preview.Capability, CapabilityTranscriptTail)
	}
	if preview.Refusal != nil {
		t.Fatalf("Pi cross-harness plan unexpectedly refused: %+v", preview.Refusal)
	}
	if preview.Execution != ExecutionPlanned {
		t.Errorf("pi source → claude: execution = %q, want %q", preview.Execution, ExecutionPlanned)
	}
	if len(preview.Warnings) == 0 {
		t.Fatal("missing Pi fixture should be disclosed as a plan warning")
	}
}

// TestPreviewSwitch_CodexToClaudePlan: reverse transfer uses Codex's exact
// rollout exporter and creates a fresh Claude target plan.
func TestPreviewSwitch_CodexToClaudePlan(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewCodex(t)

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "claude",
		Account: "work",
	})

	if preview.Capability != CapabilityTranscriptTail {
		t.Errorf("codex→claude capability = %q, want %q", preview.Capability, CapabilityTranscriptTail)
	}
	if preview.Execution != ExecutionPlanned {
		t.Errorf("codex→claude execution = %q, want %q", preview.Execution, ExecutionPlanned)
	}
	if preview.Refusal != nil {
		t.Fatalf("codex→claude unexpectedly refused: %+v", preview.Refusal)
	}
}

// TestPreviewSwitch_CodexToCodexNativeResume: Codex rollout files have an
// exact identity and Codex's native `resume <id>` path preserves continuity.
func TestPreviewSwitch_CodexToCodexNativeResume(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewCodex(t)

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "codex",
		Account: "workcodex",
	})

	if preview.Capability != CapabilityNativeResume {
		t.Errorf("codex→codex capability = %q, want %q", preview.Capability, CapabilityNativeResume)
	}
	if preview.Refusal != nil {
		t.Fatalf("codex→codex native switch refused: %s", preview.Refusal.Message)
	}
	if preview.Execution != ExecutionSupported {
		t.Errorf("codex→codex execution = %q, want %q", preview.Execution, ExecutionSupported)
	}
}

// TestPreviewSwitch_ClaudeToHermesUnsupported: Hermes is not a valid target.
func TestPreviewSwitch_ClaudeToHermesUnsupported(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-555555555555")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "hermes",
	})

	if preview.Capability != CapabilityUnsupported {
		t.Errorf("claude→hermes capability = %q, want %q", preview.Capability, CapabilityUnsupported)
	}
	if preview.Refusal == nil {
		t.Fatal("claude→hermes must be refused")
	}
	if preview.Refusal.Code != "unsupported-target" {
		t.Errorf("claude→hermes refusal code = %q, want \"unsupported-target\"", preview.Refusal.Code)
	}
}

// TestPreviewSwitch_ClaudeToPiPlan: Pi is a valid default-account target.
func TestPreviewSwitch_ClaudeToPiPlan(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "personal", "11111111-2222-3333-4444-555555555555")

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{
		Harness: "pi",
	})

	if preview.Capability != CapabilityTranscriptTail {
		t.Errorf("claude→pi capability = %q, want %q", preview.Capability, CapabilityTranscriptTail)
	}
	if preview.Execution != ExecutionPlanned {
		t.Errorf("claude→pi execution = %q, want %q", preview.Execution, ExecutionPlanned)
	}
	if preview.Refusal != nil {
		t.Fatalf("claude→pi unexpectedly refused: %+v", preview.Refusal)
	}
}

// ─── Account status ───────────────────────────────────────────────────────────

// TestPreviewSwitch_AccountStatusConfiguredVsUnknown verifies that the preview
// correctly labels accounts found in config.toml as "configured" and absent
// ones as "unknown".
func TestPreviewSwitch_AccountStatusConfiguredVsUnknown(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := switchPreviewClaude(t, "work", "11111111-2222-3333-4444-555555555555")

	// Known target account.
	pKnown := PreviewSwitch(cfg, inst, SwitchPreviewTarget{Account: "personal"})
	if pKnown.TargetAccountStat != AccountStatusConfigured {
		t.Errorf("known account: TargetAccountStat = %q, want AccountStatusConfigured", pKnown.TargetAccountStat)
	}
	if pKnown.Refusal != nil {
		t.Errorf("known account: unexpected refusal: %+v", pKnown.Refusal)
	}

	// Unknown target account.
	pUnknown := PreviewSwitch(cfg, inst, SwitchPreviewTarget{Account: "ghost-account"})
	if pUnknown.TargetAccountStat != AccountStatusUnknown {
		t.Errorf("unknown account: TargetAccountStat = %q, want AccountStatusUnknown", pUnknown.TargetAccountStat)
	}
	if pUnknown.Refusal == nil || pUnknown.Refusal.Code != "unknown-account" {
		t.Errorf("unknown account: expected unknown-account refusal, got: %v", pUnknown.Refusal)
	}
}

// ─── Nil/edge cases ───────────────────────────────────────────────────────────

// TestPreviewSwitch_NilInstance must not panic and must return a usable
// SwitchPreview with a refusal.
func TestPreviewSwitch_NilInstance(t *testing.T) {
	cfg := switchPreviewConfig(t)
	preview := PreviewSwitch(cfg, nil, SwitchPreviewTarget{Account: "work"})
	if preview == nil {
		t.Fatal("PreviewSwitch(nil inst) must return a non-nil SwitchPreview")
	}
	if preview.Refusal == nil {
		t.Fatal("PreviewSwitch(nil inst) must return a refusal")
	}
	if preview.Refusal.Code != "nil-session" {
		t.Errorf("nil-instance refusal code = %q, want \"nil-session\"", preview.Refusal.Code)
	}
}

// TestPreviewSwitch_NilConfig: a nil config is handled gracefully. Switches
// to named accounts are refused (no configured accounts), but the capability
// model does not panic.
func TestPreviewSwitch_NilConfig(t *testing.T) {
	inst := &Instance{
		ID:              "no-cfg",
		Title:           "no-cfg",
		ProjectPath:     t.TempDir(),
		Tool:            "claude",
		ClaudeSessionID: "11111111-2222-3333-4444-555555555555",
	}

	// Same-harness to default account (no account specified) — should work.
	pNoAcct := PreviewSwitch(nil, inst, SwitchPreviewTarget{})
	if pNoAcct == nil {
		t.Fatal("must not return nil")
	}
	// No target account specified → default binding → configured.
	if pNoAcct.Refusal != nil && pNoAcct.Refusal.Code == "unknown-account" {
		t.Errorf("empty target account with nil config should not be refused as unknown-account: %+v", pNoAcct.Refusal)
	}

	// Switch to a named account with nil config → refused.
	pWithAcct := PreviewSwitch(nil, inst, SwitchPreviewTarget{Account: "work"})
	if pWithAcct.Refusal == nil || pWithAcct.Refusal.Code != "unknown-account" {
		t.Errorf("named account with nil config must be refused as unknown-account, got: %+v", pWithAcct.Refusal)
	}
}

// TestPreviewSwitch_FidelityLabelsAreNonEmpty verifies that all capability
// levels produce at least one fidelity label (no empty/silent descriptions).
func TestPreviewSwitch_FidelityLabelsAreNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		cap  SwitchCapability
	}{
		{"native-resume", CapabilityNativeResume},
		{"transcript-tail", CapabilityTranscriptTail},
		{"unsupported", CapabilityUnsupported},
	}
	for _, tc := range cases {
		fid := fidelityFor(tc.cap)
		total := len(fid.Inclusions) + len(fid.Exclusions)
		if total == 0 {
			t.Errorf("capability %q: fidelity has no inclusions or exclusions — must not be silent", tc.name)
		}
	}
}

// TestPreviewSwitch_SourceInfoPopulated verifies that preview fields derived
// from the source instance are correctly populated.
func TestPreviewSwitch_SourceInfoPopulated(t *testing.T) {
	cfg := switchPreviewConfig(t)
	project := t.TempDir()
	inst := &Instance{
		ID:              "pop-test",
		Title:           "my-project",
		ProjectPath:     project,
		Tool:            "claude",
		Account:         "personal",
		ClaudeSessionID: "deadbeef-dead-dead-dead-deaddeadbeef",
	}

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{Account: "work"})

	if preview.SourceTitle != "my-project" {
		t.Errorf("SourceTitle = %q, want \"my-project\"", preview.SourceTitle)
	}
	if preview.SourceTool != "claude" {
		t.Errorf("SourceTool = %q, want \"claude\"", preview.SourceTool)
	}
	if preview.SourceAccount != "personal" {
		t.Errorf("SourceAccount = %q, want \"personal\"", preview.SourceAccount)
	}
	if preview.SourceSessionID != "deadbeef-dead-dead-dead-deaddeadbeef" {
		t.Errorf("SourceSessionID = %q, want the Claude session ID", preview.SourceSessionID)
	}
	if preview.SourceProjectPath != project {
		t.Errorf("SourceProjectPath = %q, want the temp project dir", preview.SourceProjectPath)
	}
	if preview.SourceIsRemote {
		t.Error("SourceIsRemote must be false for a local session")
	}
}

// TestPreviewSwitch_SourceSessionIDFromCodexField: for a Codex session,
// SourceSessionID must come from CodexSessionID (not ClaudeSessionID).
func TestPreviewSwitch_SourceSessionIDFromCodexField(t *testing.T) {
	cfg := switchPreviewConfig(t)
	inst := &Instance{
		ID:             "codex-id-test",
		Title:          "codex-id-test",
		ProjectPath:    t.TempDir(),
		Tool:           "codex",
		CodexSessionID: "codex-thread-xyz",
	}

	preview := PreviewSwitch(cfg, inst, SwitchPreviewTarget{Harness: "claude", Account: "work"})
	// Even though it's unsupported, SourceSessionID should reflect CodexSessionID.
	if preview.SourceSessionID != "codex-thread-xyz" {
		t.Errorf("SourceSessionID = %q, want \"codex-thread-xyz\" (from CodexSessionID)", preview.SourceSessionID)
	}
}
