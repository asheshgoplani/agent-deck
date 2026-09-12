package session

import (
	"fmt"
	"sort"
	"strings"
)

// SwitchCapability describes the kind of context continuity available when
// switching a session to a different account or harness.
//
// The capability is determined by the source tool, the target harness, and
// whether the required conversation artefacts exist on disk. It is the
// authoritative label the UI and CLI surface to the user BEFORE any mutation.
type SwitchCapability string

const (
	// CapabilityNativeResume applies to same-harness Claude → Claude switches:
	// the conversation file is copied byte-for-byte and the restarted session
	// uses `claude --resume <id>` for exact continuity. No fidelity loss.
	CapabilityNativeResume SwitchCapability = "native-resume"

	// CapabilityTranscriptTail applies to every supported cross-harness
	// direction among Claude, Codex, and Pi. The bounded exact-ID export is
	// delivered as context to a fresh target; it is never native continuity.
	CapabilityTranscriptTail SwitchCapability = "transcript-tail"

	// CapabilityUnsupported applies to Hermes, custom tools, remote sessions,
	// and any combination where no exact export path exists. Pi is supported as
	// a cross-harness source or target with its default account only.
	CapabilityUnsupported SwitchCapability = "unsupported"
)

// AccountStatus distinguishes a slot that is present in config.toml from one
// whose live authentication has been independently confirmed. The switch
// preview always reports "configured" — verifying a live OAuth token requires
// a separate outbound call that is outside this read-only path's scope.
type AccountStatus string

const (
	// AccountStatusConfigured means the account slot exists in config.toml
	// with a config_dir or codex_home binding. Authentication state unknown.
	AccountStatusConfigured AccountStatus = "configured"

	// AccountStatusUnknown means the account name was not found in any
	// configured profile. A switch to this account will be refused.
	AccountStatusUnknown AccountStatus = "unknown"
)

// SwitchFidelity describes what context is and is not preserved for a given
// SwitchCapability. Inclusions and Exclusions are human-readable labels
// suitable for display in the CLI preview and TUI confirmation dialog.
type SwitchFidelity struct {
	Inclusions []string // what IS preserved (empty for CapabilityUnsupported)
	Exclusions []string // what is NOT preserved
}

// SwitchExecution distinguishes an implemented native switch from a context
// handoff or a capability that is only planned. Preview is descriptive and
// must not claim that every capability has a mutating executor.
type SwitchExecution string

const (
	ExecutionSupported   SwitchExecution = "supported"
	ExecutionHandoffOnly SwitchExecution = "handoff-only"
	ExecutionPlanned     SwitchExecution = "planned"
	ExecutionUnsupported SwitchExecution = "unsupported"
)

// SwitchRefusal explains why a switch cannot safely proceed. A non-nil
// Refusal in SwitchPreview means the switch MUST NOT be executed — the
// preflight has identified a condition that would corrupt data or violate
// an invariant.
type SwitchRefusal struct {
	// Code is a machine-readable reason (e.g. "remote", "no-session-id").
	Code string
	// Message is a human-readable explanation.
	Message string
}

// SwitchPreviewTarget specifies the destination for a switch preview.
// Both fields are optional: omitting Harness means same harness as source;
// omitting Account means the default/unset account for the target harness.
// SwitchSourceSnapshot captures the source relationships that a cross-harness
// replacement must not silently reproduce. HasParent is informational: an
// ordinary child can transfer and retains that parent routing. HasChildren and
// managed roles are blockers because the fresh row cannot safely become their
// owner without an explicitly implemented relink protocol.
type SwitchSourceSnapshot struct {
	ParentSessionID     string
	HasParent           bool
	HasChildren         bool
	IsConductor         bool
	WatcherBridgeTarget bool
	ManagementUnknown   bool
}

