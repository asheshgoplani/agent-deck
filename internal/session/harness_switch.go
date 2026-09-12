package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HarnessSwitchOptions is the common execution input for the CLI and TUI.
type HarnessSwitchOptions struct {
	Target   SwitchPreviewTarget
	MaxBytes int
	NoStart  bool
}

// HarnessSwitchResult is deliberately explicit about continuity. Committed is
// true only after the destination identity and readiness checks pass (or when
// the source was already at the requested target and the operation is a retry).
type HarnessSwitchResult struct {
	Preview              *SwitchPreview
	OldTool              string
	NewTool              string
	OldAccount           string
	NewAccount           string
	Continuity           string
	Conversation         string
	SourceArtifactSHA256 string
	DestinationPath      string
	DestinationReady     bool
	Restarted            bool
	Committed            bool
	Warnings             []string
	LossDisclosure       []string
	// nativeSource/nativeTarget are the exact durable mutation for the
	// post-lifecycle registry CAS. They remain private so callers cannot forge
	// a switch commit without ExecuteHarnessSwitch's journal binding.
	nativeSource                 switchIdentity
	nativeTarget                 switchIdentity
	nativeJournalPath            string
	nativeStorageAcknowledgement bool
}

var ErrSwitchBusy = errors.New("session switch already in progress")

const (
	switchJournalVersion = 3
	switchPrepared       = "prepared"
	switchStaged         = "staged"
	switchInstalled      = "installed"
	switchCommitted      = "committed"
	switchCompleted      = "completed"
	switchFailed         = "failed"
)

type switchIdentity struct {
	InstanceID string `json:"instance_id"`
	Tool       string `json:"tool"`
	// StorageTool preserves the exact configured tool key for the registry CAS;
	// Tool remains the canonical harness used for switch routing.
	StorageTool   string    `json:"storage_tool,omitempty"`
	SessionID     string    `json:"session_id"`
	ClaudeID      string    `json:"claude_session_id,omitempty"`
	CodexID       string    `json:"codex_session_id,omitempty"`
	Account       string    `json:"account"`
	ProjectPath   string    `json:"project_path"`
	WorkingDir    string    `json:"working_dir,omitempty"`
	Title         string    `json:"title"`
	GroupPath     string    `json:"group_path"`
	Command       string    `json:"command"`
	Status        Status    `json:"status"`
	LastStartedAt time.Time `json:"last_started_at,omitempty"`
	Generation    string    `json:"generation"`
}

type switchJournal struct {
	Version           int            `json:"version"`
	OperationID       string         `json:"operation_id"`
	State             string         `json:"state"`
	Source            switchIdentity `json:"source"`
	Target            switchIdentity `json:"target"`
	SourcePath        string         `json:"source_path,omitempty"`
	SourceSHA256      string         `json:"source_sha256,omitempty"`
	StageDir          string         `json:"stage_dir,omitempty"`
	PromptPath        string         `json:"prompt_path,omitempty"`
	PromptSHA256      string         `json:"prompt_sha256,omitempty"`
	Destination       string         `json:"destination,omitempty"`
	WasRunning        bool           `json:"was_running"`
	NoStart           bool           `json:"no_start"`
	UpdatedAt         time.Time      `json:"updated_at"`
	Failure           string         `json:"failure,omitempty"`
	RequestGeneration string         `json:"request_generation"`
	DestinationReady  bool           `json:"destination_ready"`
	TargetObserved    bool           `json:"target_observed"`
}

// ExecuteHarnessSwitch is the only mutating account/harness switch entry
// point. PreviewSwitch is repeated inside the operation lock: a CLI preview
// and a later TUI/CLI execution must not race a changed registry or artifact.
func ExecuteHarnessSwitch(cfg *UserConfig, inst *Instance, opts HarnessSwitchOptions) (*HarnessSwitchResult, error) {
	if inst == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if !validSwitchIdentity(inst.ID) {
		return nil, fmt.Errorf("session id is not a safe switch identity: %q", inst.ID)
	}
	// This is deliberately independent of the request journal: A -> B and
	// A -> C must serialize before either can inspect request-specific state.
	// Keep it through every preflight, lifecycle, registry, and journal action.
	sourceLock, err := acquireHarnessSwitchSourceLock(inst.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSwitchBusy, err)
	}
	defer sourceLock.Release()

	preview := PreviewSwitch(cfg, inst, opts.Target)
	if preview.Refusal != nil {
		return nil, fmt.Errorf("switch refused [%s]: %s", preview.Refusal.Code, preview.Refusal.Message)
	}
	if preview.Execution == ExecutionPlanned {
		return nil, fmt.Errorf("switch is plan-only for %s -> %s: target lifecycle, fresh identity, readiness verification, and registry commit are not implemented", preview.SourceTool, preview.TargetHarness)
	}
	if preview.TargetAccount != "" && preview.TargetAccountStat != AccountStatusConfigured {
		return nil, fmt.Errorf("target account %q is not configured; authentication is not verified by Agent Deck", preview.TargetAccount)
	}

	requestGeneration := switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)
	journalPath, err := switchJournalPathForRequest(inst.ID, requestGeneration)
	if err != nil {
		return nil, err
	}
	lock, err := AcquireConfigFileLock(journalPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSwitchBusy, err)
	}
	defer lock.Release()

	journal, err := loadSwitchJournal(journalPath)
	if err != nil {
		return nil, err
	}
	if journal != nil && journal.Version == 1 && journal.State != switchCompleted && journal.State != switchFailed {
		return nil, fmt.Errorf("refusing recovery: unsupported uncertain legacy version-1 journal is preserved at %s; it will not be adopted or replayed", journalPath)
	}
	// A committed native journal has completed its lifecycle work but still
	// requires either the scoped storage CAS (from its source identity) or an
	// acknowledgement after that CAS was durably observed on its target. Neither
	// path may replay lifecycle work.
	if journal != nil && journal.State == switchCommitted && sourceRecoveryIdentityMatches(journal.Source, inst) && journalMatchesRequest(journal, inst, preview, opts) {
		return resultFromCommittedJournal(preview, journal, journalPath, false), nil
	}
	if acknowledged, acknowledgedPath, findErr := committedNativeJournalForAcknowledgement(inst.ID, inst); findErr != nil {
		return nil, findErr
	} else if acknowledged != nil {
		return resultFromCommittedJournal(preview, acknowledged, acknowledgedPath, true), nil
	}
	// Version-2 request names did not bind StorageTool. Before creating a
	// version-3 operation (or exporting any current source bytes), find an
	// exact completed predecessor so the only action is its scoped registry
	// CAS repair. A failed version-3 descendant from the old discovery bug is
	// also preserved, but cannot hide the earlier completed mutation.
	if journal == nil || (journal.Version >= switchJournalVersion && journal.State == switchFailed) {
		if completed, _, findErr := completedSwitchJournalForRecovery(inst.ID, journalPath, inst, preview, opts); findErr != nil {
			return nil, findErr
		} else if completed != nil {
			return resultFromCompletedJournal(preview, completed), nil
		}
	}
	if journal == nil {
		if incompletePath, findErr := incompleteSwitchJournalPath(inst.ID, journalPath); findErr != nil {
			return nil, findErr
		} else if incompletePath != "" {
			return nil, fmt.Errorf("refusing new switch: unresolved prior operation is preserved at %s; recover it before starting another switch", incompletePath)
		}
		// Version 1 used one journal per session. Preserve it for recovery, but
		// never overwrite a completed or failed historical operation when this
		// request is distinct (for example, account A -> B -> A).
		legacyPath, legacyErr := switchJournalPath(inst.ID)
		if legacyErr != nil {
			return nil, legacyErr
		}
		legacy, loadErr := loadSwitchJournal(legacyPath)
		if loadErr != nil {
			return nil, loadErr
		}
		if legacy != nil {
			if legacy.Version == 1 && legacy.State != switchCompleted && legacy.State != switchFailed {
				return nil, fmt.Errorf("refusing recovery: unsupported uncertain legacy version-1 journal is preserved at %s; it will not be adopted or replayed", legacyPath)
			}
			switch {
			case journalMatchesRequest(legacy, inst, preview, opts):
				journal, journalPath = legacy, legacyPath
			case legacy.State != switchCompleted && legacy.State != switchFailed:
				return nil, fmt.Errorf("refusing new switch: unresolved prior operation is preserved at %s; recover it before starting another switch", legacyPath)
			}
		}
	}
	if journal != nil && !journalMatchesRequest(journal, inst, preview, opts) {
		return nil, fmt.Errorf("refusing recovery: %s", journalRequestMismatchReason(journal, inst, preview, opts))
	}
	if journal != nil && journal.State == switchCompleted {
		return resultFromCompletedJournal(preview, journal), nil
	}
	if journal != nil && journal.State == switchFailed {
		// A failed journal is a tombstone, not permission to replay an old
		// mutation. A new request gets a fresh operation only after the caller
		// has restored the source identity.
		return nil, fmt.Errorf("previous switch failed: %s", journal.Failure)
	}
	if IsClaudeCompatible(preview.SourceTool) && IsClaudeCompatible(preview.TargetHarness) {
		return executeNativeClaudeSwitch(cfg, inst, preview, opts, journalPath, journal)
	}
	if IsCodexCompatible(preview.SourceTool) && IsCodexCompatible(preview.TargetHarness) {
		return executeNativeCodexSwitch(cfg, inst, preview, opts, journalPath, journal)
	}
	if IsClaudeCompatible(preview.SourceTool) && IsCodexCompatible(preview.TargetHarness) {
		return executeClaudeCodexSwitch(cfg, inst, preview, opts, journalPath, journal)
	}
	return nil, fmt.Errorf("switch executor is not available for %s -> %s", preview.SourceTool, preview.TargetHarness)
}

