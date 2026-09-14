package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FreshTargetLaunchOptions describes the target binding for a cross-harness
// context transfer. It contains configuration metadata only; it never accepts
// credentials or source process state.
type FreshTargetLaunchOptions struct {
	TargetHarness     string
	TargetAccount     string
	TargetAccountHome string
	TargetInstanceID  string
	TargetTitle       string
	TargetGroupPath   string
	TargetProjectPath string
}

// FreshTargetIdentity is the stable identity assigned to the new target. The
// target identity is intentionally different from the source identity: a
// transferred context is never represented as a resumed source session.
type FreshTargetIdentity struct {
	InstanceID        string `json:"instance_id"`
	Tool              string `json:"tool"`
	SessionID         string `json:"session_id,omitempty"`
	NativeSessionPath string `json:"native_session_path,omitempty"`
	Account           string `json:"account,omitempty"`
	ProjectPath       string `json:"project_path"`
	Title             string `json:"title"`
	GroupPath         string `json:"group_path,omitempty"`
}

// FreshTargetLaunchPlan is a read-only, plan-only description of how a fresh
// target could receive an exported context. NativeArgs and Environment use
// only target-native flags and non-secret account bindings. Prompt contains
// the bounded transferred context in memory and is deliberately excluded from
// JSON serialization; callers must not persist it in journals or logs.
type FreshTargetLaunchPlan struct {
	Version        int                   `json:"version"`
	Source         ContextSourceIdentity `json:"source"`
	SourceArtifact ContextArtifact       `json:"source_artifact"`
	Target         FreshTargetIdentity   `json:"target"`
	Continuity     string                `json:"continuity"`
	TargetHome     string                `json:"target_home,omitempty"`
	SessionDir     string                `json:"session_dir,omitempty"`
	NativeCommand  string                `json:"native_command"`
	NativeArgs     []string              `json:"native_args"`
	Environment    map[string]string     `json:"environment,omitempty"`
	SourceBytes    int64                 `json:"source_bytes"`
	PayloadBytes   int                   `json:"payload_bytes"`
	Truncated      bool                  `json:"truncated"`
	// Prompt is only used while an executor creates the private handoff file;
	// it is never serialized into a journal or command line.
	Prompt               []byte         `json:"-"`
	PayloadPath          string         `json:"-"`
	PayloadSHA256        string         `json:"-"`
	Fidelity             SwitchFidelity `json:"fidelity"`
	Executable           bool           `json:"executable"`
	Ready                bool           `json:"ready"`
	RemainingIntegration []string       `json:"remaining_integration"`
}

