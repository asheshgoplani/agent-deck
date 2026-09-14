package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionSwitchPreview is a read-only command that shows what switching
// a session's account or harness would do — capability level, fidelity
// preservation, and any preflight refusals — without performing any mutation.
//
// It is the canonical preflight surface for:
//   - TUI: shown in the confirmation step before committing an account change
//   - CLI: user-visible preview before running switch-account or handoff
//   - Programmatic: --json for scripted switch workflows
//
// No sessions are stopped, no files are copied, no credentials are read.
func handleSessionSwitchPreview(profile string, args []string) {
	fs := flag.NewFlagSet("session switch-preview", flag.ExitOnError)
	toHarness := fs.String("to-harness", "", "Target harness (claude, codex, …). Defaults to source harness.")
	toAccount := fs.String("to-account", "", "Target named account slot (must have a config_dir in config.toml)")
	maxChars := fs.Int("max-chars", session.DefaultHandoffMaxChars,
		"Character budget for transcript-tail capability display (bytes for multi-byte Unicode)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session switch-preview <id|title> [options]")
		fmt.Println()
		fmt.Println("Show a read-only preview of what switching a session's account or harness")
		fmt.Println("would do: capability level, context fidelity, and any preflight refusals.")
		fmt.Println("Nothing is mutated. Native execution is separate; cross-harness output is plan-only until lifecycle/readiness integration lands.")
		fmt.Println()
		fmt.Println("Capability levels:")
		fmt.Println("  native-resume    Same-harness Claude→Claude: exact copy + claude --resume")
		fmt.Println("  transcript-tail  Cross-harness Claude/Codex/Pi: bounded context to a fresh target (lossy)")
		fmt.Println("  unsupported      No switch path (Hermes, remote, …)")
		fmt.Println()
		fmt.Println("Execution labels:")
		fmt.Println("  supported        Native same-harness executor exists")
		fmt.Println("  handoff-only     Use Claude→Codex handoff; this is not native resume")
		fmt.Println("  planned          Launch plan only; target lifecycle/readiness executor is not implemented")
		fmt.Println("  unsupported      No executor exists")
		fmt.Println()
		fmt.Println("Account status labels:")
		fmt.Println("  configured  Slot present in config.toml (live auth not checked)")
		fmt.Println("  unknown     Slot absent from config.toml — switch would be refused")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session switch-preview my-project --to-account work")
		fmt.Println("  agent-deck session switch-preview my-project --to-harness codex")
		fmt.Println("  agent-deck session switch-preview my-project --to-account work --json")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	identifier := fs.Arg(0)
	out := NewCLIOutput(*jsonOutput, false)

	if identifier == "" {
		fs.Usage()
		os.Exit(1)
	}

	cfg, _ := session.LoadUserConfig()

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(1)
	}

	target := session.SwitchPreviewTarget{
		Harness: strings.TrimSpace(*toHarness),
		Account: strings.TrimSpace(*toAccount),
	}

	sourceSnapshot := switchSourceSnapshot(inst, instances)
	preview := session.PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, target, *maxChars, &sourceSnapshot)

	if *jsonOutput {
		printSwitchPreviewJSON(preview, *maxChars)
		if preview.Refusal != nil {
			os.Exit(1)
		}
		return
	}

	printSwitchPreviewHuman(preview, *maxChars)

	// Exit 1 when there is a refusal so callers can check $?
	if preview.Refusal != nil {
		os.Exit(1)
	}
}

// switchPreviewJSONOutput is the JSON representation of a SwitchPreview.
// It is a stable public contract — field names must not change.
type switchPreviewJSONOutput struct {
	SourceTitle         string `json:"source_title"`
	SourceTool          string `json:"source_tool"`
	SourceAccount       string `json:"source_account"`
	SourceAccountDir    string `json:"source_account_dir"`
	SourceAccountStatus string `json:"source_account_status"`
	SourceSessionID     string `json:"source_session_id"`
	SourceProjectPath   string `json:"source_project_path"`
	SourceIsRemote      bool   `json:"source_is_remote"`

	TargetHarness       string `json:"target_harness"`
	TargetAccount       string `json:"target_account"`
	TargetAccountDir    string `json:"target_account_dir"`
	TargetAccountStatus string `json:"target_account_status"`

	Capability string                         `json:"capability"`
	Execution  string                         `json:"execution"`
	Inclusions []string                       `json:"fidelity_inclusions"`
	Exclusions []string                       `json:"fidelity_exclusions"`
	LaunchPlan *session.FreshTargetLaunchPlan `json:"launch_plan,omitempty"`

	Refusal  *switchPreviewRefusalJSON `json:"refusal,omitempty"`
	Warnings []string                  `json:"warnings,omitempty"`

	// Informational: not a limit applied to the actual switch, only shown here.
	TranscriptBudgetBytes int `json:"transcript_budget_bytes"`
}

type switchPreviewRefusalJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func printSwitchPreviewJSON(preview *session.SwitchPreview, maxChars int) {
	out := &switchPreviewJSONOutput{
		SourceTitle:         preview.SourceTitle,
		SourceTool:          preview.SourceTool,
		SourceAccount:       preview.SourceAccount,
		SourceAccountDir:    preview.SourceAccountDir,
		SourceAccountStatus: string(preview.SourceAccountStat),
		SourceSessionID:     preview.SourceSessionID,
		SourceProjectPath:   preview.SourceProjectPath,
		SourceIsRemote:      preview.SourceIsRemote,

		TargetHarness:       preview.TargetHarness,
		TargetAccount:       preview.TargetAccount,
		TargetAccountDir:    preview.TargetAccountDir,
		TargetAccountStatus: string(preview.TargetAccountStat),

		Capability: string(preview.Capability),
		Execution:  string(preview.Execution),
		Inclusions: preview.Fidelity.Inclusions,
		Exclusions: preview.Fidelity.Exclusions,
		LaunchPlan: preview.LaunchPlan,

		Warnings: preview.Warnings,

		TranscriptBudgetBytes: maxChars,
	}
	if preview.Refusal != nil {
		out.Refusal = &switchPreviewRefusalJSON{
			Code:    preview.Refusal.Code,
			Message: preview.Refusal.Message,
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
}

func printSwitchPreviewHuman(preview *session.SwitchPreview, maxChars int) {
	fmt.Printf("Session:  %s\n", preview.SourceTitle)
	fmt.Printf("Source:   %s / account:%q [%s]\n",
		preview.SourceTool, preview.SourceAccount, preview.SourceAccountStat)
	if preview.SourceAccountDir != "" {
		fmt.Printf("          config dir: %s\n", preview.SourceAccountDir)
	}
	if preview.SourceSessionID != "" {
		fmt.Printf("          session id: %s\n", preview.SourceSessionID)
	}
	if preview.SourceIsRemote {
		fmt.Printf("          ⚠ remote session\n")
	}

	fmt.Printf("Target:   %s / account:%q [%s]\n",
		preview.TargetHarness, preview.TargetAccount, preview.TargetAccountStat)
	if preview.TargetAccountDir != "" {
		fmt.Printf("          config dir: %s\n", preview.TargetAccountDir)
	}

	fmt.Println()
	fmt.Printf("Capability: %s\n", preview.Capability)
	fmt.Printf("Execution:  %s (preview only; this command never mutates a session)\n", preview.Execution)

	if len(preview.Fidelity.Inclusions) > 0 {
		fmt.Println("Preserved:")
		for _, item := range preview.Fidelity.Inclusions {
			fmt.Printf("  ✓ %s\n", item)
		}
	}
	if len(preview.Fidelity.Exclusions) > 0 {
		fmt.Println("Not preserved:")
		for _, item := range preview.Fidelity.Exclusions {
			fmt.Printf("  ✗ %s\n", item)
		}
	}
	if preview.Capability == session.CapabilityTranscriptTail {
		fmt.Printf("  (transcript budget: %d bytes; bytes, not Unicode grapheme clusters)\n", maxChars)
	}
	if preview.LaunchPlan != nil {
		fmt.Printf("Launch plan: fresh %s target %s (executable=%t, ready=%t)\n", preview.LaunchPlan.Target.Tool, preview.LaunchPlan.Target.InstanceID, preview.LaunchPlan.Executable, preview.LaunchPlan.Ready)
		fmt.Printf("  native launch: %s %s\n", preview.LaunchPlan.NativeCommand, strings.Join(preview.LaunchPlan.NativeArgs, " "))
		fmt.Println("  plan-only: target lifecycle, identity/readiness observation, and registry commit remain unimplemented")
	}

	if len(preview.Warnings) > 0 {
		fmt.Println()
		for _, w := range preview.Warnings {
			fmt.Printf("warning: %s\n", w)
		}
	}

	fmt.Println()
	if preview.Refusal != nil {
		fmt.Printf("REFUSED [%s]: %s\n", preview.Refusal.Code, preview.Refusal.Message)
		fmt.Println()
		fmt.Println("Switch cannot proceed — resolve the above before running switch-account or handoff.")
	} else {
		switch preview.Execution {
		case session.ExecutionSupported:
			fmt.Println("Preflight passed — the native switch executor supports this path.")
		case session.ExecutionHandoffOnly:
			fmt.Println("Preflight passed — legacy handoff label; this is not native resume.")
		case session.ExecutionPlanned:
			fmt.Println("Preflight passed — launch plan only; no executable switch is available.")
		default:
			fmt.Println("No refusal was found, but no mutating executor is available.")
		}
	}
}