func executeNativeClaudeSwitch(cfg *UserConfig, inst *Instance, preview *SwitchPreview, opts HarnessSwitchOptions, journalPath string, journal *switchJournal) (*HarnessSwitchResult, error) {
	if preview.TargetAccount == "" {
		return nil, fmt.Errorf("Claude native resume requires a named target account; choose a configured account")
	}
	if preview.TargetAccount == inst.Account {
		return nativeHarnessSwitchResult(&HarnessSwitchResult{Preview: preview, OldTool: inst.Tool, NewTool: inst.Tool, OldAccount: inst.Account, NewAccount: inst.Account, Continuity: "native", Conversation: "no-op: requested account is already active; no restart or native readiness assertion was made", Committed: true, DestinationReady: false}, identityForInstance(inst), identityForInstance(inst)), nil
	}
	wasRunning := nativeSwitchRunning(inst)
	if wasRunning && strings.TrimSpace(inst.ClaudeSessionID) == "" {
		return nil, fmt.Errorf("refusing native switch for running session %q without an exact Claude session ID; no transcript may be guessed", inst.Title)
	}
	if cfg == nil {
		return nil, fmt.Errorf("configuration is unavailable; target account cannot be validated")
	}
	targetDir := cfg.GetProfileClaudeConfigDir(preview.TargetAccount)
	if targetDir == "" {
		return nil, fmt.Errorf("target Claude account %q is not configured", preview.TargetAccount)
	}

	j := journal
	if j == nil {
		j = &switchJournal{Version: switchJournalVersion, OperationID: switchOperationIDForRequest(inst.ID, switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)), State: switchPrepared,
			Source: identityForInstance(inst), Target: targetIdentityFor(inst, preview), WasRunning: wasRunning, NoStart: opts.NoStart,
			RequestGeneration: switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)}
	}
	if err := writeSwitchJournal(journalPath, j); err != nil {
		return nil, err
	}

	var sourcePath, sourceHash string
	if inst.ClaudeSessionID != "" {
		export, exportErr := ExportClaudeContext(inst, 0)
		if exportErr != nil {
			return nil, fmt.Errorf("exact source preflight failed: %w", exportErr)
		}
		sourcePath, sourceHash = export.Manifest.Artifact.Path, export.Manifest.Artifact.SourceSHA256
	}
	if sourcePath != "" {
		j.SourcePath, j.SourceSHA256 = sourcePath, sourceHash
	}
	if j.StageDir == "" {
		stageRoot, err := switchStageRoot(inst.ID)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(stageRoot, 0o700); err != nil {
			return nil, err
		}
		j.StageDir, err = os.MkdirTemp(stageRoot, "native-")
		if err != nil {
			return nil, fmt.Errorf("create switch staging directory: %w", err)
		}
	}
	if sourcePath != "" {
		stagePath := filepath.Join(j.StageDir, filepath.Base(sourcePath))
		if _, err := os.Stat(stagePath); os.IsNotExist(err) {
			if err := copyFileVerified(sourcePath, stagePath); err != nil {
				return switchFailedNative(journalPath, j, err)
			}
		}
		// Stage Claude's companion sidechain directory as well. It is not
		// required for --resume, but silently dropping it loses context.
		srcSidecar := filepath.Join(filepath.Dir(sourcePath), inst.ClaudeSessionID)
		stageSidecar := filepath.Join(j.StageDir, inst.ClaudeSessionID)
		if info, statErr := os.Stat(srcSidecar); statErr == nil && info.IsDir() {
			if _, stageErr := os.Stat(stageSidecar); os.IsNotExist(stageErr) {
				if err := copyDirVerified(srcSidecar, stageSidecar); err != nil {
					return switchFailedNative(journalPath, j, err)
				}
			}
		}
		if got, err := sha256File(stagePath); err != nil || got != sourceHash {
			if err == nil {
				err = fmt.Errorf("staged source hash %s does not match %s", got, sourceHash)
			}
			return switchFailedNative(journalPath, j, err)
		}
		j.State = switchStaged
		if err := writeSwitchJournal(journalPath, j); err != nil {
			return nil, err
		}
	}

	if wasRunning && nativeSwitchRunning(inst) {
		if err := nativeSwitchStop(inst); err != nil {
			return switchFailedNative(journalPath, j, fmt.Errorf("stop source before install: %w", err))
		}
	}
	// KillAndWait flushes the source and confirms the old pane is gone. The
	// provisional pre-copy above is only a rollback snapshot: perform a second
	// exact-ID export now and stage those final bytes before installing anything.
	// A changed path/identity is a failure, never permission to fall back to an
	// mtime scan.
	if sourcePath != "" {
		finalExport, exportErr := ExportClaudeContext(inst, 0)
		if exportErr != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, fmt.Errorf("final source export failed: %w", exportErr))
		}
		sourcePath, sourceHash = finalExport.Manifest.Artifact.Path, finalExport.Manifest.Artifact.SourceSHA256
		j.SourcePath, j.SourceSHA256 = sourcePath, sourceHash
		stagePath := filepath.Join(j.StageDir, filepath.Base(sourcePath))
		if err := os.Remove(stagePath); err != nil && !os.IsNotExist(err) {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		if err := copyFileVerified(sourcePath, stagePath); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		if got, hashErr := sha256File(stagePath); hashErr != nil || got != sourceHash {
			if hashErr == nil {
				hashErr = fmt.Errorf("final staged source hash mismatch")
			}
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, hashErr)
		}
		// Re-stage the final sidecar as well; it may have been flushed on stop.
		stageSidecar := filepath.Join(j.StageDir, inst.ClaudeSessionID)
		if err := os.RemoveAll(stageSidecar); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		srcSidecar := filepath.Join(filepath.Dir(sourcePath), inst.ClaudeSessionID)
		if info, statErr := os.Stat(srcSidecar); statErr == nil && info.IsDir() {
			if err := copyDirVerified(srcSidecar, stageSidecar); err != nil {
				return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
			}
		}
		// Use the project directory selected by the exact exporter. This keeps
		// EffectiveWorkingDir/ProjectPath canonicalization consistent with the
		// resolver used to locate the source.
		dst := filepath.Join(ExpandPath(targetDir), "projects", filepath.Base(filepath.Dir(sourcePath)), filepath.Base(sourcePath))
		j.Destination = dst
		if err := installStagedArtifact(stagePath, dst, sourceHash); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		if info, statErr := os.Stat(stageSidecar); statErr == nil && info.IsDir() {
			if err := installStagedDirectory(stageSidecar, filepath.Join(filepath.Dir(dst), inst.ClaudeSessionID)); err != nil {
				return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
			}
		}
		j.State = switchInstalled
		if err := writeSwitchJournal(journalPath, j); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
	}
	if trustErr := PreAcceptClaudeTrust(filepath.Join(targetDir, ".claude.json"), inst.EffectiveWorkingDir()); trustErr != nil {
		j.Failure = "folder trust pre-seed: " + trustErr.Error()
	}
	oldAccount := inst.Account
	if err := commitNativeSwitchAccount(journalPath, j, inst, preview.TargetAccount, wasRunning); err != nil {
		return nil, err
	}

	conversation := "conversation migrated; native destination readiness has not yet been asserted"
	if sourcePath == "" {
		conversation = "no conversation to migrate (fresh session); target account committed; native destination readiness was not asserted"
	}
	result := nativeHarnessSwitchResult(&HarnessSwitchResult{Preview: preview, OldTool: inst.Tool, NewTool: inst.Tool, OldAccount: oldAccount, NewAccount: inst.Account, Continuity: "native", Conversation: conversation, SourceArtifactSHA256: sourceHash, DestinationPath: j.Destination, Committed: true, LossDisclosure: append([]string(nil), preview.Fidelity.Exclusions...)}, j.Source, j.Target)
	result.nativeJournalPath = journalPath
	if !opts.NoStart && wasRunning {
		if err := startNativeSwitchInstance(inst); err != nil {
			result.Committed = false
			inst.Account = oldAccount
			if restartErr := startNativeSwitchInstance(inst); restartErr != nil {
				j.Failure = fmt.Sprintf("target start failed: %v; source rollback failed: %v", err, restartErr)
			} else {
				j.Failure = "target start failed; source account restored"
			}
			j.State, j.UpdatedAt = switchFailed, time.Now()
			_ = writeSwitchJournal(journalPath, j)
			return result, fmt.Errorf("target Claude session failed to start: %w", err)
		}
		// Start only requests a tmux launch; it is not native readiness
		// evidence. Native account switches have no observer wired into this
		// transaction, so keep readiness explicitly pending rather than
		// inferring it from mutable fields or Start success.
		result.Restarted = true
		result.Conversation = "conversation migrated; target start requested; native destination readiness remains pending"
	}
	// The lifecycle journal remains committed until the caller's scoped storage
	// CAS succeeds and completeNativeHarnessSwitchJournal durably acknowledges
	// that success. Start is never acknowledgement evidence.
	return result, nil
}

