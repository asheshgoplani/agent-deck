package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/google/uuid"
)

type LifecycleOperationMetadata struct {
	Instance      *Instance `json:"instance,omitempty"`
	WorktreePath  string    `json:"worktree_path,omitempty"`
	ConductorName string    `json:"conductor_name,omitempty"`
}

func LifecycleIntentPayload(inst *Instance, worktreePath, conductorName string) string {
	raw, err := json.Marshal(LifecycleOperationMetadata{Instance: inst, WorktreePath: worktreePath, ConductorName: conductorName})
	if err != nil {
		return ""
	}
	return string(raw)
}

const (
	LifecycleIntentRemove         = "remove"
	LifecycleIntentWorktreeFinish = "worktree-finish"
	LifecycleIntentArchive        = "archive"
)

type LifecycleIntentHandle = statedb.LifecycleIntent

var lifecycleClaimRenewInterval = 10 * time.Second
var lifecycleBeforeRecoveryClaim func(statedb.LifecycleIntent)
var lifecycleAfterGenerationRead func(statedb.LifecycleIntent)
var lifecycleRecoveryMutation func(string)
var lifecycleRemoveWorktree = RemoveSessionWorktree
var lifecycleInstanceExists = func(inst *Instance) bool { return inst != nil && inst.Exists() }

func PrepareLifecycleIntent(storage *Storage, instanceID, kind, payload string) (LifecycleIntentHandle, error) {
	if storage == nil || storage.db == nil {
		return LifecycleIntentHandle{}, errors.New("prepare lifecycle intent: storage unavailable")
	}
	var metadata LifecycleOperationMetadata
	_ = json.Unmarshal([]byte(payload), &metadata)
	var generation int64
	if metadata.Instance != nil {
		generation = metadata.Instance.PersistenceGeneration
	}
	return storage.db.PrepareLifecycleIntent(statedb.LifecycleIntent{InstanceID: instanceID, Kind: kind, Payload: payload, Generation: generation})
}

func AdvanceLifecycleIntent(storage *Storage, intent LifecycleIntentHandle, phase, payload string) error {
	if storage == nil || storage.db == nil {
		return errors.New("advance lifecycle intent: storage unavailable")
	}
	return storage.db.AdvanceLifecycleIntent(intent.InstanceID, intent.Token, phase, payload)
}

func CompleteLifecycleIntent(storage *Storage, intent LifecycleIntentHandle) error {
	if storage == nil || storage.db == nil {
		return errors.New("complete lifecycle intent: storage unavailable")
	}
	return storage.db.CompleteLifecycleIntent(intent.InstanceID, intent.Token)
}