// SnapshotSwitchSource builds the read-only relationship snapshot from the
// registry rows visible to the caller. watcherBridgeTarget and
// managementUnknown are supplied by the presentation layer, which owns watcher
// route configuration; session deliberately never reads watcher credentials or
// rewrites watcher routing.
func SnapshotSwitchSource(inst *Instance, instances []*Instance, watcherBridgeTarget, managementUnknown bool) SwitchSourceSnapshot {
	snapshot := SwitchSourceSnapshot{WatcherBridgeTarget: watcherBridgeTarget, ManagementUnknown: managementUnknown}
	if inst == nil {
		return snapshot
	}
	snapshot.ParentSessionID = strings.TrimSpace(inst.ParentSessionID)
	snapshot.HasParent = snapshot.ParentSessionID != ""
	snapshot.IsConductor = inst.IsConductor
	for _, candidate := range instances {
		if candidate != nil && candidate.ID != inst.ID && candidate.ParentSessionID == inst.ID {
			snapshot.HasChildren = true
			break
		}
	}
	return snapshot
}

// AuthoritativeSwitchSourceSnapshot verifies that source still names the exact
// registry row observed by an ownership authority, then derives its ownership
// state from that current row set. Callers supply watcher state only after
// reading their current watcher configuration; unreadable watcher state must
// be represented by managementUnknown so execution fails closed.
func AuthoritativeSwitchSourceSnapshot(source *Instance, instances []*Instance, watcherBridgeTarget, managementUnknown bool) (SwitchSourceSnapshot, error) {
	if source == nil {
		return SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("cross-harness source is nil")
	}
	for _, current := range instances {
		if current == nil || current.ID != source.ID {
			continue
		}
		if identityForInstance(current) != identityForInstance(source) || current.ParentSessionID != source.ParentSessionID || current.ParentProjectPath != source.ParentProjectPath {
			return SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("cross-harness source changed since it was selected")
		}
		return SnapshotSwitchSource(current, instances, watcherBridgeTarget, managementUnknown), nil
	}
	return SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("cross-harness source no longer exists in storage")
}

// SwitchPreviewTarget specifies the destination for a switch preview.
type SwitchPreviewTarget struct {
	// Harness is the target tool name: "claude", "codex", "hermes", "pi", etc.
	// Empty means same harness as the source session.
	Harness string
	// Account is the named account slot (must have a [profiles.<name>.claude]
	// or equivalent block in config.toml for tools that use named accounts).
	// Pi accepts only the empty default account; named Pi accounts are refused.
	// Empty means the default/unset account.
	Account string
}

// SwitchPreview is the read-only result of PreviewSwitch. It describes exactly
// what a switch would do — and whether it can safely proceed — without
// performing any mutation, stopping sessions, copying files, or modifying
// registry state.
//
// Consumers:
//   - CLI `session switch-preview`: surfaces Refusal, Capability, Fidelity
//   - TUI confirmation dialog: gating the Edit Session account picker commit
//   - Programmatic callers (--json): structured output for scripted workflows
type SwitchPreview struct {
	// Source fields
	SourceTitle       string // human-readable session name
	SourceTool        string // "claude", "codex", "hermes", "pi", ...
	SourceAccount     string // current account slot (empty = default/unset)
	SourceAccountDir  string // resolved config dir for the source account
	SourceAccountStat AccountStatus
	SourceSessionID   string // ClaudeSessionID or CodexSessionID
	SourceProjectPath string
	SourceIsRemote    bool // true for --ssh sessions

	// Target fields
	TargetHarness     string
	TargetAccount     string
	TargetAccountDir  string // resolved config dir for the target account, if applicable
	TargetAccountStat AccountStatus

	// Switch capability and fidelity
	Capability SwitchCapability
	Fidelity   SwitchFidelity
	Execution  SwitchExecution

	// Refusal is non-nil when the switch MUST NOT proceed.
	// The caller must surface this to the user before any mutation.
	Refusal *SwitchRefusal

	// Non-fatal notes (e.g. "conversation not yet on disk — fresh session").
	Warnings []string

	// LaunchPlan is populated for a supported cross-harness direction when the
	// exact source exporter succeeds. It is always plan-only: Executable and
	// Ready remain false until target lifecycle and identity verification exist.
	LaunchPlan *FreshTargetLaunchPlan `json:"launch_plan,omitempty"`
}

