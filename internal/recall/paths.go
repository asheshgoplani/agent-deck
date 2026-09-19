package recall

import (
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// recall.db is machine-global, not per profile: its inputs (every Claude
// config dir, later ~/.codex and friends) are machine-global and a
// per-profile placement would mean one full backfill per profile of the
// same corpus. `profile` is a column, not a directory. The names below are
// in agentpaths' migration list so they follow the XDG layout.
const (
	DBName    = "recall.db"
	DirName   = "recall"
	lockName  = "sweep.lock"
	queueName = "queue.jsonl"
)

// DBPath resolves recall.db through agentpaths (never a hardcoded
// ~/.agent-deck literal).
func DBPath() (string, error) {
	return agentpaths.EffectiveDataPath(DBName, DBName, DirName)
}

// LockPath is the machine-global sweep lock beside recall.db.
func LockPath() (string, error) {
	return sidecarPath(lockName)
}

// QueuePath is the hook-appended work queue (phase 3 writes it).
func QueuePath() (string, error) {
	return sidecarPath(queueName)
}

// sidecarPath resolves a file in the recall data dir beside recall.db.
func sidecarPath(name string) (string, error) {
	dir, err := agentpaths.EffectiveDataPath(DirName, DBName, DirName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