// RecoverLifecycleIntents finishes only transitions whose durable state makes
// the next action unambiguous. Prepared worktree finishes with a live row are
// retained for an explicit retry because repository mutation may not have run.
func RecoverLifecycleIntents(storage *Storage, instances []*Instance) error {
	if storage == nil || storage.db == nil {
		return nil
	}
	intents, err := storage.db.LifecycleIntents()
	if err != nil {
		return err
	}
	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}
	var recoveryErr error
	recoveryOwner := uuid.NewString()
	for _, intent := range intents {
		joinIntentErr := func(candidate error) {
			if errors.Is(candidate, statedb.ErrLifecycleIntentOwnership) {
				active, readErr := storage.db.LifecycleIntents()
				if readErr == nil {
					found := false
					for _, current := range active {
						if current.InstanceID == intent.InstanceID && current.Token == intent.Token {
							found = true
							break
						}
					}
					if !found {
						return
					}
				}
			}
			recoveryErr = errors.Join(recoveryErr, candidate)
		}
		inst := byID[intent.InstanceID]
		var metadata LifecycleOperationMetadata
		_ = json.Unmarshal([]byte(intent.Payload), &metadata)
		if intent.Kind == LifecycleIntentArchive && (inst == nil || !inst.IsArchived()) {
			continue
		}
		if (intent.Kind == LifecycleIntentRemove || intent.Kind == LifecycleIntentWorktreeFinish) && inst != nil && intent.Phase == "prepared" {
			continue
		}
		if lifecycleBeforeRecoveryClaim != nil {
			lifecycleBeforeRecoveryClaim(intent)
		}
		claimed, claimErr := storage.db.ClaimLifecycleIntent(intent.InstanceID, intent.Token, recoveryOwner)
		if claimErr != nil {
			recoveryErr = errors.Join(recoveryErr, claimErr)
			continue
		}
		if !claimed {
			continue
		}
		stopRenew := make(chan struct{})
		renewDone := make(chan struct{})
		var claimLost atomic.Bool
		var claimErrMu sync.Mutex
		var renewalErr error
		go func(intent LifecycleIntentHandle) {
			defer close(renewDone)
			ticker := time.NewTicker(lifecycleClaimRenewInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					renewed, err := storage.db.RenewLifecycleIntentClaim(intent.InstanceID, intent.Token, recoveryOwner)
					if err != nil || !renewed {
						claimErrMu.Lock()
						if err == nil {
							err = statedb.ErrLifecycleIntentOwnership
						}
						renewalErr = err
						claimErrMu.Unlock()
						claimLost.Store(true)
						return
					}
				case <-stopRenew:
					return
				}
			}
		}(intent)
		finishClaim := func() { close(stopRenew); <-renewDone }
		ensureOwned := func(requireRow bool) error {
			if claimLost.Load() {
				claimErrMu.Lock()
				err := renewalErr
				claimErrMu.Unlock()
				if err == nil {
					err = statedb.ErrLifecycleIntentOwnership
				}
				return err
			}
			return storage.db.ValidateLifecycleClaim(intent.InstanceID, intent.Token, recoveryOwner, intent.Generation, requireRow)
		}
		markMutation := func(name string) {
			if lifecycleRecoveryMutation != nil {
				lifecycleRecoveryMutation(name)
			}
		}

		// The startup slice is only a routing hint. The durable row is read
		// again after claiming destructive ownership, closing the snapshot/ID
		// reuse window before any process, filesystem, row, or queue mutation.
		durableGeneration, rowExists, generationErr := storage.db.LifecycleTargetGeneration(intent.InstanceID, intent.Token, recoveryOwner)
		if generationErr != nil {
			finishClaim()
			recoveryErr = errors.Join(recoveryErr, generationErr)
			continue
		}
		if lifecycleAfterGenerationRead != nil {
			lifecycleAfterGenerationRead(intent)
		}
		if rowExists && (intent.Generation == 0 || durableGeneration != intent.Generation) {
			// The ID now belongs to a newer incarnation. Never touch its row or
			// queue; only retire the payload-owned runtime from the old operation.
			if lifecycleInstanceExists(metadata.Instance) {
				if err := ensureOwned(false); err != nil {
					finishClaim()
					joinIntentErr(err)
					continue
				}
				markMutation("stale-runtime-kill")
				if killErr := metadata.Instance.KillAndWait(); killErr != nil && metadata.Instance.Exists() {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("retire stale lifecycle runtime %s: %w", intent.InstanceID, killErr))
					continue
				}
			}
			if completeErr := storage.db.CompleteClaimedLifecycleIntent(intent.InstanceID, intent.Token, recoveryOwner); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
			continue
		}
		switch intent.Kind {
		case LifecycleIntentArchive:
			if err := ensureOwned(true); err != nil {
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, err)
				continue
			}
			tx, lockErr := BeginRuntimeQueueTransaction(intent.InstanceID)
			if lockErr != nil {
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive %s: %w", intent.InstanceID, lockErr))
				continue
			}
			if inst.Exists() {
				if err := ensureOwned(true); err != nil {
					tx.Release()
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, err)
					continue
				}
				markMutation("archive-kill")
				if killErr := inst.KillAndWait(); killErr != nil && inst.Exists() {
					tx.Release()
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive stop %s: %w", intent.InstanceID, killErr))
					continue
				}
			}
			discardErr := storage.db.WithLifecycleClaimGuard(intent.InstanceID, intent.Token, recoveryOwner, intent.Generation, true, func() error {
				if claimLost.Load() {
					return statedb.ErrLifecycleIntentOwnership
				}
				markMutation("archive-queue-discard")
				return tx.Discard()
			})
			if discardErr != nil {
				tx.Release()
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive queue %s: %w", intent.InstanceID, discardErr))
				continue
			}
			tx.Release()
			if completeErr := storage.db.CompleteClaimedLifecycleIntent(intent.InstanceID, intent.Token, recoveryOwner); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
		case LifecycleIntentRemove, LifecycleIntentWorktreeFinish:
			if intent.Kind == LifecycleIntentWorktreeFinish && inst != nil && intent.Phase == "merged" && metadata.Instance != nil {
				if err := ensureOwned(true); err != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, err)
					continue
				}
				markMutation("worktree-remove")
				if _, removeErr := lifecycleRemoveWorktree(metadata.Instance); removeErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, removeErr)
					continue
				}
				if advanceErr := storage.db.AdvanceClaimedLifecycleIntent(intent.InstanceID, intent.Token, recoveryOwner, "worktree-removed", intent.Payload); advanceErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, advanceErr)
					continue
				}
				intent.Phase = "worktree-removed"
			}
			teardownInst := inst
			if teardownInst == nil {
				teardownInst = metadata.Instance
			}
			if teardownInst != nil && teardownInst.Exists() {
				if err := ensureOwned(inst != nil); err != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, err)
					continue
				}
				markMutation("remove-kill")
				if killErr := teardownInst.KillAndWait(); killErr != nil && teardownInst.Exists() {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, killErr)
					continue
				}
			}
			if metadata.ConductorName != "" {
				if err := ensureOwned(inst != nil); err != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, err)
					continue
				}
				markMutation("conductor-teardown")
				if teardownErr := TeardownConductor(metadata.ConductorName); teardownErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, teardownErr)
					continue
				}
				_ = UninstallHeartbeatDaemon(metadata.ConductorName)
			}
			if inst != nil && intent.Kind == LifecycleIntentWorktreeFinish {
				if err := ensureOwned(true); err != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, err)
					continue
				}
				markMutation("claimed-delete")
				if deleteErr := storage.db.DeleteClaimedLifecycleInstance(intent.InstanceID, intent.Token, recoveryOwner, intent.Generation, intent.Kind, intent.Phase); deleteErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, deleteErr)
					continue
				}
			}
			tx, lockErr := BeginRuntimeQueueTransaction(intent.InstanceID)
			if lockErr != nil {
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, lockErr)
				continue
			}
			discardErr := storage.db.WithLifecycleClaimGuard(intent.InstanceID, intent.Token, recoveryOwner, intent.Generation, false, func() error {
				if claimLost.Load() {
					return statedb.ErrLifecycleIntentOwnership
				}
				markMutation("queue-discard")
				return tx.Discard()
			})
			if discardErr != nil {
				tx.Release()
				finishClaim()
				joinIntentErr(discardErr)
				continue
			}
			tx.Release()
			if completeErr := storage.db.CompleteClaimedLifecycleIntent(intent.InstanceID, intent.Token, recoveryOwner); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
		default:
			finishClaim()
		}
	}
	return recoveryErr
}