// PreviewSwitch returns a read-only description of what switching inst to
// target would do, without performing any mutation. The Refusal field is
// non-nil when the switch cannot safely proceed.
//
// cfg may be nil (treated as empty config): the preview then returns
// AccountStatusUnknown for both source and target accounts and refuses
// named-account switches that require config.
//
// This function never stops sessions, copies files, reads Claude credentials,
// or modifies registry state.
func PreviewSwitch(cfg *UserConfig, inst *Instance, target SwitchPreviewTarget) *SwitchPreview {
	return PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, target, DefaultHandoffMaxChars, nil)
}

// PreviewSwitchWithMaxBytes is the compatibility entry point for callers that
// do not have a registry snapshot. It still protects direct conductor rows;
// CLI and TUI callers should use PreviewSwitchWithMaxBytesAndSnapshot so
// dependent children and watcher-route ownership are also known.
func PreviewSwitchWithMaxBytes(cfg *UserConfig, inst *Instance, target SwitchPreviewTarget, maxBytes int) *SwitchPreview {
	return PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, target, maxBytes, nil)
}

// PreviewSwitchWithMaxBytesAndSnapshot is PreviewSwitchWithMaxBytes with the
// caller's read-only source relationship snapshot. It never writes registry,
// transcript, credential, watcher, or lifecycle state.
func PreviewSwitchWithMaxBytesAndSnapshot(cfg *UserConfig, inst *Instance, target SwitchPreviewTarget, maxBytes int, snapshot *SwitchSourceSnapshot) *SwitchPreview {
	if inst == nil {
		return &SwitchPreview{
			Capability: CapabilityUnsupported,
			Refusal: &SwitchRefusal{
				Code:    "nil-session",
				Message: "no session provided",
			},
		}
	}

	targetHarness := strings.TrimSpace(target.Harness)
	if targetHarness == "" {
		targetHarness = inst.Tool // default: same harness
	}
	targetAccount := strings.TrimSpace(target.Account)

	preview := &SwitchPreview{
		SourceTitle:       inst.Title,
		SourceTool:        inst.Tool,
		SourceAccount:     inst.Account,
		SourceSessionID:   resolveSourceSessionID(inst),
		SourceProjectPath: inst.ProjectPath,
		SourceIsRemote:    inst.IsSSH(),
		TargetHarness:     targetHarness,
		TargetAccount:     targetAccount,
	}
	if inst.Tool == "pi" && !inst.IsSSH() {
		// Pi stores the exact identity in its instance-scoped session header;
		// reading that header is deterministic and does not scan neighboring
		// sessions. Missing files remain a disclosed plan warning below.
		if piID, piErr := exactPiSessionID(inst); piErr == nil {
			preview.SourceSessionID = piID
		}
	}

	// Resolve source account status and config dir.
	if IsClaudeCompatible(inst.Tool) {
		preview.SourceAccountDir = GetClaudeConfigDirForInstance(inst)
		if inst.Account != "" {
			if cfg != nil && cfg.GetProfileClaudeConfigDir(inst.Account) != "" {
				preview.SourceAccountStat = AccountStatusConfigured
			} else {
				preview.SourceAccountStat = AccountStatusUnknown
			}
		} else {
			// Empty account = default/global; the resolved dir is still valid.
			preview.SourceAccountStat = AccountStatusConfigured
		}
	} else if inst.Tool == "pi" {
		// Pi has no named-account abstraction in this integration. Empty is
		// the factual default/unknown state, never a verified identity claim.
		preview.SourceAccountStat = AccountStatusUnknown
		preview.Warnings = append(preview.Warnings, "Pi account identity is default/unknown; only the Pi default target account is supported")
	} else {
		preview.SourceAccountStat = AccountStatusConfigured
	}

	// Resolve target account status and config dir (tool-specific).
	targetDir, targetStat := resolveTargetAccountInfo(cfg, targetHarness, targetAccount)
	preview.TargetAccountDir = targetDir
	preview.TargetAccountStat = targetStat

	// Remote sessions: refuse before anything else (#1851).
	if inst.IsSSH() {
		preview.Capability = CapabilityUnsupported
		preview.Fidelity = fidelityForUnsupported()
		preview.Refusal = &SwitchRefusal{
			Code: "remote",
			Message: fmt.Sprintf(
				"session %q runs on %s; its transcript is on the remote host and cannot be "+
					"migrated or read locally. Account switching for remote sessions is not supported.",
				inst.Title, inst.SSHHost),
		}
		return preview
	}

	// Determine capability from source tool + target harness.
	capability := determineCapability(inst.Tool, targetHarness)
	preview.Capability = capability
	preview.Fidelity = fidelityFor(capability)
	preview.Execution = executionFor(capability)

	// Unsupported: build an informative refusal message.
	if capability == CapabilityUnsupported {
		preview.Refusal = refusalForUnsupported(inst.Tool, targetHarness)
		return preview
	}

	// Pi supports only its default account. A named Pi account is a clear
	// semantic refusal, not an ordinary missing config slot.
	if targetHarness == "pi" && targetAccount != "" {
		preview.Refusal = &SwitchRefusal{
			Code:    "pi-account-unsupported",
			Message: fmt.Sprintf("Pi has no named-account configuration; target account %q is unsupported (use the Pi default)", targetAccount),
		}
		return preview
	}

	// Unknown target account: refuse before checking conversation state.
	if targetAccount != "" && targetStat == AccountStatusUnknown {
		available := ConfiguredAccountNames(cfg)
		hint := "none configured"
		if len(available) > 0 {
			hint = strings.Join(available, ", ")
		}
		preview.Refusal = &SwitchRefusal{
			Code: "unknown-account",
			Message: fmt.Sprintf(
				"account %q has no [profiles.%s.%s].config_dir in config.toml (configured accounts: %s)",
				targetAccount, targetAccount, targetHarness, hint),
		}
		return preview
	}

	// A cross-harness target is a new row. Never infer that it can inherit
	// conductor ownership, watcher bridge routing, or dependent-child delivery.
	// This guard is intentionally before source export/planning; execution repeats
	// this preflight before staging, target creation, or lifecycle work.
	if capability == CapabilityTranscriptTail {
		if refusal := refusalForCrossHarnessSource(inst, snapshot); refusal != nil {
			preview.Refusal = refusal
			return preview
		}
	}

	// Transcript-tail requires an exact source artifact. Pi's identity is read
	// from its instance-scoped session header; all other tools use their stored
	// exact ID. No newest-file fallback is allowed.
	if capability == CapabilityTranscriptTail {
		if preview.SourceSessionID == "" && inst.Tool != "pi" {
			preview.Refusal = &SwitchRefusal{
				Code: "no-session-id",
				Message: fmt.Sprintf(
					"session %q has no recorded conversation ID; cannot build a cross-harness launch plan",
					inst.Title),
			}
			return preview
		}
		if maxBytes <= 0 {
			maxBytes = DefaultHandoffMaxChars
		}
		plan, planErr := BuildFreshTargetLaunchPlanForInstance(cfg, inst, SwitchPreviewTarget{Harness: targetHarness, Account: targetAccount}, maxBytes)
		if planErr != nil {
			preview.Warnings = append(preview.Warnings, "cross-harness launch plan unavailable: "+planErr.Error())
		} else {
			preview.LaunchPlan = plan
		}
	}

	// Native-resume: note if the exact identified conversation is not on disk
	// yet (non-fatal). Do not use the migration layer's newest-file fallback in
	// a preview: preview must never imply that a neighbouring session is ours.
	if capability == CapabilityNativeResume && preview.SourceSessionID != "" {
		var artifactErr error
		if IsClaudeCompatible(inst.Tool) {
			_, artifactErr = uniqueRegularArtifact(claudeExactTranscriptCandidates(inst), preview.SourceSessionID+".jsonl")
		} else if IsCodexCompatible(inst.Tool) {
			var matches []string
			matches, artifactErr = exactCodexRolloutMatches(preview.SourceSessionID, inst.getCodexHomeDir())
			if artifactErr == nil {
				_, artifactErr = uniqueRegularArtifact(matches, "rollout for "+preview.SourceSessionID)
			}
		}
		if artifactErr != nil {
			preview.Warnings = append(preview.Warnings,
				"exact conversation artifact not found — this may be a fresh session; no neighbouring transcript was selected")
		}
	}

	return preview
}

