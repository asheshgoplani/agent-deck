package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"time"
)

// CrossHarnessTargetStore persists only the distinct target. Implementations
// must never replace or update the source record as part of a transfer.
type CrossHarnessTargetStore interface {
	LoadTarget(id string) (*Instance, error)
	CreateTarget(target *Instance) error
	SaveTarget(target *Instance) error
	// FinalizeTargetHandoff atomically archives the still-intact source and
	// reveals the already-ready destination. It must be scoped to these two
	// rows; a full registry save is not a valid implementation.
	FinalizeTargetHandoff(source, target *Instance) error
}

// CrossHarnessTargetLifecycle starts a target through the application's normal
// lifecycle ownership. It intentionally has no Stop method: a failed or
// unverified transfer must not signal either the source or target implicitly.
type CrossHarnessTargetLifecycle interface {
	// StartTarget must execute exactly the supplied immutable plan. It must not
	// resolve an account, home, cwd, native arguments, or identity again.
	StartTarget(ctx context.Context, target *Instance, plan *FreshTargetLaunchPlan) error
}

// CrossHarnessTargetObserver supplies evidence emitted by the target harness.
// Ready is insufficient by itself: the evidence must correlate to the exact
// target instance and native identity before a transfer can be completed.
type CrossHarnessTargetObserver interface {
	ObserveTarget(ctx context.Context, target *Instance, expected FreshTargetIdentity) (CrossHarnessTargetEvidence, error)
}

// CrossHarnessTargetObservationPreparer is optional. Native observers use it
// to capture a pre-launch evidence lower bound; older injected observers remain
// valid and are still required to provide independent native evidence.
type CrossHarnessTargetObservationPreparer interface {
	BeginTargetObservation(target *Instance, expected FreshTargetIdentity)
}

// CrossHarnessTargetObservationJournaler supplies a durable pre-launch
// baseline. The optional interface keeps older injected observers compatible,
// while the production observer uses it to retain Pi's pre-launch native
// identity through uncertain-launch recovery.
type CrossHarnessTargetObservationJournaler interface {
	PrepareTargetObservation(target *Instance, expected FreshTargetIdentity) crossHarnessObservationBaseline
	RestoreTargetObservation(target *Instance, baseline crossHarnessObservationBaseline)
}

type CrossHarnessTargetEvidence struct {
	InstanceID   string
	Tool         string
	SessionID    string
	ArtifactPath string
	Ready        bool
	// Evidence names the independent native signal used by the observer; it is
	// diagnostic only and never substitutes for identity correlation.
	Evidence string
}

type CrossHarnessSwitchOptions struct {
	Target   SwitchPreviewTarget
	MaxBytes int
	NoStart  bool
	Timeout  time.Duration
	// SourceSnapshot is retained for preview compatibility only. Execution
	// ignores it and requires SourceOwnership to re-read current state.
	SourceSnapshot *SwitchSourceSnapshot
}

// CrossHarnessSourceOwnershipValidator re-reads the current source row,
// dependent-child graph, and watcher routing. It is mandatory for execution:
// a caller-owned preview snapshot is advisory only and must never authorize a
// source replacement after it has become stale.
//
// Watcher routing lives outside the session database. Validation is therefore
// deliberately repeated before staging and final commit, but cannot make the
// external watcher read atomic with the database supersession transaction.
type CrossHarnessSourceOwnershipValidator interface {
	ValidateCrossHarnessSource(source *Instance) (SwitchSourceSnapshot, error)
}

// CrossHarnessSourceOwnershipValidatorFunc adapts a current-state validator
// without making ownership validation optional.
type CrossHarnessSourceOwnershipValidatorFunc func(source *Instance) (SwitchSourceSnapshot, error)

func (f CrossHarnessSourceOwnershipValidatorFunc) ValidateCrossHarnessSource(source *Instance) (SwitchSourceSnapshot, error) {
	if f == nil {
		return SwitchSourceSnapshot{ManagementUnknown: true}, fmt.Errorf("source ownership validator is nil")
	}
	return f(source)
}

type CrossHarnessSwitchDependencies struct {
	Store           CrossHarnessTargetStore
	Lifecycle       CrossHarnessTargetLifecycle
	Observer        CrossHarnessTargetObserver
	SourceOwnership CrossHarnessSourceOwnershipValidator
}

// StorageCrossHarnessTargetStore adapts the existing targeted storage APIs.
// It deliberately saves only the target row, avoiding a stale full-registry
// write that could overwrite the unchanged source or unrelated sessions.
type StorageCrossHarnessTargetStore struct {
	Storage *Storage
}