func executeNativeCodexSwitch(cfg *UserConfig, inst *Instance, preview *SwitchPreview, opts HarnessSwitchOptions, journalPath string, journal *switchJournal) (*HarnessSwitchResult, error) {
	if cfg == nil || preview.TargetAccount == "" {
		return nil, fmt.Errorf("Codex native resume requires a configured target account")
	}
	targetHome := cfg.GetProfileCodexConfigDir(preview.TargetAccount)
	if targetHome == "" {
		return nil, fmt.Errorf("target Codex account %q is not configured", preview.TargetAccount)
	}
	if preview.TargetAccount == inst.Account {
		return nativeHarnessSwitchResult(&HarnessSwitchResult{Preview: preview, OldTool: inst.Tool, NewTool: inst.Tool, OldAccount: inst.Account, NewAccount: inst.Account, Continuity: "native", Conversation: "no-op: requested account is already active; no restart or native readiness assertion was made", Committed: true, DestinationReady: false}, identityForInstance(inst), identityForInstance(inst)), nil
	}
	if strings.TrimSpace(inst.CodexSessionID) == "" {
		return nil, fmt.Errorf("refusing Codex native switch without an exact Codex session ID")
	}
	export, err := ExportCodexContext(inst, 0)
	if err != nil {
		return nil, fmt.Errorf("exact Codex source preflight failed: %w", err)
	}
	sourcePath, sourceHash := export.Manifest.Artifact.Path, export.Manifest.Artifact.SourceSHA256
	wasRunning := nativeSwitchRunning(inst)
	j := journal
	if j == nil {
		j = &switchJournal{Version: switchJournalVersion, OperationID: switchOperationIDForRequest(inst.ID, switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)), State: switchPrepared, Source: identityForInstance(inst), Target: targetIdentityFor(inst, preview), WasRunning: wasRunning, NoStart: opts.NoStart,
			RequestGeneration: switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)}
	}
	j.SourcePath, j.SourceSHA256 = sourcePath, sourceHash
	// The prepared receipt is the first durable boundary. Do not create a
	// staging directory or copy a rollout until it exists: otherwise a failed
	// first write leaves unjournaled artifacts that recovery cannot account for.
	if j.State == switchPrepared {
		if err := harnessSwitchJournalWrite(journalPath, j); err != nil {
			return nil, fmt.Errorf("persist prepared Codex switch: %w", err)
		}
	}
	if j.StageDir == "" {
		root, rootErr := switchStageRoot(inst.ID)
		if rootErr != nil {
			return nil, rootErr
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
		j.StageDir, err = os.MkdirTemp(root, "codex-")
		if err != nil {
			return nil, err
		}
	}
	stagePath := filepath.Join(j.StageDir, filepath.Base(sourcePath))
	if _, statErr := os.Stat(stagePath); os.IsNotExist(statErr) {
		if err := copyFileVerified(sourcePath, stagePath); err != nil {
			return switchFailedNative(journalPath, j, err)
		}
	}
	if got, hashErr := sha256File(stagePath); hashErr != nil || got != sourceHash {
		if hashErr == nil {
			hashErr = fmt.Errorf("staged Codex rollout hash mismatch")
		}
		return switchFailedNative(journalPath, j, hashErr)
	}
	j.State = switchStaged
	if err := writeSwitchJournal(journalPath, j); err != nil {
		return nil, err
	}
	if wasRunning && nativeSwitchRunning(inst) {
		if err := nativeSwitchStop(inst); err != nil {
			return switchFailedNative(journalPath, j, err)
		}
	}
	// The first staged copy is provisional. Re-export after the synchronous stop
	// so normal-exit flushes are included in the exact final rollout.
	if wasRunning {
		finalExport, exportErr := ExportCodexContext(inst, 0)
		if exportErr != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, fmt.Errorf("final Codex export failed: %w", exportErr))
		}
		sourcePath, sourceHash = finalExport.Manifest.Artifact.Path, finalExport.Manifest.Artifact.SourceSHA256
		j.SourcePath, j.SourceSHA256 = sourcePath, sourceHash
		if err := os.Remove(stagePath); err != nil && !os.IsNotExist(err) {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		if err := copyFileVerified(sourcePath, stagePath); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		if got, hashErr := sha256File(stagePath); hashErr != nil || got != sourceHash {
			if hashErr == nil {
				hashErr = fmt.Errorf("final Codex staged hash mismatch")
			}
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, hashErr)
		}
	}
	rel, err := filepath.Rel(ExpandPath(inst.getCodexHomeDir()), sourcePath)
	if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return switchFailedAfterStop(journalPath, j, inst, wasRunning, fmt.Errorf("Codex rollout is outside source CODEX_HOME"))
	}
	destination := filepath.Join(ExpandPath(targetHome), rel)
	j.Destination = destination
	if err := installStagedArtifact(stagePath, destination, sourceHash); err != nil {
		if wasRunning {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		return switchFailedNative(journalPath, j, err)
	}
	j.State = switchInstalled
	if err := writeSwitchJournal(journalPath, j); err != nil {
		if wasRunning {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		return nil, err
	}
	oldAccount := inst.Account
	oldIdentity := identityForInstance(inst)
	if err := commitNativeSwitchAccount(journalPath, j, inst, preview.TargetAccount, wasRunning); err != nil {
		return nil, err
	}
	result := nativeHarnessSwitchResult(&HarnessSwitchResult{Preview: preview, OldTool: inst.Tool, NewTool: inst.Tool, OldAccount: oldAccount, NewAccount: inst.Account, Continuity: "native", Conversation: "Codex rollout migrated; native destination readiness has not yet been asserted", SourceArtifactSHA256: sourceHash, DestinationPath: destination, Committed: true, LossDisclosure: append([]string(nil), preview.Fidelity.Exclusions...)}, j.Source, j.Target)
	result.nativeJournalPath = journalPath
	if !opts.NoStart && wasRunning {
		if err := startNativeSwitchInstance(inst); err != nil {
			result.Committed = false
			restoreSwitchIdentity(inst, oldIdentity, oldIdentity.Command)
			rollbackErr := startNativeSwitchInstance(inst)
			j.State, j.Failure = switchFailed, err.Error()
			if rollbackErr != nil {
				j.Failure += "; source rollback failed: " + rollbackErr.Error()
			}
			_ = writeSwitchJournal(journalPath, j)
			if rollbackErr != nil {
				return result, fmt.Errorf("%w; source rollback failed: %v", err, rollbackErr)
			}
			return result, err
		}
		// A successful Start is not an observed Codex thread event. Keep this
		// native switch pending until an observer can provide fresh exact
		// evidence; never promote assigned fields to readiness evidence.
		result.Restarted = true
		result.Conversation = "Codex rollout migrated; target start requested; native destination readiness remains pending"
	}
	// The lifecycle journal remains committed until the caller's scoped storage
	// CAS succeeds and completeNativeHarnessSwitchJournal durably acknowledges
	// that success. Start is never acknowledgement evidence.
	return result, nil
}

func executeClaudeCodexSwitch(cfg *UserConfig, inst *Instance, preview *SwitchPreview, opts HarnessSwitchOptions, journalPath string, journal *switchJournal) (*HarnessSwitchResult, error) {
	if preview.TargetAccount != "" && preview.TargetAccountStat != AccountStatusConfigured {
		return nil, fmt.Errorf("Codex target account %q is not configured", preview.TargetAccount)
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultHandoffMaxChars
	}
	wasRunning := inst.Exists()
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, opts.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("exact Claude handoff preflight failed: %w", err)
	}
	export, err := ExportClaudeContext(inst, 0)
	if err != nil {
		return nil, fmt.Errorf("exact Claude identity preflight failed: %w", err)
	}
	beforeHash := export.Manifest.Artifact.SourceSHA256
	j := journal
	if j == nil {
		j = &switchJournal{Version: switchJournalVersion, OperationID: switchOperationIDForRequest(inst.ID, switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)), State: switchPrepared, Source: identityForInstance(inst), Target: targetIdentityFor(inst, preview), WasRunning: wasRunning, NoStart: opts.NoStart,
			RequestGeneration: switchRequestGeneration(identityForInstance(inst), opts.Target, opts.NoStart)}
	}
	j.SourcePath, j.SourceSHA256 = export.Manifest.Artifact.Path, beforeHash
	if j.PromptPath == "" {
		root, err := switchStageRoot(inst.ID)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, err
		}
		j.PromptPath = filepath.Join(root, "codex-handoff.txt")
		if err := os.WriteFile(j.PromptPath, []byte(prompt), 0o600); err != nil {
			return nil, fmt.Errorf("stage Codex handoff: %w", err)
		}
		j.PromptSHA256, err = sha256File(j.PromptPath)
		if err != nil {
			return nil, err
		}
	}
	j.State = switchStaged
	if err := writeSwitchJournal(journalPath, j); err != nil {
		return nil, err
	}
	previousDetectedAt := inst.CodexDetectedAt
	if wasRunning && inst.Exists() {
		if err := inst.KillAndWait(); err != nil {
			return nil, fmt.Errorf("stop source before Codex handoff: %w", err)
		}
	}
	if wasRunning {
		// Rebuild both the exact source manifest and bounded prompt after the
		// synchronous stop; a normal exit may have flushed another turn.
		finalExport, exportErr := ExportClaudeContext(inst, 0)
		if exportErr != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, fmt.Errorf("final Claude export failed: %w", exportErr))
		}
		finalPrompt, finalInfo, promptErr := BuildClaudeToCodexHandoffPrompt(inst, opts.MaxBytes)
		if promptErr != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, fmt.Errorf("final Claude handoff failed: %w", promptErr))
		}
		j.SourcePath, j.SourceSHA256 = finalExport.Manifest.Artifact.Path, finalExport.Manifest.Artifact.SourceSHA256
		beforeHash = j.SourceSHA256
		if err := os.WriteFile(j.PromptPath, []byte(finalPrompt), 0o600); err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		j.PromptSHA256, err = sha256File(j.PromptPath)
		if err != nil {
			return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
		}
		prompt, info = finalPrompt, finalInfo
	}
	old := identityForInstance(inst)
	oldCommand := inst.Command
	inst.Tool, inst.Account, inst.Command = preview.TargetHarness, preview.TargetAccount, "codex"
	inst.ClaudeSessionID, inst.CodexSessionID = "", ""
	inst.CodexDetectedAt = time.Time{}
	j.State = switchCommitted
	if err := writeSwitchJournal(journalPath, j); err != nil {
		restoreSwitchIdentity(inst, old, oldCommand)
		return switchFailedAfterStop(journalPath, j, inst, wasRunning, err)
	}
	result := &HarnessSwitchResult{Preview: preview, OldTool: old.Tool, NewTool: inst.Tool, OldAccount: old.Account, NewAccount: inst.Account, Continuity: "transferred", Conversation: fmt.Sprintf("context handoff staged from %s (%d messages; truncated=%v)", info.TranscriptPath, info.IncludedCount, info.Truncated), SourceArtifactSHA256: beforeHash, Committed: true, LossDisclosure: append([]string(nil), preview.Fidelity.Exclusions...)}
	if !opts.NoStart && wasRunning {
		if err := inst.StartWithMessage(prompt); err != nil {
			result.Committed = false
			restoreSwitchIdentity(inst, old, oldCommand)
			rollbackErr := inst.Start()
			j.State, j.Failure = switchFailed, fmt.Sprintf("Codex destination start failed: %v", err)
			if rollbackErr != nil {
				j.Failure += "; source rollback failed: " + rollbackErr.Error()
			}
			_ = writeSwitchJournal(journalPath, j)
			return result, fmt.Errorf("Codex handoff failed: %w", err)
		}
		if err := verifyNativeEvent(inst, preview.TargetHarness, preview.TargetAccount, previousDetectedAt); err != nil {
			result.Committed = false
			restoreSwitchIdentity(inst, old, oldCommand)
			rollbackErr := inst.Start()
			j.State, j.Failure = switchFailed, err.Error()
			if rollbackErr != nil {
				j.Failure += "; source rollback failed: " + rollbackErr.Error()
			}
			_ = writeSwitchJournal(journalPath, j)
			if rollbackErr != nil {
				return result, fmt.Errorf("%w; source rollback failed: %v", err, rollbackErr)
			}
			return result, err
		}
		j.Target.SessionID, j.Target.CodexID = inst.CodexSessionID, inst.CodexSessionID
		j.TargetObserved, j.DestinationReady = true, true
		result.Restarted, result.DestinationReady = true, true
	}
	if opts.NoStart || !wasRunning {
		j.State, j.DestinationReady, j.TargetObserved = switchCommitted, false, false
		j.UpdatedAt = time.Now()
		if err := writeSwitchJournal(journalPath, j); err != nil {
			return result, err
		}
		_ = info
		return result, nil
	}
	j.State, j.UpdatedAt = switchCompleted, time.Now()
	if err := writeSwitchJournal(journalPath, j); err != nil {
		return result, err
	}
	_ = info // metadata is represented by the preview's loss disclosure and journal hash.
	cleanupCompletedSwitchArtifacts(j)
	return result, nil
}

