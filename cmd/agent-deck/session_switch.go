package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// handleSessionSwitch is the mutating CLI surface for the same backend used by
// the TUI. It never infers an account or harness: both are explicit (an empty
// value means the target default only where the preview says that is valid).
func handleSessionSwitch(profile string, args []string) {
	fs := flag.NewFlagSet("session switch", flag.ExitOnError)
	toHarness := fs.String("to-harness", "", "Target harness (claude, codex, pi, …); empty keeps the source harness")
	toAccount := fs.String("to-account", "", "Target configured account slot; empty means the target default")
	maxBytes := fs.Int("max-bytes", session.DefaultHandoffMaxChars, "Maximum transferred context bytes for cross-harness handoff")
	noStart := fs.Bool("no-start", false, "Create the distinct target without starting it")
	confirmContextLoss := fs.Bool("confirm-context-loss", false, "Required for lossy cross-harness transfer after reviewing switch-preview")
	jsonOutput := fs.Bool("json", false, "Output the switch result as JSON")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session switch <id|title> --to-harness <harness> [--to-account <account>]")
		fmt.Println()
		fmt.Println("Switch a session through the common source-preserving backend.")
		fmt.Println("Same-harness native switches are supported where verified; cross-harness directions create fresh targets and wait for native readiness evidence.")
		fmt.Println("Configured account presence is reported; OAuth authentication is never verified here.")
		fmt.Println("Pi cross-harness plans use the default account only; named Pi accounts fail explicitly.")
		fmt.Println()
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	inst, errMsg, errCode := ResolveSession(fs.Arg(0), instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		os.Exit(1)
	}
	cfg, cfgErr := session.LoadUserConfig()
	if cfgErr != nil {
		out.Error(fmt.Sprintf("load config: %v", cfgErr), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	target := session.SwitchPreviewTarget{Harness: strings.TrimSpace(*toHarness), Account: strings.TrimSpace(*toAccount)}
	sourceSnapshot := switchSourceSnapshot(inst, instances)
	preview := session.PreviewSwitchWithMaxBytesAndSnapshot(cfg, inst, target, *maxBytes, &sourceSnapshot)
	var result *session.HarnessSwitchResult
	var crossResult *session.CrossHarnessSwitchResult
	var switchErr error
	if preview.Execution == session.ExecutionPlanned && preview.Refusal == nil {
		if !*confirmContextLoss {
			out.Error("cross-harness transfer is lossy; review `session switch-preview` and repeat with --confirm-context-loss", ErrCodeInvalidOperation)
			os.Exit(1)
		}
		crossResult, switchErr = session.ExecuteCrossHarnessSwitch(context.Background(), cfg, inst, session.CrossHarnessSwitchOptions{
			Target: target, MaxBytes: *maxBytes, NoStart: *noStart, SourceSnapshot: &sourceSnapshot,
		}, session.CrossHarnessSwitchDependencies{
			Store:           session.StorageCrossHarnessTargetStore{Storage: storage},
			Lifecycle:       session.InstanceCrossHarnessLifecycle{},
			Observer:        session.NewNativeCrossHarnessTargetObserver(),
			SourceOwnership: switchSourceOwnershipValidator(storage),
		})
	} else {
		result, switchErr = session.ExecuteHarnessSwitch(cfg, inst, session.HarnessSwitchOptions{
			Target: target, MaxBytes: *maxBytes, NoStart: *noStart,
		})
	}
	if result == nil && crossResult == nil {
		if switchErr == nil {
			switchErr = fmt.Errorf("switch failed without a result")
		}
		out.Error(switchErr.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if switchErr != nil {
		// A normal lack of readiness remains pending. A journal failure after a
		// committed target/account write is instead failed and recovery-required;
		// never present either as a successful switch.
		if crossResult != nil && crossResult.Pending && !errors.Is(switchErr, session.ErrCrossHarnessRecoveryRequired) {
			payload := map[string]any{
				"success": false, "status": "pending", "pending": true, "operation_id": crossResult.OperationID,
				"target_id": crossHarnessTargetID(crossResult), "target_ready": crossResult.TargetReady,
				"missing_contract":    crossResult.MissingContract,
				"configured_account":  crossResult.ConfiguredAccount,
				"authentication":      crossResult.Authentication,
				"context_delivery":    crossResult.ContextDelivery,
				"semantic_acceptance": crossResult.SemanticAcceptance,
				"source_sha256":       crossResult.SourceSHA256, "loss_disclosure": crossResult.LossDisclosure,
			}
			if *jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetEscapeHTML(false)
				_ = enc.Encode(payload)
			} else {
				fmt.Printf("Created distinct %s target %s; status=pending; readiness=%s (%s)\\n", crossHarnessTargetTool(crossResult, preview.TargetHarness), crossHarnessTargetID(crossResult), readinessPresentation(crossResult.TargetReady), crossResult.MissingContract)
			}
			return
		}
		if crossResult != nil {
			recoveryRequired := crossResult.TargetCreated || errors.Is(switchErr, session.ErrCrossHarnessRecoveryRequired)
			data := map[string]interface{}{
				"status": "failed", "pending": crossResult.Pending, "recovery_required": recoveryRequired,
				"operation_id": crossResult.OperationID, "target_id": crossHarnessTargetID(crossResult),
				"target_created": crossResult.TargetCreated, "target_ready": crossResult.TargetReady,
				"missing_contract": crossResult.MissingContract, "configured_account": crossResult.ConfiguredAccount,
				"authentication": crossResult.Authentication, "context_delivery": crossResult.ContextDelivery,
				"semantic_acceptance": crossResult.SemanticAcceptance, "source_sha256": crossResult.SourceSHA256,
				"loss_disclosure": crossResult.LossDisclosure,
			}
			if *jsonOutput {
				out.ErrorWithData(switchErr.Error(), ErrCodeInvalidOperation, data)
			} else {
				fmt.Fprintf(os.Stderr, "Switch failed; status=failed; recovery-required=%t; target_id=%s; target_created=%t; target_ready=%t: %v\n", recoveryRequired, crossHarnessTargetID(crossResult), crossResult.TargetCreated, crossResult.TargetReady, switchErr)
			}
		} else if *jsonOutput {
			out.ErrorWithData(switchErr.Error(), ErrCodeInvalidOperation, map[string]interface{}{"status": switchPresentationStatus(false, false), "pending": false, "committed": result != nil && result.Committed, "recovery_required": result != nil && result.Committed})
		} else {
			fmt.Fprintf(os.Stderr, "Switch failed; status=failed; recovery-required=%t: %v\n", result != nil && result.Committed, switchErr)
		}
		// Native lifecycle work may have completed before a later journal/error
		// boundary. Persist that exact bounded mutation without replaying the
		// stale registry snapshot or the lifecycle operation.
		if result != nil && result.Committed {
			if saveErr := storage.CommitNativeHarnessSwitch(inst, result); saveErr != nil {
				fmt.Fprintf(os.Stderr, "warning: persist native switch recovery failed: %v\n", saveErr)
			}
		}
		os.Exit(1)
	}
	if crossResult != nil {
		payload := map[string]any{
			"success": crossResult.TargetReady, "status": crossHarnessPresentationStatus(crossResult), "pending": crossResult.Pending,
			"operation_id": crossResult.OperationID, "target_id": crossHarnessTargetID(crossResult),
			"target_ready": crossResult.TargetReady, "source_sha256": crossResult.SourceSHA256,
			"configured_account": crossResult.ConfiguredAccount, "authentication": crossResult.Authentication,
			"context_delivery": crossResult.ContextDelivery, "semantic_acceptance": crossResult.SemanticAcceptance,
			"loss_disclosure": crossResult.LossDisclosure,
		}
		if *jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(payload)
			return
		}
		fmt.Printf("Created distinct %s target %s; status=%s; readiness=%s\n", crossHarnessTargetTool(crossResult, preview.TargetHarness), crossHarnessTargetID(crossResult), crossHarnessPresentationStatus(crossResult), readinessPresentation(crossResult.TargetReady))
		return
	}
	if err := storage.CommitNativeHarnessSwitch(inst, result); err != nil {
		out.Error(fmt.Sprintf("save switched session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	payload := map[string]any{
		"success": result.Committed && result.DestinationReady, "status": switchPresentationStatus(result.Committed, result.DestinationReady), "pending": result.Committed && !result.DestinationReady,
		"id": inst.ID, "title": inst.Title,
		"old_tool": result.OldTool, "new_tool": result.NewTool,
		"old_account": result.OldAccount, "new_account": result.NewAccount,
		"continuity": result.Continuity, "source_sha256": result.SourceArtifactSHA256,
		"destination_path": result.DestinationPath, "destination_ready": result.DestinationReady,
		"restarted": result.Restarted, "loss_disclosure": result.LossDisclosure,
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(payload)
		return
	}
	status := switchPresentationStatus(result.Committed, result.DestinationReady)
	fmt.Printf("Switch %s: %s/%q -> %s/%q (%s); status=%s; readiness=%s\n", inst.Title, result.OldTool, result.OldAccount, result.NewTool, result.NewAccount, result.Continuity, status, readinessPresentation(result.DestinationReady))
	if len(result.LossDisclosure) > 0 {
		fmt.Printf("Not preserved: %s\n", strings.Join(result.LossDisclosure, "; "))
	}
}

// switchPresentationStatus is shared by the native and cross-harness command
// surfaces. A durable account assignment without native readiness evidence is
// pending, not a successful ready switch.
// switchSourceSnapshot reads only public watcher route metadata. A missing
// clients.json means no routes are configured; an unreadable or malformed file
// is fail-closed for cross-harness replacement because routing ownership cannot
// be safely inferred or copied.
// switchSourceOwnershipValidator obtains a fresh registry graph and watcher
// routing state for each executor validation. The executor calls it before
// staging and final commit; watcher files remain external to the DB transaction
// and therefore cannot be proven atomic with that final CAS.
func switchSourceOwnershipValidator(storage *session.Storage) session.CrossHarnessSourceOwnershipValidator {
	return session.CrossHarnessSourceOwnershipValidatorFunc(func(source *session.Instance) (session.SwitchSourceSnapshot, error) {
		if storage == nil {
			return session.SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("session storage is unavailable")
		}
		instances, err := storage.Load()
		if err != nil {
			return session.SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("load current session graph: %w", err)
		}
		var current *session.Instance
		for _, candidate := range instances {
			if candidate != nil && source != nil && candidate.ID == source.ID {
				current = candidate
				break
			}
		}
		if current == nil {
			return session.AuthoritativeSwitchSourceSnapshot(source, instances, false, true)
		}
		snapshot := switchSourceSnapshot(current, instances)
		return session.AuthoritativeSwitchSourceSnapshot(source, instances, snapshot.WatcherBridgeTarget, snapshot.ManagementUnknown)
	})
}

func switchSourceSnapshot(inst *session.Instance, instances []*session.Instance) session.SwitchSourceSnapshot {
	watcherTarget, managementUnknown := false, false
	watcherDir, err := session.WatcherDir()
	if err != nil {
		managementUnknown = true
	} else {
		clientsPath := filepath.Join(watcherDir, "clients.json")
		if _, statErr := os.Stat(clientsPath); statErr == nil {
			clients, loadErr := watcher.LoadClientsJSON(clientsPath)
			if loadErr != nil {
				managementUnknown = true
			} else {
				for _, client := range clients {
					if client.Conductor == inst.ID || client.Conductor == inst.Title || session.ConductorSessionTitle(client.Conductor) == inst.Title {
						watcherTarget = true
						break
					}
				}
			}
		} else if !os.IsNotExist(statErr) {
			managementUnknown = true
		}
	}
	return session.SnapshotSwitchSource(inst, instances, watcherTarget, managementUnknown)
}

func switchPresentationStatus(committed, destinationReady bool) string {
	if !committed {
		return "failed"
	}
	if !destinationReady {
		return "pending"
	}
	return "success"
}

func crossHarnessTargetID(result *session.CrossHarnessSwitchResult) string {
	if result == nil || result.Target == nil {
		return ""
	}
	return result.Target.ID
}

func crossHarnessTargetTool(result *session.CrossHarnessSwitchResult, fallback string) string {
	if result == nil || result.Target == nil {
		return fallback
	}
	return result.Target.Tool
}

func crossHarnessPresentationStatus(result *session.CrossHarnessSwitchResult) string {
	if result == nil {
		return "failed"
	}
	if result.Pending {
		return "pending"
	}
	return switchPresentationStatus(result.TargetCreated, result.TargetReady)
}

func readinessPresentation(ready bool) string {
	if ready {
		return "ready"
	}
	return "pending"
}
