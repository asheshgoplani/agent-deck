package main

import "fmt"

// startupCreationRollback compensates the single-call startup-query creation
// route. Call failures explicitly: os.Exit in CLI handlers skips deferred work.
// Keeping operations injectable exercises the same failure boundary as the CLI.
type startupCreationRollback struct {
	id             string
	removeRow      func() error
	stop           func() error
	cleanup        func() error
	startAttempted bool
}

func (r *startupCreationRollback) run(stage string, action func() error) error {
	if r != nil && stage == "start session" {
		r.startAttempted = true
	}
	err := action()
	if err == nil || r == nil {
		return err
	}
	if r.startAttempted {
		if stopErr := r.stop(); stopErr != nil {
			return fmt.Errorf("%w; rollback incomplete for session %s: stop: %v", err, r.id, stopErr)
		}
	}
	if removeErr := r.removeRow(); removeErr != nil {
		return fmt.Errorf("%w; rollback incomplete for session %s: remove row: %v", err, r.id, removeErr)
	}
	if cleanupErr := r.cleanup(); cleanupErr != nil {
		return fmt.Errorf("%w; rollback incomplete for session %s: owned artifacts: %v", err, r.id, cleanupErr)
	}
	return fmt.Errorf("%w; failed creation %s rolled back", err, r.id)
}