// Narrow seams keep native executor regressions filesystem-only: tests inject
// lifecycle state, stop/start outcomes, and journal failures without creating
// a tmux session.
var (
	nativeSwitchStart         = func(inst *Instance) error { return inst.Start() }
	nativeSwitchRunning       = func(inst *Instance) bool { return inst.Exists() }
	nativeSwitchStop          = func(inst *Instance) error { return inst.KillAndWait() }
	harnessSwitchJournalWrite = writeSwitchJournal
)

func startNativeSwitchInstance(inst *Instance) error {
	return nativeSwitchStart(inst)
}

// commitNativeSwitchAccount is the last durable boundary before a native
// target can start. If writing it fails after the source was stopped, restore
// the complete original identity and restart the source before returning.
func commitNativeSwitchAccount(path string, j *switchJournal, inst *Instance, account string, wasRunning bool) error {
	if inst == nil || j == nil {
		return fmt.Errorf("native switch commit is missing source identity")
	}
	inst.Account = account
	j.State = switchCommitted
	if err := harnessSwitchJournalWrite(path, j); err != nil {
		returnCause := fmt.Errorf("persist native switch commit: %w", err)
		_, rollbackErr := switchFailedAfterStop(path, j, inst, wasRunning, returnCause)
		return rollbackErr
	}
	return nil
}

