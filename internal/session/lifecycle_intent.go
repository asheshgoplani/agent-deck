package session

import (
	"encoding/json"
	"errors"
	"fmt"
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
		go func(intent LifecycleIntentHandle) {
			defer close(renewDone)
			ticker := time.NewTicker(lifecycleClaimRenewInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_, _ = storage.db.RenewLifecycleIntentClaim(intent.InstanceID, intent.Token, recoveryOwner)
				case <-stopRenew:
					return
				}
			}
		}(intent)
		finishClaim := func() { close(stopRenew); <-renewDone }

		// The startup slice is only a routing hint. The durable row is read
		// again after claiming destructive ownership, closing the snapshot/ID
		// reuse window before any process, filesystem, row, or queue mutation.
		durableGeneration, rowExists, generationErr := storage.db.LifecycleTargetGeneration(intent.InstanceID, intent.Token, recoveryOwner)
		if generationErr != nil {
			finishClaim()
			recoveryErr = errors.Join(recoveryErr, generationErr)
			continue
		}
		if rowExists && (intent.Generation == 0 || durableGeneration != intent.Generation) {
			// The ID now belongs to a newer incarnation. Never touch its row or
			// queue; only retire the payload-owned runtime from the old operation.
			if metadata.Instance != nil && metadata.Instance.Exists() {
				if killErr := metadata.Instance.KillAndWait(); killErr != nil && metadata.Instance.Exists() {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("retire stale lifecycle runtime %s: %w", intent.InstanceID, killErr))
					continue
				}
			}
			if completeErr := CompleteLifecycleIntent(storage, intent); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
			continue
		}
		switch intent.Kind {
		case LifecycleIntentArchive:
			tx, lockErr := BeginRuntimeQueueTransaction(intent.InstanceID)
			if lockErr != nil {
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive %s: %w", intent.InstanceID, lockErr))
				continue
			}
			if inst.Exists() {
				if killErr := inst.KillAndWait(); killErr != nil && inst.Exists() {
					tx.Release()
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive stop %s: %w", intent.InstanceID, killErr))
					continue
				}
			}
			if discardErr := tx.Discard(); discardErr != nil {
				tx.Release()
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive queue %s: %w", intent.InstanceID, discardErr))
				continue
			}
			tx.Release()
			if completeErr := CompleteLifecycleIntent(storage, intent); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
		case LifecycleIntentRemove, LifecycleIntentWorktreeFinish:
			if intent.Kind == LifecycleIntentWorktreeFinish && inst != nil && intent.Phase == "merged" && metadata.Instance != nil {
				if _, removeErr := RemoveSessionWorktree(metadata.Instance); removeErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, removeErr)
					continue
				}
			}
			teardownInst := inst
			if teardownInst == nil {
				teardownInst = metadata.Instance
			}
			if teardownInst != nil && teardownInst.Exists() {
				if killErr := teardownInst.KillAndWait(); killErr != nil && teardownInst.Exists() {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, killErr)
					continue
				}
			}
			if metadata.ConductorName != "" {
				if teardownErr := TeardownConductor(metadata.ConductorName); teardownErr != nil {
					finishClaim()
					recoveryErr = errors.Join(recoveryErr, teardownErr)
					continue
				}
				_ = UninstallHeartbeatDaemon(metadata.ConductorName)
			}
			if inst != nil && intent.Kind == LifecycleIntentWorktreeFinish {
				if deleteErr := storage.db.DeleteInstance(intent.InstanceID); deleteErr != nil {
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
			if discardErr := tx.Discard(); discardErr != nil {
				tx.Release()
				finishClaim()
				recoveryErr = errors.Join(recoveryErr, discardErr)
				continue
			}
			tx.Release()
			if completeErr := CompleteLifecycleIntent(storage, intent); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
			finishClaim()
		default:
			finishClaim()
		}
	}
	return recoveryErr
}