func refusalForCrossHarnessSource(inst *Instance, supplied *SwitchSourceSnapshot) *SwitchRefusal {
	// A direct caller may not have inventory, but a conductor role on the row is
	// still known and must never be bypassed.
	snapshot := SnapshotSwitchSource(inst, nil, false, false)
	if supplied != nil {
		snapshot = *supplied
		if inst != nil && inst.IsConductor {
			snapshot.IsConductor = true
		}
	}
	switch {
	case snapshot.ManagementUnknown:
		return &SwitchRefusal{Code: "management-unknown", Message: "cannot verify watcher/service ownership for this source; cross-harness replacement is refused until routing is readable. No roles, credentials, or routes were copied."}
	case snapshot.IsConductor:
		return &SwitchRefusal{Code: "managed-conductor", Message: "this source is a managed conductor. Cross-harness replacement cannot preserve its single-consumer inbox ownership, bridge targets, or service role; use a separately configured conductor instead."}
	case snapshot.WatcherBridgeTarget:
		return &SwitchRefusal{Code: "watcher-bridge-target", Message: "this source is a watcher bridge target. Cross-harness replacement cannot relink watcher routes or preserve single-consumer ownership; configure the destination directly after upgrading and testing it."}
	case snapshot.HasChildren:
		return &SwitchRefusal{Code: "dependent-children", Message: "this source has dependent child sessions. Cross-harness replacement cannot relink their parent inbox routing; move or finish the children before switching."}
	default:
		return nil
	}
}