func nativeHarnessSwitchResult(result *HarnessSwitchResult, source, target switchIdentity) *HarnessSwitchResult {
	if result == nil {
		return nil
	}
	result.nativeSource, result.nativeTarget = source, target
	return result
}

// completeNativeHarnessSwitchJournal acknowledges the storage CAS that follows
// a native lifecycle operation. Until Storage.CommitNativeHarnessSwitch has
// atomically persisted the exact source-to-target mutation, a committed
// journal remains deliberately non-terminal and blocks another switch.
func completeNativeHarnessSwitchJournal(result *HarnessSwitchResult) error {
	if result == nil || result.nativeJournalPath == "" {
		return nil
	}
	journal, err := loadSwitchJournal(result.nativeJournalPath)
	if err != nil {
		return fmt.Errorf("load native switch journal after storage commit: %w", err)
	}
	if journal == nil {
		return fmt.Errorf("native switch journal disappeared before storage acknowledgement")
	}
	if journal.State == switchCompleted {
		return nil
	}
	if journal.State != switchCommitted {
		return fmt.Errorf("native switch journal is %q before storage acknowledgement", journal.State)
	}
	if !sameSwitchIdentity(journal.Source, result.nativeSource) || !sameSwitchIdentity(journal.Target, result.nativeTarget) {
		return fmt.Errorf("native switch journal identity changed before storage acknowledgement")
	}
	journal.State, journal.UpdatedAt = switchCompleted, time.Now()
	if err := harnessSwitchJournalWrite(result.nativeJournalPath, journal); err != nil {
		return fmt.Errorf("persist native switch storage acknowledgement: %w", err)
	}
	cleanupCompletedSwitchArtifacts(journal)
	return nil
}

func sameSwitchIdentity(left, right switchIdentity) bool {
	return left.InstanceID == right.InstanceID &&
		left.Tool == right.Tool &&
		left.StorageTool == right.StorageTool &&
		left.SessionID == right.SessionID &&
		left.ClaudeID == right.ClaudeID &&
		left.CodexID == right.CodexID &&
		left.Account == right.Account &&
		left.ProjectPath == right.ProjectPath &&
		left.WorkingDir == right.WorkingDir &&
		left.Title == right.Title &&
		left.GroupPath == right.GroupPath &&
		left.Command == right.Command &&
		left.Status == right.Status &&
		left.LastStartedAt.Equal(right.LastStartedAt) &&
		left.Generation == right.Generation
}

func cleanupCompletedSwitchArtifacts(j *switchJournal) {
	if j == nil {
		return
	}
	// Only remove this operation's private staging directory. Never remove the
	// source artifact or destination, and leave failed/incomplete journals for
	// explicit recovery.
	if j.StageDir != "" {
		_ = os.RemoveAll(j.StageDir)
	}
	if j.PromptPath != "" && !strings.HasPrefix(filepath.Clean(j.PromptPath), filepath.Clean(j.StageDir)+string(os.PathSeparator)) {
		_ = os.Remove(j.PromptPath)
	}
}

func switchFailedNative(path string, j *switchJournal, err error) (*HarnessSwitchResult, error) {
	j.State, j.Failure, j.UpdatedAt = switchFailed, err.Error(), time.Now()
	_ = writeSwitchJournal(path, j)
	return nil, err
}

func switchFailedAfterStop(path string, j *switchJournal, inst *Instance, wasRunning bool, cause error) (*HarnessSwitchResult, error) {
	// A failed post-stop commit must never leave the in-memory source carrying
	// the target identity. Restoration is independent of lifecycle state; only
	// restarting is conditional on a source that was running before the switch.
	restoreSwitchIdentity(inst, j.Source, j.Source.Command)
	var restoreErr error
	if wasRunning {
		restoreErr = startNativeSwitchInstance(inst)
	}
	j.State, j.Failure, j.UpdatedAt = switchFailed, cause.Error(), time.Now()
	if restoreErr != nil {
		j.Failure += "; source restore failed: " + restoreErr.Error()
		cause = fmt.Errorf("%w; source restore failed: %v", cause, restoreErr)
	}
	if err := writeSwitchJournal(path, j); err != nil {
		cause = fmt.Errorf("%w; failed to persist switch failure: %v", cause, err)
	}
	return nil, cause
}

func installStagedDirectory(stage, destination string) error {
	if err := ensureNoSymlinkPath(destination); err != nil {
		return err
	}
	return filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if info, statErr := os.Lstat(target); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("refusing sidecar destination: %s", target)
			}
			want, hashErr := sha256File(path)
			if hashErr != nil {
				return hashErr
			}
			got, hashErr := sha256File(target)
			if hashErr != nil {
				return hashErr
			}
			if got != want {
				return fmt.Errorf("sidecar destination already differs: %s", target)
			}
			return nil
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return copyFileVerified(path, target)
	})
}