func (s StorageCrossHarnessTargetStore) LoadTarget(id string) (*Instance, error) {
	if s.Storage == nil {
		return nil, fmt.Errorf("cross-harness storage is unavailable")
	}
	instances, err := s.Storage.Load()
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst != nil && inst.ID == id {
			return inst, nil
		}
	}
	return nil, nil
}

func (s StorageCrossHarnessTargetStore) CreateTarget(target *Instance) error {
	if s.Storage == nil {
		return fmt.Errorf("cross-harness storage is unavailable")
	}
	return s.Storage.InsertSessionAndVerify(target, nil)
}

func (s StorageCrossHarnessTargetStore) SaveTarget(target *Instance) error {
	if s.Storage == nil {
		return fmt.Errorf("cross-harness storage is unavailable")
	}
	return s.Storage.InsertSessionAndVerify(target, nil)
}

func (s StorageCrossHarnessTargetStore) FinalizeTargetHandoff(source, target *Instance) error {
	if s.Storage == nil {
		return fmt.Errorf("cross-harness storage is unavailable")
	}
	return s.Storage.FinalizeCrossHarnessSupersession(source, target)
}

type CrossHarnessSwitchResult struct {
	Preview         *SwitchPreview
	Target          *Instance
	OperationID     string
	TargetCreated   bool
	TargetStarted   bool
	TargetReady     bool
	Pending         bool
	MissingContract string
	SourceSHA256    string
	LossDisclosure  []string
	// ConfiguredAccount is configuration presence only. Authentication is
	// intentionally never inferred from it or from a successful process start.
	ConfiguredAccount  string
	Authentication     string
	ContextDelivery    string
	SemanticAcceptance string
}

var (
	ErrCrossHarnessPending          = errors.New("cross-harness target is pending readiness evidence")
	ErrCrossHarnessRecoveryRequired = errors.New("cross-harness target recovery is required")
)

const (
	crossHarnessPrepared        = "prepared"
	crossHarnessCreated         = "target-created"
	crossHarnessLaunchAttempted = "launch-attempted"
	crossHarnessStarted         = "target-started"
	crossHarnessReady           = "ready"
	crossHarnessFailed          = "failed"
)

type crossHarnessLaunchBinding struct {
	TargetHome    string            `json:"target_home,omitempty"`
	SessionDir    string            `json:"session_dir,omitempty"`
	NativeCommand string            `json:"native_command"`
	NativeArgs    []string          `json:"native_args"`
	Environment   map[string]string `json:"environment,omitempty"`
	PayloadPath   string            `json:"payload_path"`
	PayloadSHA256 string            `json:"payload_sha256"`
}

type crossHarnessJournal struct {
	Version      int                       `json:"version"`
	OperationID  string                    `json:"operation_id"`
	State        string                    `json:"state"`
	Source       ContextSourceIdentity     `json:"source"`
	Target       FreshTargetIdentity       `json:"target"`
	Launch       crossHarnessLaunchBinding `json:"launch"`
	SourceSHA256 string                    `json:"source_sha256"`
	TargetID     string                    `json:"target_id"`
	// Superseded is written only after the scoped source+target storage CAS
	// succeeds. It makes recovery idempotent without replaying the launch.
	Superseded      bool                            `json:"superseded"`
	NativeSessionID string                          `json:"native_session_id,omitempty"`
	NoStart         bool                            `json:"no_start"`
	Observation     crossHarnessObservationBaseline `json:"observation,omitempty"`
	UpdatedAt       time.Time                       `json:"updated_at"`
	Failure         string                          `json:"failure,omitempty"`
}