// resolveSourceSessionID returns the best available session identifier for
// the source instance, prioritising ClaudeSessionID then CodexSessionID.
func resolveSourceSessionID(inst *Instance) string {
	if inst == nil {
		return ""
	}
	if inst.ClaudeSessionID != "" {
		return inst.ClaudeSessionID
	}
	return inst.CodexSessionID
}

// resolveTargetAccountInfo returns the config dir and AccountStatus for the
// target (harness, account) pair. For tools that do not use named accounts
// (hermes, pi, copilot) the dir is empty and status is still reported.
// ConfiguredAccountNamesForHarness returns named slots whose binding exists
// for the selected harness. It never implies that OAuth authentication was
// verified; callers should label these slots as configured only.
func ConfiguredAccountNamesForSwitch(cfg *UserConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	for name := range cfg.Profiles {
		if cfg.GetProfileClaudeConfigDir(name) != "" || cfg.GetProfileCodexConfigDir(name) != "" {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ConfiguredAccountNamesForHarness(cfg *UserConfig, harness string) []string {
	if cfg == nil {
		return nil
	}
	var names []string
	for name := range cfg.Profiles {
		if IsClaudeCompatible(harness) {
			if cfg.GetProfileClaudeConfigDir(name) != "" {
				names = append(names, name)
			}
		} else if IsCodexCompatible(harness) {
			if cfg.GetProfileCodexConfigDir(name) != "" {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func resolveTargetAccountInfo(cfg *UserConfig, harness, account string) (dir string, status AccountStatus) {
	if account == "" {
		// Pi has no named-account semantics; even its default is not an
		// authentication assertion. Other harnesses may use their default.
		if harness == "pi" {
			return "", AccountStatusUnknown
		}
		return "", AccountStatusConfigured
	}
	switch {
	case IsClaudeCompatible(harness):
		if cfg == nil {
			return "", AccountStatusUnknown
		}
		d := cfg.GetProfileClaudeConfigDir(account)
		if d == "" {
			return "", AccountStatusUnknown
		}
		return d, AccountStatusConfigured
	case IsCodexCompatible(harness):
		if cfg == nil {
			return "", AccountStatusUnknown
		}
		// Codex uses CODEX_HOME env; GetProfileCodexConfigDir maps to that.
		d := cfg.GetProfileCodexConfigDir(account)
		if d == "" {
			return "", AccountStatusUnknown
		}
		return d, AccountStatusConfigured
	default:
		// All other tools have no named-account concept in the current schema.
		return "", AccountStatusUnknown
	}
}

// determineCapability resolves the SwitchCapability from source tool and
// target harness names.
//
// Matrix:
//
//	Claude → Claude:  native-resume  (exact copy + --resume)
//	Codex → Codex:    native-resume  (exact rollout copy + codex resume)
//	all other pairs among Claude, Codex, and Pi: transcript-tail
//	*      → *:       unsupported    (no migration path)
func determineCapability(sourceTool, targetHarness string) SwitchCapability {
	sourceClaude, sourceCodex, sourcePi := IsClaudeCompatible(sourceTool), IsCodexCompatible(sourceTool), sourceTool == "pi"
	targetClaude, targetCodex, targetPi := IsClaudeCompatible(targetHarness), IsCodexCompatible(targetHarness), targetHarness == "pi"
	switch {
	case (sourceClaude && targetClaude) || (sourceCodex && targetCodex):
		return CapabilityNativeResume
	case (sourceClaude || sourceCodex || sourcePi) && (targetClaude || targetCodex || targetPi) && sourceTool != targetHarness:
		return CapabilityTranscriptTail
	default:
		return CapabilityUnsupported
	}
}

// fidelityFor returns the SwitchFidelity for a given capability level.
// Fidelity descriptions are stable labels used by both CLI and TUI surfaces.
func executionFor(cap SwitchCapability) SwitchExecution {
	switch cap {
	case CapabilityNativeResume:
		return ExecutionSupported
	case CapabilityTranscriptTail:
		return ExecutionPlanned
	default:
		return ExecutionUnsupported
	}
}

func fidelityFor(cap SwitchCapability) SwitchFidelity {
	switch cap {
	case CapabilityNativeResume:
		return fidelityForNativeResume()
	case CapabilityTranscriptTail:
		return fidelityForTranscriptTail()
	default:
		return fidelityForUnsupported()
	}
}

func fidelityForNativeResume() SwitchFidelity {
	return SwitchFidelity{
		Inclusions: []string{
			"exact conversation file (JSONL, copied byte-for-byte)",
			"subagent sidechain transcripts (companion directory)",
			"session ID continuity (claude --resume <id>)",
			"full conversation history up to switch point",
		},
		Exclusions: nil, // no fidelity loss for native-resume
	}
}

func fidelityForTranscriptTail() SwitchFidelity {
	return SwitchFidelity{
		Inclusions: []string{
			fmt.Sprintf("exact exported context payload bounded to %d bytes", DefaultHandoffMaxChars),
			"source harness, cwd, title, and group metadata",
			"explicitly distinct target identity",
		},
		Exclusions: []string{
			"native session state (target is fresh; no native resume)",
			"source process, credentials, and permission settings",
			"attachments and harness-specific tool state",
			"context beyond the export character/byte budget",
			"target readiness or authentication verification",
		},
	}
}

func fidelityForUnsupported() SwitchFidelity {
	return SwitchFidelity{
		Inclusions: nil,
		Exclusions: []string{
			"all context — no switch path exists for this source/target combination",
		},
	}
}

// refusalForUnsupported builds a SwitchRefusal for CapabilityUnsupported with
// an informative message naming the specific unsupported combination.
func refusalForUnsupported(sourceTool, targetHarness string) *SwitchRefusal {
	switch {
	case sourceTool == "hermes":
		return &SwitchRefusal{
			Code:    "unsupported-tool",
			Message: "Hermes sessions have no account abstraction or transcript export. Switching is not supported.",
		}
	case sourceTool == "pi":
		return &SwitchRefusal{
			Code:    "unsupported-tool",
			Message: "Pi sessions have no account abstraction or transcript export. Switching is not supported.",
		}
	case targetHarness == "hermes":
		return &SwitchRefusal{
			Code:    "unsupported-target",
			Message: "Hermes is not a supported switch target: no context import path exists.",
		}
	case targetHarness == "pi":
		return &SwitchRefusal{
			Code:    "unsupported-target",
			Message: "Pi is not a supported switch target: no context import path exists.",
		}
	default:
		return &SwitchRefusal{
			Code: "unsupported-combination",
			Message: fmt.Sprintf(
				"no switch path exists from %q to %q. Supported: same-harness native resume, or cross-harness context plans among Claude, Codex, and Pi.",
				sourceTool, targetHarness),
		}
	}
}