// installStagedArtifact installs an exact staged artifact without silently
// clobbering a destination conversation. An identical destination is already
// installed. A shorter destination may be advanced only when it is a strict
// byte prefix of the staged exact source; its own fsynced .bak- snapshot is
// retained before an atomic replacement. Diverged or newer destinations are
// left intact alongside the staged source for explicit recovery.
func installStagedArtifact(stage, destination, expectedHash string) error {
	if expectedHash == "" {
		return fmt.Errorf("staged source hash is required")
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		return fmt.Errorf("inspect staged artifact: %w", err)
	}
	if stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.Mode().IsRegular() {
		return fmt.Errorf("staged artifact is not a regular file: %s", stage)
	}
	stageHash, err := sha256File(stage)
	if err != nil {
		return err
	}
	if stageHash != expectedHash {
		return fmt.Errorf("staged source hash mismatch")
	}
	if err := ensureNoSymlinkPath(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("unsafe destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	// All Agent Deck writers use this destination-specific interprocess lock.
	// The final hash revalidation below makes an external change a bounded
	// refusal rather than permission to overwrite bytes we did not snapshot.
	lock, err := AcquireConfigFileLock(destination + ".switch-install")
	if err != nil {
		return fmt.Errorf("lock destination install: %w", err)
	}
	defer lock.Release()

	info, err := os.Lstat(destination)
	if os.IsNotExist(err) {
		if err := copyFileVerified(stage, destination); err != nil {
			return err
		}
		got, err := sha256File(destination)
		if err != nil || got != expectedHash {
			if err == nil {
				err = fmt.Errorf("destination hash mismatch")
			}
			return err
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reinspect destination artifact: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing destination artifact: %s", destination)
	}
	destinationHash, err := sha256File(destination)
	if err != nil {
		return err
	}
	if destinationHash == expectedHash {
		return nil
	}

	isPrefix, err := strictBytePrefix(destination, stage)
	if err != nil {
		return fmt.Errorf("validate destination ancestry: %w", err)
	}
	if !isPrefix {
		return fmt.Errorf("destination contains a divergent or newer conversation; preserving both files and refusing overwrite: %s", destination)
	}
	backup, err := snapshotConversationArtifact(destination, destinationHash)
	if err != nil {
		return err
	}
	// A concurrent non-cooperating writer cannot be safely overwritten. Keep
	// the durable snapshot and require an explicit retry/recovery instead.
	currentHash, err := sha256File(destination)
	if err != nil {
		return err
	}
	if currentHash != destinationHash {
		return fmt.Errorf("destination changed during safe append preparation; preserved snapshot at %s and refusing overwrite", backup)
	}

	tmp, err := os.CreateTemp(filepath.Dir(destination), ".switch-install-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := copyFileVerified(stage, tmpPath); err != nil {
		return err
	}
	if err := syncRegularFile(tmpPath); err != nil {
		return fmt.Errorf("sync replacement artifact: %w", err)
	}
	currentHash, err = sha256File(destination)
	if err != nil {
		return err
	}
	if currentHash != destinationHash {
		return fmt.Errorf("destination changed before safe append install; preserved snapshot at %s and refusing overwrite", backup)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("atomically install advanced conversation: %w", err)
	}
	fsyncDir(filepath.Dir(destination))
	got, err := sha256File(destination)
	if err != nil || got != expectedHash {
		if err == nil {
			err = fmt.Errorf("destination hash mismatch after safe append install")
		}
		return err
	}
	return nil
}

func strictBytePrefix(prefixPath, fullPath string) (bool, error) {
	prefixInfo, err := os.Lstat(prefixPath)
	if err != nil {
		return false, err
	}
	fullInfo, err := os.Lstat(fullPath)
	if err != nil {
		return false, err
	}
	if !prefixInfo.Mode().IsRegular() || prefixInfo.Mode()&os.ModeSymlink != 0 || !fullInfo.Mode().IsRegular() || fullInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("ancestry comparison requires regular files")
	}
	if prefixInfo.Size() >= fullInfo.Size() {
		return false, nil
	}
	prefix, err := os.Open(prefixPath)
	if err != nil {
		return false, err
	}
	defer prefix.Close()
	full, err := os.Open(fullPath)
	if err != nil {
		return false, err
	}
	defer full.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := prefix.Read(buf)
		if n > 0 {
			fullBuf := make([]byte, n)
			if _, err := io.ReadFull(full, fullBuf); err != nil {
				return false, err
			}
			if !bytes.Equal(buf[:n], fullBuf) {
				return false, nil
			}
		}
		if readErr == io.EOF {
			return true, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
}

func snapshotConversationArtifact(destination, expectedHash string) (string, error) {
	for n := 0; ; n++ {
		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		if n > 0 {
			suffix = fmt.Sprintf("%s-%d", suffix, n)
		}
		backup := destination + ".bak-" + suffix
		f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reserve destination backup: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		if err := copyFileVerified(destination, backup); err != nil {
			_ = os.Remove(backup)
			return "", fmt.Errorf("snapshot destination conversation: %w", err)
		}
		if err := syncRegularFile(backup); err != nil {
			return "", fmt.Errorf("sync destination backup: %w", err)
		}
		fsyncDir(filepath.Dir(backup))
		backupHash, err := sha256File(backup)
		if err != nil {
			return "", err
		}
		if backupHash != expectedHash {
			return "", fmt.Errorf("destination changed while snapshotting; preserved snapshot at %s and refusing overwrite", backup)
		}
		return backup, nil
	}
}

func syncRegularFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func verifyNativeEvent(inst *Instance, harness, account string, previousDetectedAt time.Time) error {
	if inst == nil || !inst.Exists() {
		return fmt.Errorf("destination harness is not running")
	}
	if inst.Tool != harness || inst.Account != account {
		return fmt.Errorf("destination mutable fields do not match requested target")
	}
	var id string
	var detectedAt time.Time
	if IsCodexCompatible(harness) {
		id, detectedAt = inst.CodexSessionID, inst.CodexDetectedAt
	} else if IsClaudeCompatible(harness) {
		id, detectedAt = inst.ClaudeSessionID, inst.ClaudeDetectedAt
	} else {
		return fmt.Errorf("no native readiness verifier for %q", harness)
	}
	if strings.TrimSpace(id) == "" || !detectedAt.After(previousDetectedAt) {
		return fmt.Errorf("destination lacks a fresh native identity event")
	}
	return nil
}

func restoreSwitchIdentity(inst *Instance, old switchIdentity, oldCommand string) {
	if inst == nil {
		return
	}
	inst.Tool, inst.Account, inst.Command = old.Tool, old.Account, oldCommand
	inst.ProjectPath, inst.Title, inst.GroupPath = old.ProjectPath, old.Title, old.GroupPath
	inst.ClaudeSessionID, inst.CodexSessionID = old.ClaudeID, old.CodexID
	if inst.ClaudeSessionID == "" && inst.CodexSessionID == "" {
		// Compatibility with journals written before per-tool IDs were split.
		if IsClaudeCompatible(old.Tool) {
			inst.ClaudeSessionID = old.SessionID
		} else if IsCodexCompatible(old.Tool) {
			inst.CodexSessionID = old.SessionID
		}
	}
}

func identityForInstance(inst *Instance) switchIdentity {
	if inst == nil {
		return switchIdentity{}
	}
	tool := canonicalSwitchHarness(inst.Tool)
	if tool == "" {
		tool = strings.ToLower(strings.TrimSpace(inst.Tool))
	}
	return switchIdentity{InstanceID: inst.ID, Tool: tool, StorageTool: inst.Tool, SessionID: resolveSourceSessionID(inst), ClaudeID: inst.ClaudeSessionID, CodexID: inst.CodexSessionID, Account: strings.TrimSpace(inst.Account), ProjectPath: canonicalSwitchPath(inst.ProjectPath), WorkingDir: canonicalSwitchPath(inst.EffectiveWorkingDir()), Title: inst.Title, GroupPath: inst.GroupPath, Command: inst.Command, Status: inst.Status, LastStartedAt: inst.LastStartedAt}
}

func canonicalSwitchPath(path string) string {
	path = strings.TrimSpace(ExpandPath(path))
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func targetIdentityFor(inst *Instance, preview *SwitchPreview) switchIdentity {
	id := identityForInstance(inst)
	id.Tool = canonicalSwitchHarness(preview.TargetHarness)
	if id.Tool == "" {
		id.Tool = strings.ToLower(strings.TrimSpace(preview.TargetHarness))
	}
	id.Account = strings.TrimSpace(preview.TargetAccount)
	id.Generation = "target"
	if IsClaudeCompatible(preview.TargetHarness) {
		id.SessionID, id.ClaudeID, id.CodexID = inst.ClaudeSessionID, inst.ClaudeSessionID, ""
	} else if IsCodexCompatible(preview.TargetHarness) {
		id.SessionID, id.ClaudeID, id.CodexID = inst.CodexSessionID, "", inst.CodexSessionID
	} else {
		id.SessionID, id.ClaudeID, id.CodexID = "", "", ""
	}
	return id
}
func validSwitchIdentity(id string) bool {
	return id != "" && filepath.Base(id) == id && !strings.ContainsRune(id, '\x00')
}
func switchOperationID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:12])
}
func switchOperationIDForRequest(id, requestGeneration string) string {
	return switchOperationID(id) + "-" + requestGeneration[:12]
}

// switchJournalPath is the version-1 single-journal location. It remains
// readable so an interrupted older operation can be recovered without moving
// or overwriting its evidence.
func switchJournalPath(id string) (string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, switchOperationID(id)+".json"), nil
}