// BuildFreshTargetLaunchPlan constructs a fresh-target launch plan from an
// already exact-ID exported context. It supports all six directions among
// Claude, Codex, and Pi. It performs no writes, lifecycle operations, config
// reads, credential access, or registry mutation.
//
// The returned plan is intentionally not executable by itself. Creating and
// persisting a distinct target Instance, starting its process, and observing a
// target-native identity/readiness event are owned by ExecuteCrossHarnessSwitch.
func BuildFreshTargetLaunchPlan(export *ContextExport, opts FreshTargetLaunchOptions) (*FreshTargetLaunchPlan, error) {
	if export == nil {
		return nil, fmt.Errorf("context export is nil")
	}
	source := export.Manifest.Source
	sourceHarness := canonicalSwitchHarness(source.Tool)
	targetHarness := canonicalSwitchHarness(opts.TargetHarness)
	if sourceHarness == "" {
		return nil, fmt.Errorf("unsupported source harness %q", source.Tool)
	}
	if targetHarness == "" {
		return nil, fmt.Errorf("unsupported target harness %q", opts.TargetHarness)
	}
	if sourceHarness == targetHarness {
		return nil, fmt.Errorf("fresh target plan requires different harnesses, got %s", targetHarness)
	}
	if strings.TrimSpace(source.SessionID) == "" {
		return nil, fmt.Errorf("source context has no exact session ID")
	}
	if len(export.Payload) == 0 {
		return nil, fmt.Errorf("source context payload is empty")
	}
	if export.Manifest.Continuity != "native" {
		return nil, fmt.Errorf("source context must be an exact native export")
	}

	account := strings.TrimSpace(opts.TargetAccount)
	if targetHarness == "pi" && account != "" {
		return nil, fmt.Errorf("Pi does not support named accounts; target account %q is unsupported (use the Pi default)", account)
	}
	if account != "" && (targetHarness == "claude" || targetHarness == "codex") && strings.TrimSpace(opts.TargetAccountHome) == "" {
		return nil, fmt.Errorf("target %s account %q has no configured home binding", targetHarness, account)
	}

	project := strings.TrimSpace(opts.TargetProjectPath)
	if project == "" {
		project = strings.TrimSpace(source.WorkingDir)
	}
	if project == "" {
		project = strings.TrimSpace(source.ProjectPath)
	}
	if project == "" {
		return nil, fmt.Errorf("source context has no project path for target cwd")
	}
	targetID := strings.TrimSpace(opts.TargetInstanceID)
	if targetID == "" {
		targetID = stableTransferInstanceID(source, targetHarness, account)
	}
	if targetID == source.InstanceID && targetID != "" {
		return nil, fmt.Errorf("target instance identity must differ from source instance %q", source.InstanceID)
	}
	title := strings.TrimSpace(opts.TargetTitle)
	if title == "" {
		title = source.Title
	}
	if title == "" {
		title = "transferred-" + targetID
	}
	group := strings.TrimSpace(opts.TargetGroupPath)
	if group == "" {
		group = source.GroupPath
	}

	targetSessionID := ""
	if targetHarness == "claude" {
		targetSessionID = stableTransferSessionID(source, targetHarness, account)
	}
	plan := &FreshTargetLaunchPlan{
		Version:        1,
		Source:         source,
		SourceArtifact: export.Manifest.Artifact,
		Target: FreshTargetIdentity{
			InstanceID:  targetID,
			Tool:        targetHarness,
			SessionID:   targetSessionID,
			Account:     account,
			ProjectPath: project,
			Title:       title,
			GroupPath:   group,
		},
		Continuity:    "transferred",
		TargetHome:    strings.TrimSpace(opts.TargetAccountHome),
		NativeCommand: targetHarness,
		Environment:   make(map[string]string),
		SourceBytes:   export.Manifest.Artifact.SourceBytes,
		PayloadBytes:  export.Manifest.Artifact.PayloadBytes,
		Truncated:     export.Manifest.Artifact.Truncated,
		Prompt:        transferredPrompt(export, sourceHarness, targetHarness, project, title, group),
		Fidelity:      transferredFidelity(export),
		Executable:    false,
		Ready:         false,
		RemainingIntegration: []string{
			"execute through ExecuteCrossHarnessSwitch; this read-only plan does not mutate a target",
			"target-native evidence is required before readiness can be reported",
			"context delivery is recorded separately; semantic context acceptance remains pending",
		},
	}

	switch targetHarness {
	case "claude":
		// The caller appends Prompt as the native positional message. Keeping
		// it out of NativeArgs prevents preview JSON from serializing context.
		plan.NativeArgs = []string{"--session-id", targetSessionID}
		if plan.TargetHome != "" {
			plan.Environment["CLAUDE_CONFIG_DIR"] = plan.TargetHome
		}
	case "codex":
		// Codex has no fresh-session --session-id flag in its native CLI. The
		// stable target identity is therefore the Agent Deck target instance;
		// the native prompt and cwd flags still create a fresh Codex thread.
		// The caller appends Prompt as Codex's native positional message.
		plan.NativeArgs = []string{"-C", project}
		if plan.TargetHome != "" {
			plan.Environment["CODEX_HOME"] = plan.TargetHome
		}
	case "pi":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve target Pi home: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return nil, fmt.Errorf("resolve target Pi home: empty home")
		}
		// Bind the actual local target home now. A literal ${HOME} expression
		// would be re-resolved by a later shell and could launch another
		// account/incarnation after the preview was accepted.
		plan.SessionDir = filepath.Join(home, ".pi", "agent-deck", targetID)
		// Pi v0.85+ chooses a timestamped native JSONL filename under this
		// instance-scoped directory. There is no safe predicted file path; the
		// observer correlates a post-launch file against its durable directory
		// snapshot rather than assuming the legacy session.jsonl name.
		// Pi's supported fresh-session binding is its instance-scoped session
		// directory. Pi mints the native session header ID; the observer reads
		// that exact fresh header rather than inventing a --session-id flag.
		plan.NativeArgs = []string{"--session-dir", plan.SessionDir, "--name", title}
	default:
		return nil, fmt.Errorf("unsupported target harness %q", targetHarness)
	}
	return plan, nil
}

