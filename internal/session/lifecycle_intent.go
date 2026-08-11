package session

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	LifecycleIntentRemove         = "remove"
	LifecycleIntentWorktreeFinish = "worktree-finish"
	LifecycleIntentArchive        = "archive"
)

func PrepareLifecycleIntent(storage *Storage, instanceID, kind, payload string) error {
	if storage == nil || storage.db == nil {
		return errors.New("prepare lifecycle intent: storage unavailable")
	}
	return storage.db.PrepareLifecycleIntent(statedb.LifecycleIntent{InstanceID: instanceID, Kind: kind, Payload: payload})
}

func CompleteLifecycleIntent(storage *Storage, instanceID string) error {
	if storage == nil || storage.db == nil {
		return errors.New("complete lifecycle intent: storage unavailable")
	}
	return storage.db.CompleteLifecycleIntent(instanceID)
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
	for _, intent := range intents {
		inst := byID[intent.InstanceID]
		switch intent.Kind {
		case LifecycleIntentArchive:
			if inst == nil || !inst.IsArchived() {
				continue
			}
			tx, lockErr := BeginRuntimeQueueTransaction(intent.InstanceID)
			if lockErr != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive %s: %w", intent.InstanceID, lockErr))
				continue
			}
			if inst.Exists() {
				if killErr := inst.Kill(); killErr != nil {
					tx.Release()
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive stop %s: %w", intent.InstanceID, killErr))
					continue
				}
			}
			if discardErr := tx.Discard(); discardErr != nil {
				tx.Release()
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover archive queue %s: %w", intent.InstanceID, discardErr))
				continue
			}
			tx.Release()
			if completeErr := CompleteLifecycleIntent(storage, intent.InstanceID); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
		case LifecycleIntentRemove, LifecycleIntentWorktreeFinish:
			if inst != nil {
				continue
			}
			tx, lockErr := BeginRuntimeQueueTransaction(intent.InstanceID)
			if lockErr != nil {
				recoveryErr = errors.Join(recoveryErr, lockErr)
				continue
			}
			if discardErr := tx.Discard(); discardErr != nil {
				tx.Release()
				recoveryErr = errors.Join(recoveryErr, discardErr)
				continue
			}
			tx.Release()
			if completeErr := CompleteLifecycleIntent(storage, intent.InstanceID); completeErr != nil {
				recoveryErr = errors.Join(recoveryErr, completeErr)
			}
		}
	}
	return recoveryErr
}