// harnessSwitchSourceLockPath is stable for one source instance, unlike the
// request journal path which intentionally varies by destination. Cross-harness
// transactions use this same lock before they construct a distinct target.
func harnessSwitchSourceLockPath(id string) (string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, switchOperationID(id)+".source-transaction"), nil
}

func acquireHarnessSwitchSourceLock(id string) (*ConfigFileLock, error) {
	path, err := harnessSwitchSourceLockPath(id)
	if err != nil {
		return nil, err
	}
	return AcquireConfigFileLock(path)
}

func switchJournalPathForRequest(id, requestGeneration string) (string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, switchOperationIDForRequest(id, requestGeneration)+".json"), nil
}

// completedSwitchJournalForRecovery finds exactly one completed version-2
// journal that still binds this exact source and target. Version 2 recorded
// exact IDs and working directories, but not StorageTool; receipts missing
// any of that identity evidence are deliberately not recovered. It returns a
// memory-only copy with StorageTool = Tool inferred; the original journal is
// never rewritten.
func completedSwitchJournalForRecovery(id, exceptPath string, inst *Instance, preview *SwitchPreview, opts HarnessSwitchOptions) (*switchJournal, string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	prefix := switchOperationID(id) + "-"
	var matched *switchJournal
	var matchedPath string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path == exceptPath {
			continue
		}
		journal, loadErr := loadSwitchJournal(path)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if journal == nil || journal.Version != 2 || journal.State != switchCompleted ||
			journal.Source.SessionID == "" || journal.Target.SessionID == "" ||
			journal.Source.WorkingDir == "" || journal.Target.WorkingDir == "" ||
			!journalMatchesRequest(journal, inst, preview, opts) {
			continue
		}
		if matched != nil {
			return nil, "", fmt.Errorf("refusing recovery: multiple compatible completed legacy operations are preserved at %s and %s", matchedPath, path)
		}
		// This copy is the recovery capability passed to the scoped storage CAS;
		// never fill fields into or rewrite the historical receipt on disk.
		copyJournal := *journal
		copyJournal.Source = journal.Source
		copyJournal.Target = journal.Target
		if copyJournal.Source.StorageTool == "" {
			copyJournal.Source.StorageTool = copyJournal.Source.Tool
		}
		if copyJournal.Target.StorageTool == "" {
			copyJournal.Target.StorageTool = copyJournal.Target.Tool
		}
		matched, matchedPath = &copyJournal, path
	}
	return matched, matchedPath, nil
}

// committedNativeJournalForAcknowledgement finds an exact native target that
// was already written by the scoped storage CAS, but whose journal completion
// write failed. It deliberately returns only version-3 same-harness journals:
// old nonterminal formats are uncertain evidence and are never adopted.
func committedNativeJournalForAcknowledgement(id string, inst *Instance) (*switchJournal, string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return nil, "", err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	prefix := switchOperationID(id) + "-"
	var matched *switchJournal
	var matchedPath string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		journal, loadErr := loadSwitchJournal(path)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if journal == nil || journal.Version < switchJournalVersion || journal.State != switchCommitted ||
			normalizedJournalHarness(journal.Source.Tool) != normalizedJournalHarness(journal.Target.Tool) ||
			!targetIdentityMatches(journal.Target, inst) || !instanceSnapshotMatchesNativeTarget(inst, journal.Target) {
			continue
		}
		if matched != nil {
			return nil, "", fmt.Errorf("refusing storage acknowledgement: multiple committed native journals match target identity at %s and %s", matchedPath, path)
		}
		matched, matchedPath = journal, path
	}
	return matched, matchedPath, nil
}

// incompleteSwitchJournalPath finds a distinct versioned operation that has
// not reached a terminal state. Different request paths deliberately retain
// historical completed journals, but they must not permit concurrent recovery
// or a second uncertain start for the same session.
func incompleteSwitchJournalPath(id, exceptPath string) (string, error) {
	root, err := runtimeDataPath("harness-switch")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	prefix := switchOperationID(id) + "-"
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if path == exceptPath {
			continue
		}
		journal, loadErr := loadSwitchJournal(path)
		if loadErr != nil {
			return "", loadErr
		}
		if journal != nil && journal.State != switchCompleted && journal.State != switchFailed {
			return path, nil
		}
	}
	return "", nil
}

func switchStageRoot(id string) (string, error) {
	root, err := runtimeDataPath("harness-switch-staging")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, switchOperationID(id)), nil
}

func loadSwitchJournal(path string) (*switchJournal, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j switchJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("read switch journal: %w", err)
	}
	return &j, nil
}
func writeSwitchJournal(path string, j *switchJournal) error {
	if j == nil {
		return fmt.Errorf("nil switch journal")
	}
	j.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".switch-journal-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := fsyncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	fsyncDir(filepath.Dir(path))
	return nil
}