// ExecuteCrossHarnessSwitch creates and starts a fresh target while preserving
// the source instance, process, transcript, account, children, and routing.
// It never reports completion from mutable target fields: completion requires
// independent target-native evidence from Observer.
func ExecuteCrossHarnessSwitch(ctx context.Context, cfg *UserConfig, source *Instance, opts CrossHarnessSwitchOptions, deps CrossHarnessSwitchDependencies) (*CrossHarnessSwitchResult, error) {
	if source == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if !validSwitchIdentity(source.ID) {
		return nil, fmt.Errorf("session id is not a safe switch identity: %q", source.ID)
	}
	// A fresh target is deliberately distinct, but the export/lifecycle
	// transaction still acts on this source. Share the native source lock so a
	// same-source native switch cannot race target planning or source handling.
	sourceLock, err := acquireHarnessSwitchSourceLock(source.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSwitchBusy, err)
	}
	defer sourceLock.Release()
	if deps.Store == nil {
		return nil, fmt.Errorf("cross-harness target store is required")
	}
	if deps.SourceOwnership == nil {
		return nil, fmt.Errorf("switch refused [management-unknown]: authoritative source ownership validation is required")
	}
	if deps.Lifecycle == nil && !opts.NoStart {
		return nil, fmt.Errorf("cross-harness target lifecycle is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	validateSourceOwnership := func() (SwitchSourceSnapshot, error) {
		snapshot, validateErr := deps.SourceOwnership.ValidateCrossHarnessSource(source)
		if validateErr != nil {
			return SwitchSourceSnapshot{}, fmt.Errorf("switch refused [management-unknown]: authoritative source ownership validation failed: %w", validateErr)
		}
		if refusal := refusalForCrossHarnessSource(source, &snapshot); refusal != nil {
			return SwitchSourceSnapshot{}, fmt.Errorf("switch refused [%s]: %s", refusal.Code, refusal.Message)
		}
		return snapshot, nil
	}

	// Never authorize execution from a caller-provided snapshot. The authority
	// must read current graph/routing state before any export, target creation,
	// staging, or lifecycle work.
	snapshot, err := validateSourceOwnership()
	if err != nil {
		return nil, err
	}
	preview := PreviewSwitchWithMaxBytesAndSnapshot(cfg, source, opts.Target, opts.MaxBytes, &snapshot)
	if preview.Refusal != nil {
		return nil, fmt.Errorf("switch refused [%s]: %s", preview.Refusal.Code, preview.Refusal.Message)
	}
	if preview.Execution != ExecutionPlanned || preview.LaunchPlan == nil {
		return nil, fmt.Errorf("cross-harness switch requires an executable fresh-target plan for %s -> %s", preview.SourceTool, preview.TargetHarness)
	}
	// Re-export at execution time. A preview's in-memory prompt is not an
	// authority to mutate later and must never be persisted in the journal.
	plan, err := BuildFreshTargetLaunchPlanForInstance(cfg, source, opts.Target, opts.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("exact source export for target failed: %w", err)
	}
	if plan.Target.InstanceID == source.ID {
		return nil, fmt.Errorf("target instance identity unexpectedly equals source")
	}

	// The export may be slow enough for a new dependent or watcher route to be
	// added. Revalidate immediately before the first staging side effect.
	if _, err := validateSourceOwnership(); err != nil {
		return nil, err
	}

	// The target receives an immutable, operation-bound file rather than the
	// raw JSONL or an enormous shell-quoted command. Stage it before recording
	// the launch binding so the journal and lifecycle share its exact path/hash.
	if err := stageCrossHarnessPayload(plan); err != nil {
		return nil, err
	}
	journalPath, err := crossHarnessJournalPath(plan)
	if err != nil {
		return nil, err
	}
	lock, err := AcquireConfigFileLock(journalPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSwitchBusy, err)
	}
	defer lock.Release()

	journal, err := loadCrossHarnessJournal(journalPath)
	if err != nil {
		return nil, err
	}
	if journal == nil {
		journal = &crossHarnessJournal{Version: 4, OperationID: crossHarnessOperationID(plan), State: crossHarnessPrepared,
			Source: plan.Source, Target: plan.Target, Launch: launchBindingForPlan(plan), SourceSHA256: plan.SourceArtifact.SourceSHA256,
			TargetID: plan.Target.InstanceID, NoStart: opts.NoStart}
		if err := writeCrossHarnessJournal(journalPath, journal); err != nil {
			return nil, err
		}
	} else if !crossHarnessJournalMatches(journal, plan, opts.NoStart) {
		return nil, fmt.Errorf("refusing recovery: cross-harness journal identity or launch binding does not match exact source and target")
	}

	result := &CrossHarnessSwitchResult{
		Preview: preview, OperationID: journal.OperationID, SourceSHA256: journal.SourceSHA256,
		LossDisclosure:    append([]string(nil), plan.Fidelity.Exclusions...),
		ConfiguredAccount: configuredCrossHarnessAccount(plan.Target), Authentication: "unverified",
		ContextDelivery: "not-delivered", SemanticAcceptance: "pending",
	}
	target, err := deps.Store.LoadTarget(plan.Target.InstanceID)
	if err != nil {
		return failCrossHarness(journalPath, journal, result, fmt.Errorf("load target %q: %w", plan.Target.InstanceID, err))
	}
	if target == nil {
		target = newCrossHarnessTarget(plan)
		// Replacement preserves the source's visible placement and parent routing;
		// only the fresh harness/native identity changes.
		target.Order = source.Order
		target.ParentSessionID = source.ParentSessionID
		target.ParentProjectPath = source.ParentProjectPath
		target.NoTransitionNotify = source.NoTransitionNotify
		// Keep an unready destination out of the default active list. The target
		// remains durable and directly addressable for recovery, while the source
		// remains the sole visible row until final atomic supersession.
		target.ArchivedAt = time.Now().UTC()
		target.Supersedes = source.ID
		if err := deps.Store.CreateTarget(target); err != nil {
			return failCrossHarness(journalPath, journal, result, fmt.Errorf("persist fresh target: %w", err))
		}
		// Creation is the durable cross-harness commit. Retain the target
		// metadata even if recording the following journal transition fails.
		result.Target, result.TargetCreated = target, true
		journal.State, journal.Failure = crossHarnessCreated, ""
		if err := writeCrossHarnessJournal(journalPath, journal); err != nil {
			return crossHarnessRecoveryRequired(result, "target creation was committed but its journal transition was not persisted", err)
		}
	} else if !crossHarnessTargetMatches(target, plan.Target) {
		return failCrossHarness(journalPath, journal, result, fmt.Errorf("persisted target %q does not match planned distinct target identity", plan.Target.InstanceID))
	}
	result.Target = target

	if opts.NoStart {
		return pendingCrossHarness(journalPath, journal, result, "target lifecycle start and target-native readiness observation were deferred by --no-start")
	}
	uncertainLaunch := journal.State == crossHarnessLaunchAttempted
	if journal.State == crossHarnessCreated || journal.State == crossHarnessPrepared {
		if err := crossHarnessPlanMatchesConfig(cfg, plan); err != nil {
			return failCrossHarness(journalPath, journal, result, err)
		}
		if journaler, ok := deps.Observer.(CrossHarnessTargetObservationJournaler); ok {
			journal.Observation = journaler.PrepareTargetObservation(target, plan.Target)
		} else if preparer, ok := deps.Observer.(CrossHarnessTargetObservationPreparer); ok {
			preparer.BeginTargetObservation(target, plan.Target)
		}
		// This receipt is intentionally durable before the lifecycle call. A
		// crash or ambiguous lifecycle error after this point is recovery-only:
		// observe this exact target, never start or deliver the prompt again.
		journal.State, journal.Failure = crossHarnessLaunchAttempted, ""
		if err := writeCrossHarnessJournal(journalPath, journal); err != nil {
			return nil, err
		}
		uncertainLaunch = true
		if err := deps.Lifecycle.StartTarget(ctx, target, plan); err != nil {
			journal.Failure = "launch result uncertain: " + err.Error()
			if writeErr := writeCrossHarnessJournal(journalPath, journal); writeErr != nil {
				return result, fmt.Errorf("launch result uncertain: %v; persist recovery journal: %w", err, writeErr)
			}
		} else {
			journal.State, journal.Failure = crossHarnessStarted, ""
			if err := deps.Store.SaveTarget(target); err != nil {
				journal.State, journal.Failure = crossHarnessLaunchAttempted, "persist started target uncertain: "+err.Error()
				return pendingCrossHarness(journalPath, journal, result, journal.Failure)
			}
			if err := writeCrossHarnessJournal(journalPath, journal); err != nil {
				journal.State, journal.Failure = crossHarnessLaunchAttempted, "persist started journal uncertain: "+err.Error()
				return pendingCrossHarness(journalPath, journal, result, journal.Failure)
			}
			uncertainLaunch = false
			result.TargetStarted = true
			result.ContextDelivery = "lifecycle prompt delivery returned; native semantic acceptance remains pending"
		}
	}
	if deps.Observer == nil {
		contract := missingTargetContract(plan.Target.Tool)
		if uncertainLaunch {
			contract = "recovery required after uncertain launch; " + contract
		}
		return pendingCrossHarness(journalPath, journal, result, contract)
	}
	if restorer, ok := deps.Observer.(CrossHarnessTargetObservationJournaler); ok && !journal.Observation.StartedAt.IsZero() {
		restorer.RestoreTargetObservation(target, journal.Observation)
	}
	evidence, observeErr := deps.Observer.ObserveTarget(ctx, target, plan.Target)
	if observeErr != nil {
		contract := missingTargetContract(plan.Target.Tool) + ": " + observeErr.Error()
		if uncertainLaunch || ctx.Err() != nil {
			return pendingCrossHarness(journalPath, journal, result, "recovery required after uncertain launch; "+contract)
		}
		return failCrossHarness(journalPath, journal, result, fmt.Errorf("observe target-native readiness: %w", observeErr))
	}
	if err := validateCrossHarnessEvidence(evidence, plan); err != nil {
		if uncertainLaunch {
			return pendingCrossHarness(journalPath, journal, result, "recovery required after uncertain launch; exact target evidence not yet valid: "+err.Error())
		}
		return failCrossHarness(journalPath, journal, result, err)
	}
	switch plan.Target.Tool {
	case "pi":
		if evidence.ArtifactPath == "" {
			return failCrossHarness(journalPath, journal, result, fmt.Errorf("Pi readiness evidence has no exact native artifact path"))
		}
		target.PiSessionID, target.PiSessionPath = evidence.SessionID, evidence.ArtifactPath
		if err := deps.Store.SaveTarget(target); err != nil {
			return pendingCrossHarness(journalPath, journal, result, "persist observed Pi native identity/path: "+err.Error())
		}
	case "codex":
		// The observed rollout ID, not a launch field, is the fresh Codex
		// identity. Persist it before replacement so recovery cannot mistake a
		// ready target for an unbound fresh shell.
		target.CodexSessionID = evidence.SessionID
		if err := deps.Store.SaveTarget(target); err != nil {
			return pendingCrossHarness(journalPath, journal, result, "persist observed Codex native identity: "+err.Error())
		}
	}
	// Older ready journals predate pending-target archival. Bind their target
	// to this exact source before finalization so recovery can atomically fold a
	// formerly duplicated ready row into the same reversible lineage without
	// replaying launch or prompt delivery.
	if target.Supersedes != source.ID {
		target.Supersedes = source.ID
		if err := deps.Store.SaveTarget(target); err != nil {
			return pendingCrossHarness(journalPath, journal, result, "persist target recovery lineage: "+err.Error())
		}
	}
	// Source replacement is intentionally last: readiness evidence and target
	// persistence are already proven. Recheck external ownership immediately
	// before the DB CAS; the storage boundary independently guards current
	// source/child state. A failed check leaves the original visible and the
	// target hidden as a recoverable pending row.
	if _, err := validateSourceOwnership(); err != nil {
		return pendingCrossHarness(journalPath, journal, result, err.Error())
	}
	if err := deps.Store.FinalizeTargetHandoff(source, target); err != nil {
		return pendingCrossHarness(journalPath, journal, result, "atomic source/target supersession pending: "+err.Error())
	}
	source.ArchivedAt, source.SupersededBy = time.Now().UTC(), target.ID
	target.ArchivedAt, target.Supersedes = time.Time{}, source.ID
	journal.State, journal.Failure, journal.NativeSessionID, journal.Superseded = crossHarnessReady, "", evidence.SessionID, true
	result.TargetReady = true
	result.ContextDelivery = "lifecycle prompt delivery returned; native semantic acceptance remains pending"
	if err := writeCrossHarnessJournal(journalPath, journal); err != nil {
		return crossHarnessRecoveryRequired(result, "target-native readiness was observed but its journal transition was not persisted", err)
	}
	result.SemanticAcceptance = "pending"
	return result, nil
}