// BuildFreshTargetLaunchPlanForInstance performs the exact-ID export and then
// builds a plan. It is the shared read-only adapter used by CLI/TUI previews;
// it never starts or mutates either instance.
func BuildFreshTargetLaunchPlanForInstance(cfg *UserConfig, inst *Instance, target SwitchPreviewTarget, maxBytes int) (*FreshTargetLaunchPlan, error) {
	if inst == nil {
		return nil, fmt.Errorf("session is nil")
	}
	harness := strings.TrimSpace(target.Harness)
	if harness == "" {
		harness = inst.Tool
	}
	if canonicalSwitchHarness(harness) == "pi" && strings.TrimSpace(target.Account) != "" {
		return nil, fmt.Errorf("Pi does not support named accounts; target account %q is unsupported (use the Pi default)", target.Account)
	}
	var sessionID string
	if inst.Tool == "pi" {
		var err error
		sessionID, err = exactPiSessionID(inst)
		if err != nil {
			return nil, err
		}
	} else {
		sessionID = resolveSourceSessionID(inst)
	}
	// A fresh target may only receive a readable portable projection. Native
	// ExportContext deliberately remains raw so same-harness account switching
	// can retain valid metadata-only artifacts.
	export, err := ExportPortableContext(inst, ContextExportOptions{SessionID: sessionID, MaxBytes: maxBytes})
	if err != nil {
		return nil, err
	}
	dir, stat := resolveTargetAccountInfo(cfg, canonicalSwitchHarness(harness), strings.TrimSpace(target.Account))
	if strings.TrimSpace(target.Account) != "" && stat != AccountStatusConfigured {
		return nil, fmt.Errorf("target account %q is not configured for %s", target.Account, harness)
	}
	return BuildFreshTargetLaunchPlan(export, FreshTargetLaunchOptions{
		TargetHarness: harness, TargetAccount: target.Account, TargetAccountHome: ExpandPath(dir),
		TargetProjectPath: inst.EffectiveWorkingDir(), TargetTitle: inst.Title, TargetGroupPath: inst.GroupPath,
	})
}

// CanonicalSwitchHarnessForUI exposes the same normalization used by launch
// plans so the TUI can decide whether its edit confirmation is a lossy
// cross-harness action without duplicating tool aliases.
func CanonicalSwitchHarnessForUI(harness string) string {
	return canonicalSwitchHarness(harness)
}

func canonicalSwitchHarness(harness string) string {
	harness = strings.ToLower(strings.TrimSpace(harness))
	switch {
	case IsClaudeCompatible(harness):
		return "claude"
	case IsCodexCompatible(harness):
		return "codex"
	case harness == "pi":
		return "pi"
	default:
		return ""
	}
}

func stableTransferInstanceID(source ContextSourceIdentity, target, account string) string {
	sum := sha256.Sum256([]byte("agent-deck/cross-harness/instance/v1\x00" + source.InstanceID + "\x00" + source.SessionID + "\x00" + target + "\x00" + account))
	return "switch-" + hex.EncodeToString(sum[:12])
}

func stableTransferSessionID(source ContextSourceIdentity, target, account string) string {
	sum := sha256.Sum256([]byte("agent-deck/cross-harness/session/v1\x00" + source.InstanceID + "\x00" + source.SessionID + "\x00" + target + "\x00" + account))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func transferredPrompt(export *ContextExport, source, target, project, title, group string) []byte {
	projection := portableContextProjection(source, export.Payload)
	if export.portable {
		// BuildFreshTargetLaunchPlanForInstance supplies the already bounded
		// portable export. Projecting it again would parse its rendered text as
		// JSONL and discard every readable turn.
		projection = string(export.Payload)
	}
	return []byte(fmt.Sprintf("You are continuing an Agent Deck session in a fresh %s target.\nSource harness: %s\nTarget cwd: %s\nTarget title: %s\nTarget group: %s\nThis is a portable projection of readable user and assistant text only. Native session state, credentials, tool state, instruction attachments, bridge/account metadata, and source process identity are not transferred.\n--- BEGIN TRANSFERRED CONTEXT ---\n%s\n--- END TRANSFERRED CONTEXT ---\nTreat this as context only; do not claim native resume.", target, source, project, title, group, projection))
}

func transferredFidelity(export *ContextExport) SwitchFidelity {
	losses := append([]string(nil), export.Manifest.LossDisclosure...)
	losses = append(losses,
		"only readable user and assistant text is projected; instructions, attachments, bridge/account metadata, and tool-specific state are omitted",
		"native session state is not resumed across harnesses",
		"source process, files, credentials, attachments, and tool state remain outside the target",
		"target starts as a distinct fresh session with a stable Agent Deck identity",
	)
	return SwitchFidelity{
		Inclusions: []string{"bounded exact-ID exported context payload", "source harness, cwd, title, and group metadata", "explicit source and target identity manifest"},
		Exclusions: losses,
	}
}