// switchRequestGeneration is the durable recovery binding. It excludes only
// ephemeral status, so a waiting -> idle refresh cannot poison a prepared
// retry. The exact native IDs, account, cwd, title, group, command, and launch
// incarnation are immutable and a real external edit cannot be replayed. The
// JSON payload is canonical rather than fmt's Go-value
// rendering: time zones, locations, and stripped monotonic readings therefore
// hash identically after a JSON disk round-trip.
func switchRequestGeneration(source switchIdentity, target SwitchPreviewTarget, noStart bool) string {
	harness := canonicalSwitchHarness(target.Harness)
	if harness == "" {
		harness = canonicalSwitchHarness(source.Tool)
	}
	if harness == "" {
		harness = strings.ToLower(strings.TrimSpace(target.Harness))
	}
	payload := struct {
		Version       int    `json:"version"`
		InstanceID    string `json:"instance_id"`
		Tool          string `json:"tool"`
		StorageTool   string `json:"storage_tool"`
		SessionID     string `json:"session_id"`
		ClaudeID      string `json:"claude_session_id"`
		CodexID       string `json:"codex_session_id"`
		Account       string `json:"account"`
		ProjectPath   string `json:"project_path"`
		WorkingDir    string `json:"working_dir"`
		Title         string `json:"title"`
		GroupPath     string `json:"group_path"`
		Command       string `json:"command"`
		LaunchAtUTC   string `json:"launch_at_utc"`
		TargetHarness string `json:"target_harness"`
		TargetAccount string `json:"target_account"`
		NoStart       bool   `json:"no_start"`
	}{
		Version: 3, InstanceID: source.InstanceID, Tool: canonicalSwitchHarness(source.Tool), StorageTool: source.StorageTool, SessionID: source.SessionID,
		ClaudeID: source.ClaudeID, CodexID: source.CodexID, Account: strings.TrimSpace(source.Account),
		ProjectPath: canonicalSwitchPath(source.ProjectPath), WorkingDir: canonicalSwitchPath(source.WorkingDir),
		Title: source.Title, GroupPath: source.GroupPath, Command: source.Command,
		LaunchAtUTC: source.LastStartedAt.UTC().Format(time.RFC3339Nano), TargetHarness: harness,
		TargetAccount: strings.TrimSpace(target.Account), NoStart: noStart,
	}
	if payload.Tool == "" {
		payload.Tool = strings.ToLower(strings.TrimSpace(source.Tool))
	}
	data, _ := json.Marshal(payload) // This fixed struct cannot fail to marshal.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

func normalizedJournalHarness(harness string) string {
	if normalized := canonicalSwitchHarness(harness); normalized != "" {
		return normalized
	}
	return strings.ToLower(strings.TrimSpace(harness))
}

func targetIdentityMatches(j switchIdentity, inst *Instance) bool {
	if inst == nil {
		return false
	}
	current := identityForInstance(inst)
	return j.InstanceID == current.InstanceID && normalizedJournalHarness(j.Tool) == current.Tool && (j.StorageTool == "" || j.StorageTool == current.StorageTool) && j.Account == current.Account &&
		j.ProjectPath == current.ProjectPath && (j.WorkingDir == "" || j.WorkingDir == current.WorkingDir) &&
		j.Title == current.Title && j.GroupPath == current.GroupPath && j.Command == current.Command &&
		j.SessionID == current.SessionID
}

// instanceSnapshotMatchesNativeTarget distinguishes an in-memory lifecycle
// target (whose last loaded row is still the source) from a target actually
// observed after the storage CAS. ConfirmNativeHarnessSwitchTarget performs the
// authoritative database check again before the journal is acknowledged.
func instanceSnapshotMatchesNativeTarget(inst *Instance, target switchIdentity) bool {
	if inst == nil || inst.storageSnapshot == nil || inst.storageSnapshot.stored == nil {
		return false
	}
	stored := inst.storageSnapshot.stored
	if stored.ID != target.InstanceID || stored.Tool != target.StorageTool || stored.Account != target.Account || stored.ProjectPath != target.ProjectPath || stored.Command != target.Command {
		return false
	}
	var ids struct {
		ClaudeSessionID string `json:"claude_session_id"`
		CodexSessionID  string `json:"codex_session_id"`
	}
	if err := json.Unmarshal(stored.ToolData, &ids); err != nil {
		return false
	}
	return ids.ClaudeSessionID == target.ClaudeID && ids.CodexSessionID == target.CodexID
}

// sourceRecoveryIdentityMatches is intentionally narrower than the TUI modal
// guard. Durable recovery accepts only ephemeral status churn, never a changed
// native ID, account, cwd, title, group, command, or launch incarnation.
func sourceRecoveryIdentityMatches(j switchIdentity, inst *Instance) bool {
	if inst == nil {
		return false
	}
	current := identityForInstance(inst)
	return j.InstanceID == current.InstanceID && normalizedJournalHarness(j.Tool) == current.Tool && (j.StorageTool == "" || j.StorageTool == current.StorageTool) && j.SessionID == current.SessionID &&
		j.ClaudeID == current.ClaudeID && j.CodexID == current.CodexID && j.Account == current.Account &&
		j.ProjectPath == current.ProjectPath && (j.WorkingDir == "" || j.WorkingDir == current.WorkingDir) &&
		j.Title == current.Title && j.GroupPath == current.GroupPath && j.Command == current.Command &&
		j.LastStartedAt.Equal(current.LastStartedAt)
}

// SwitchModalIdentity is a UI-only stale-confirmation guard. Unlike durable
// recovery, it rejects any display or lifecycle refresh made while a modal was
// open. Its fields must not be added to switchRequestGeneration.
type SwitchModalIdentity struct {
	identity switchIdentity
}

func CaptureSwitchModalIdentity(inst *Instance) SwitchModalIdentity {
	return SwitchModalIdentity{identity: identityForInstance(inst)}
}

func (captured SwitchModalIdentity) Matches(inst *Instance) bool {
	current := identityForInstance(inst)
	return captured.identity.InstanceID == current.InstanceID && captured.identity.Tool == current.Tool &&
		captured.identity.Account == current.Account && captured.identity.ProjectPath == current.ProjectPath &&
		captured.identity.WorkingDir == current.WorkingDir && captured.identity.Title == current.Title &&
		captured.identity.GroupPath == current.GroupPath && captured.identity.Command == current.Command &&
		captured.identity.Status == current.Status && captured.identity.ClaudeID == current.ClaudeID &&
		captured.identity.CodexID == current.CodexID && captured.identity.LastStartedAt.Equal(current.LastStartedAt)
}

func journalMatchesRequest(j *switchJournal, inst *Instance, preview *SwitchPreview, options ...HarnessSwitchOptions) bool {
	if j == nil || inst == nil || preview == nil || j.Version < 1 || j.RequestGeneration == "" {
		return false
	}
	noStart := false
	if len(options) > 0 {
		noStart = options[0].NoStart
	}
	target := SwitchPreviewTarget{Harness: preview.TargetHarness, Account: preview.TargetAccount}
	targetHarness := normalizedJournalHarness(target.Harness)
	if normalizedJournalHarness(j.Target.Tool) != targetHarness || j.Target.Account != strings.TrimSpace(target.Account) || j.NoStart != noStart {
		return false
	}
	if j.State == switchCompleted {
		// The lifecycle can finish before the scoped registry CAS. Accept either
		// the immutable target (already persisted) or exact source (CAS retry),
		// so repair never repeats stop/start/prompt work. DestinationReady is
		// never inferred from Start.
		return ((!j.NoStart && targetIdentityMatches(j.Target, inst)) || sourceRecoveryIdentityMatches(j.Source, inst)) && journalGenerationMatches(j, target, noStart)
	}
	return sourceRecoveryIdentityMatches(j.Source, inst) && journalGenerationMatches(j, target, noStart)
}

func journalGenerationMatches(j *switchJournal, target SwitchPreviewTarget, noStart bool) bool {
	if j.Version < switchJournalVersion {
		// Earlier versions used a different request payload (version 1 also used
		// fmt's representation of time.Time). Preserve their evidence and recover
		// only after the durable field comparison above; recomputing a newer
		// digest would reject an otherwise exact retry.
		return true
	}
	return j.RequestGeneration != "" && j.RequestGeneration == switchRequestGeneration(j.Source, target, noStart)
}

func journalRequestMismatchReason(j *switchJournal, inst *Instance, preview *SwitchPreview, options ...HarnessSwitchOptions) string {
	if j == nil || inst == nil || preview == nil {
		return "journal or session is incomplete"
	}
	if !sourceRecoveryIdentityMatches(j.Source, inst) {
		return "source immutable identity changed; journal is preserved and will not be replayed"
	}
	noStart := false
	if len(options) > 0 {
		noStart = options[0].NoStart
	}
	if j.NoStart != noStart {
		return "restart mode differs from the prepared operation; retry with the original --no-start setting or recover the preserved journal"
	}
	targetHarness := normalizedJournalHarness(preview.TargetHarness)
	if normalizedJournalHarness(j.Target.Tool) != targetHarness || j.Target.Account != strings.TrimSpace(preview.TargetAccount) {
		return "target harness/account differs from the prepared operation; journal is preserved and will not be replayed"
	}
	if j.Version >= switchJournalVersion && !journalGenerationMatches(j, SwitchPreviewTarget{Harness: preview.TargetHarness, Account: preview.TargetAccount}, noStart) {
		return "journal request binding is missing or corrupt; journal is preserved and will not be replayed"
	}
	return "journal identity does not match this request; journal is preserved and will not be replayed"
}

func resultFromCommittedJournal(preview *SwitchPreview, j *switchJournal, journalPath string, storageAlreadyCommitted bool) *HarnessSwitchResult {
	result := resultFromCompletedJournal(preview, j)
	result.nativeJournalPath = journalPath
	result.nativeStorageAcknowledgement = storageAlreadyCommitted
	return result
}

func resultFromCompletedJournal(preview *SwitchPreview, j *switchJournal) *HarnessSwitchResult {
	conversation := "switch recovered from completed journal"
	if !j.DestinationReady {
		conversation += "; native destination readiness remains pending"
	}
	return nativeHarnessSwitchResult(&HarnessSwitchResult{Preview: preview, OldTool: j.Source.Tool, NewTool: j.Target.Tool, OldAccount: j.Source.Account, NewAccount: j.Target.Account, Continuity: map[bool]string{true: "native", false: "transferred"}[IsClaudeCompatible(j.Target.Tool)], Conversation: conversation, SourceArtifactSHA256: j.SourceSHA256, DestinationPath: j.Destination, DestinationReady: j.DestinationReady, Committed: true, LossDisclosure: append([]string(nil), preview.Fidelity.Exclusions...)}, j.Source, j.Target)
}
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