// stageCrossHarnessPayload makes the portable handoff available to the target
// without putting its contents in a tmux command, launch record, or failure
// sidecar. The source artifact remains at Manifest.Artifact.Path as the raw
// evidence; this file is only the lossy portable projection.
func stageCrossHarnessPayload(plan *FreshTargetLaunchPlan) error {
	if plan == nil || len(plan.Prompt) == 0 {
		return fmt.Errorf("cross-harness handoff has no portable payload to stage")
	}
	root, err := runtimeDataPath("cross-harness-handoff-staging")
	if err != nil {
		return err
	}
	stageDir := filepath.Join(root, crossHarnessOperationID(plan))
	if err := ensureNoSymlinkPath(stageDir); err != nil {
		return fmt.Errorf("unsafe cross-harness handoff stage: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return fmt.Errorf("create cross-harness handoff stage: %w", err)
	}
	if err := os.Chmod(stageDir, 0o700); err != nil {
		return fmt.Errorf("secure cross-harness handoff stage: %w", err)
	}
	stageInfo, err := os.Lstat(stageDir)
	if err != nil || stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.IsDir() || stageInfo.Mode().Perm() != 0o700 {
		if err != nil {
			return fmt.Errorf("inspect cross-harness handoff stage: %w", err)
		}
		return fmt.Errorf("cross-harness handoff stage is not a private directory")
	}
	path := filepath.Join(stageDir, "portable-context.txt")
	sum := sha256.Sum256(plan.Prompt)
	hash := hex.EncodeToString(sum[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if chmodErr := file.Chmod(0o600); chmodErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return fmt.Errorf("secure cross-harness handoff payload: %w", chmodErr)
		}
		if _, writeErr := file.Write(plan.Prompt); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return fmt.Errorf("write cross-harness handoff payload: %w", writeErr)
		}
		if syncErr := file.Sync(); syncErr != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return fmt.Errorf("sync cross-harness handoff payload: %w", syncErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("close cross-harness handoff payload: %w", closeErr)
		}
	} else if !os.IsExist(err) {
		return fmt.Errorf("create cross-harness handoff payload: %w", err)
	}
	plan.PayloadPath, plan.PayloadSHA256 = path, hash
	if err := validateStagedCrossHarnessPayload(plan); err != nil {
		return err
	}
	// No later executor path needs an in-memory copy. In particular, this keeps
	// a recovery launch from accidentally passing the context to StartWithMessage.
	plan.Prompt = nil
	return nil
}

func validateStagedCrossHarnessPayload(plan *FreshTargetLaunchPlan) error {
	if plan == nil || plan.PayloadPath == "" || plan.PayloadSHA256 == "" {
		return fmt.Errorf("cross-harness lifecycle plan has no staged payload binding")
	}
	if err := ensureNoSymlinkPath(plan.PayloadPath); err != nil {
		return fmt.Errorf("unsafe cross-harness handoff payload: %w", err)
	}
	info, err := os.Lstat(plan.PayloadPath)
	if err != nil {
		return fmt.Errorf("inspect cross-harness handoff payload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("cross-harness handoff payload is not a private regular file")
	}
	data, err := os.ReadFile(plan.PayloadPath)
	if err != nil {
		return fmt.Errorf("read cross-harness handoff payload: %w", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != plan.PayloadSHA256 {
		return fmt.Errorf("cross-harness handoff payload hash does not match launch binding")
	}
	return nil
}

func launchBindingForPlan(plan *FreshTargetLaunchPlan) crossHarnessLaunchBinding {
	if plan == nil {
		return crossHarnessLaunchBinding{}
	}
	binding := crossHarnessLaunchBinding{TargetHome: plan.TargetHome, SessionDir: plan.SessionDir, NativeCommand: plan.NativeCommand, PayloadPath: plan.PayloadPath, PayloadSHA256: plan.PayloadSHA256}
	binding.NativeArgs = append([]string(nil), plan.NativeArgs...)
	if len(plan.Environment) > 0 {
		binding.Environment = make(map[string]string, len(plan.Environment))
		for key, value := range plan.Environment {
			binding.Environment[key] = value
		}
	}
	return binding
}

func crossHarnessPlanMatchesConfig(cfg *UserConfig, plan *FreshTargetLaunchPlan) error {
	if plan == nil || plan.Target.InstanceID == "" || plan.Target.ProjectPath == "" || canonicalSwitchHarness(plan.Target.Tool) != plan.Target.Tool {
		return fmt.Errorf("cross-harness launch plan has an invalid target binding")
	}
	if plan.NativeCommand != plan.Target.Tool || len(plan.NativeArgs) == 0 {
		return fmt.Errorf("cross-harness launch plan has no exact native command binding")
	}
	if plan.Target.Account != "" {
		dir, status := resolveTargetAccountInfo(cfg, plan.Target.Tool, plan.Target.Account)
		if status != AccountStatusConfigured || ExpandPath(dir) != plan.TargetHome {
			return fmt.Errorf("refusing target launch: configured %s account %q drifted from the immutable plan", plan.Target.Tool, plan.Target.Account)
		}
	}
	return nil
}

func cloneCrossHarnessLaunchPlan(plan *FreshTargetLaunchPlan) *FreshTargetLaunchPlan {
	if plan == nil {
		return nil
	}
	clone := *plan
	clone.NativeArgs = append([]string(nil), plan.NativeArgs...)
	clone.Prompt = append([]byte(nil), plan.Prompt...)
	if len(plan.Environment) > 0 {
		clone.Environment = make(map[string]string, len(plan.Environment))
		for key, value := range plan.Environment {
			clone.Environment[key] = value
		}
	}
	return &clone
}

func crossHarnessPlanCommand(target *Instance, plan *FreshTargetLaunchPlan, _ string) (string, bool, error) {
	if target == nil || plan == nil || !crossHarnessTargetMatches(target, plan.Target) || filepath.Clean(target.EffectiveWorkingDir()) != filepath.Clean(plan.Target.ProjectPath) {
		return "", false, fmt.Errorf("cross-harness lifecycle target no longer matches its immutable launch plan")
	}
	if plan.NativeCommand != plan.Target.Tool || len(plan.NativeArgs) == 0 {
		return "", false, fmt.Errorf("cross-harness lifecycle plan has no exact native command")
	}
	if err := validateStagedCrossHarnessPayload(plan); err != nil {
		return "", false, err
	}
	keys := make([]string, 0, len(plan.Environment))
	for key := range plan.Environment {
		if !IsValidEnvKey(key) {
			return "", false, fmt.Errorf("cross-harness lifecycle plan has invalid environment key %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	prefix := target.buildEnvSourceCommand() + fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ", shellescape.Quote(target.ID), shellescape.Quote(target.Title), shellescape.Quote(target.Tool), shellescape.Quote(sessionProfileEnvValue()))
	for _, key := range keys {
		prefix += key + "=" + shellescape.Quote(plan.Environment[key]) + " "
	}
	// The shell receives only fixed script text and quoted plan values. It reads
	// the handoff file into one quoted argv element; payload bytes are never
	// parsed as shell syntax or embedded in the tmux start command.
	args := []string{shellescape.Quote(plan.PayloadPath), shellescape.Quote(plan.NativeCommand)}
	for _, arg := range plan.NativeArgs {
		args = append(args, shellescape.Quote(arg))
	}
	// Identity injection (identity_injection.go): the switched-to session is a
	// NEW instance in a NEW harness, so it gets its own identity block, built
	// from the target record, through the target harness's native flag. Added
	// at command-build time only; the journaled plan stays immutable and never
	// carries the block. Each argv element is quoted like the plan's own.
	for _, arg := range target.identityNativeArgs(plan.Target.Tool, plan.Environment["CODEX_HOME"]) {
		args = append(args, shellescape.Quote(arg))
	}
	readPayload := shellescape.Quote(`payload=$(cat "$1") || exit 1; shift; command=$1; shift; exec "$command" "$@" "$payload"`)
	command := prefix + "sh -c " + readPayload + " agent-deck-handoff " + strings.Join(args, " ")
	return command, true, nil
}

func newCrossHarnessTarget(plan *FreshTargetLaunchPlan) *Instance {
	target := NewInstanceWithGroupAndTool(plan.Target.Title, plan.Target.ProjectPath, plan.Target.GroupPath, plan.Target.Tool)
	target.ID = plan.Target.InstanceID
	target.Account = plan.Target.Account
	target.Command = plan.NativeCommand
	if target.tmuxSession != nil {
		target.tmuxSession.InstanceID = target.ID
	}
	if IsClaudeCompatible(target.Tool) {
		// StartWithMessage selects --session-id when no matching native
		// transcript exists, yielding the planned fresh Claude target ID.
		target.ClaudeSessionID = plan.Target.SessionID
	}
	return target
}

func crossHarnessTargetMatches(target *Instance, expected FreshTargetIdentity) bool {
	return target != nil && target.ID == expected.InstanceID && canonicalSwitchHarness(target.Tool) == expected.Tool &&
		target.Account == expected.Account && target.ProjectPath == expected.ProjectPath && target.Title == expected.Title && target.GroupPath == expected.GroupPath
}

func validateCrossHarnessEvidence(e CrossHarnessTargetEvidence, plan *FreshTargetLaunchPlan) error {
	if !e.Ready {
		return fmt.Errorf("target-native readiness event has not been observed")
	}
	if e.InstanceID != plan.Target.InstanceID || canonicalSwitchHarness(e.Tool) != plan.Target.Tool {
		return fmt.Errorf("target-native readiness event belongs to wrong or stale target")
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return fmt.Errorf("target-native readiness event has no native session identity")
	}
	if plan.Target.Tool == "claude" {
		if e.SessionID != plan.Target.SessionID {
			return fmt.Errorf("target-native session identity %q does not match planned target %q", e.SessionID, plan.Target.SessionID)
		}
	} else if e.SessionID == plan.Source.SessionID {
		return fmt.Errorf("target-native session identity reuses the source identity")
	}
	return nil
}

func pendingCrossHarness(path string, journal *crossHarnessJournal, result *CrossHarnessSwitchResult, contract string) (*CrossHarnessSwitchResult, error) {
	// Pending is the in-memory safety state as well as a journalled state. Set
	// it before persistence so a journal I/O failure can never be misreported as
	// a ready target by a caller that retains the committed target metadata.
	result.Pending, result.MissingContract = true, contract
	journal.Failure = contract
	if err := writeCrossHarnessJournal(path, journal); err != nil {
		return crossHarnessRecoveryRequired(result, contract, err)
	}
	return result, fmt.Errorf("%w: %s", ErrCrossHarnessPending, contract)
}

func crossHarnessRecoveryRequired(result *CrossHarnessSwitchResult, contract string, persistErr error) (*CrossHarnessSwitchResult, error) {
	result.Pending = true
	result.MissingContract = fmt.Sprintf("recovery required: %s; journal persistence failed: %v", contract, persistErr)
	return result, fmt.Errorf("%w: %s; %w: persist pending journal: %v", ErrCrossHarnessPending, contract, ErrCrossHarnessRecoveryRequired, persistErr)
}

func failCrossHarness(path string, journal *crossHarnessJournal, result *CrossHarnessSwitchResult, cause error) (*CrossHarnessSwitchResult, error) {
	journal.State, journal.Failure = crossHarnessFailed, cause.Error()
	if err := writeCrossHarnessJournal(path, journal); err != nil {
		return result, fmt.Errorf("%w; persist failure journal: %v", cause, err)
	}
	return result, cause
}

func configuredCrossHarnessAccount(target FreshTargetIdentity) string {
	if strings.TrimSpace(target.Account) == "" {
		return "default"
	}
	return target.Account + " (configured slot; authentication unverified)"
}

func missingTargetContract(tool string) string {
	return fmt.Sprintf("missing target-native readiness contract for %s: observer must report the distinct target instance ID, fresh native session identity, and ready event", tool)
}

func crossHarnessOperationID(plan *FreshTargetLaunchPlan) string {
	sum := sha256.Sum256([]byte("agent-deck/cross-harness-operation/v1\x00" + plan.Source.InstanceID + "\x00" + plan.Source.SessionID + "\x00" + plan.Target.InstanceID + "\x00" + plan.Target.Tool + "\x00" + plan.Target.Account))
	return hex.EncodeToString(sum[:16])
}

func crossHarnessJournalPath(plan *FreshTargetLaunchPlan) (string, error) {
	root, err := runtimeDataPath("cross-harness-switch")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, crossHarnessOperationID(plan)+".json"), nil
}

func loadCrossHarnessJournal(path string) (*crossHarnessJournal, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal crossHarnessJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("read cross-harness journal: %w", err)
	}
	return &journal, nil
}

func writeCrossHarnessJournal(path string, journal *crossHarnessJournal) error {
	if journal == nil {
		return fmt.Errorf("nil cross-harness journal")
	}
	journal.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cross-harness-journal-")
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func crossHarnessJournalMatches(journal *crossHarnessJournal, plan *FreshTargetLaunchPlan, noStart bool) bool {
	if journal == nil || plan == nil || journal.Version != 4 || journal.OperationID != crossHarnessOperationID(plan) || journal.NoStart != noStart || journal.SourceSHA256 != plan.SourceArtifact.SourceSHA256 {
		return false
	}
	return journal.Source == plan.Source && journal.Target == plan.Target &&
		reflect.DeepEqual(journal.Launch, launchBindingForPlan(plan))
}

// InstanceCrossHarnessLifecycle is the production adapter. It is deliberately
// small so tests can inject a no-process lifecycle. It does not stop or alter
// the source and does not attempt rollback by signalling the target.
type InstanceCrossHarnessLifecycle struct{}

func (InstanceCrossHarnessLifecycle) StartTarget(ctx context.Context, target *Instance, plan *FreshTargetLaunchPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == nil || plan == nil || !crossHarnessTargetMatches(target, plan.Target) {
		return fmt.Errorf("refusing lifecycle start for an unbound cross-harness target")
	}
	// StartWithMessage normally resolves command details from mutable config.
	// This temporary in-memory binding makes its launch path consume the exact
	// validated plan instead; it is never persisted or reused on recovery.
	target.crossHarnessLaunch = cloneCrossHarnessLaunchPlan(plan)
	defer func() { target.crossHarnessLaunch = nil }()
	// crossHarnessPlanCommand reads the already staged payload at exec time;
	// passing it here would recreate the oversized tmux command failure.
	return target.StartWithMessage("")
}
