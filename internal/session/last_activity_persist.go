// Issue #1846: the TUI's "ago" surfaces went stale because every trace of
// real agent activity was ephemeral — the hook status file is deleted on
// attach-return (deliberately, so a fresh-looking "waiting" file cannot mask
// a pane killed by /q), the tmux tracker's confirmed activity is process-
// local, and MarkAccessed only reached SQLite on the next full save cycle.
// After a TUI restart the row badge collapsed to CreatedAt while the preview
// fell back to a stale LastAccessedAt — two different wrong ages for the
// same session.
//
// tool_data.last_activity_at is the durable record that closes the gap,
// using the same extras-zone mechanism as #1704's last_started_at: merged
// into the tool_data JSON blob outside the positional MarshalToolData
// signature, so legacy binaries preserve it via MergeToolDataExtras and
// legacy rows read as zero ("unknown").
package session

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const toolDataLastActivityAtKey = "last_activity_at"

// lastActivityPersistInterval throttles the targeted DB write on the hook
// observation path: a busy agent fires several hook events per turn, and
// each observation would otherwise cost an UPDATE. Evidence-destroying
// paths (ClearHookStatus) bypass the throttle with force=true, so at most
// one interval of precision is ever at risk — and only if the process dies
// without passing through ClearHookStatus.
const lastActivityPersistInterval = 60 * time.Second

// WriteLastActivityAtToToolData merges last_activity_at into the given
// tool_data JSON blob as a Unix-seconds integer. A zero time removes the
// key, keeping never-active sessions indistinguishable from rows saved by
// an older binary.
func WriteLastActivityAtToToolData(td json.RawMessage, t time.Time) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if !t.IsZero() {
		raw, _ := json.Marshal(t.Unix())
		m[toolDataLastActivityAtKey] = raw
	} else {
		delete(m, toolDataLastActivityAtKey)
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadLastActivityAtFromToolData extracts last_activity_at from the blob.
// Returns the zero time for missing/malformed/legacy rows — callers must
// treat that as "unknown", never as "active at epoch".
func ReadLastActivityAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		LastActivityAt int64 `json:"last_activity_at"`
	}
	_ = json.Unmarshal(td, &blob)
	if blob.LastActivityAt == 0 {
		return time.Time{}
	}
	return time.Unix(blob.LastActivityAt, 0).UTC()
}

// LastActivityAt returns the durable last-activity timestamp: the most
// recent hook-evidenced agent activity this or any previous process
// recorded. Zero means no activity has ever been recorded (legacy row or
// genuinely never active). Thread-safe.
func (i *Instance) LastActivityAt() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.lastActivityAt
}

// noteAgentActivityLocked folds t into the in-memory last-activity record
// (monotonic: an older timestamp never rewinds it). Caller must hold i.mu.
//
// Deliberately memory-only: the SQLite flush lives in persistLastActivity,
// which callers invoke AFTER releasing i.mu. WriteLastActivityAt goes
// through withBusyRetry on connections with busy_timeout=5000, so under
// contention a single write can stall for seconds — and i.mu gates the TUI
// render path, so holding it across the write would freeze the UI
// (CodeRabbit finding on #1847).
func (i *Instance) noteAgentActivityLocked(t time.Time) {
	if !t.IsZero() && t.After(i.lastActivityAt) {
		i.lastActivityAt = t
	}
}

// persistLastActivity flushes the in-memory last-activity record to SQLite.
// Caller must NOT hold i.mu — the lock is taken only briefly to snapshot
// state on either side of the (potentially slow) DB write. force bypasses
// the write throttle; pass true when the evidence backing the in-memory
// value is about to be destroyed (ClearHookStatus deleting the hook file).
//
// lastActivityPersistMu serializes persists per instance so two concurrent
// callers cannot write out of order (an older snapshot landing after a
// newer one would leave the DB behind the throttle marker forever); each
// caller re-snapshots the latest value once it holds the persist lock, so
// writes are strictly monotonic.
func (i *Instance) persistLastActivity(force bool) {
	i.lastActivityPersistMu.Lock()
	defer i.lastActivityPersistMu.Unlock()

	i.mu.RLock()
	to := i.lastActivityAt
	skip := to.IsZero() || !to.After(i.lastActivityPersisted) ||
		(!force && to.Sub(i.lastActivityPersisted) < lastActivityPersistInterval)
	i.mu.RUnlock()
	if skip {
		return
	}

	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	if err := db.WriteLastActivityAt(i.ID, to); err != nil {
		sessionLog.Debug("last_activity_persist_failed",
			slog.String("instance", i.ID),
			slog.String("error", err.Error()),
		)
		return
	}

	i.mu.Lock()
	if to.After(i.lastActivityPersisted) {
		i.lastActivityPersisted = to
	}
	i.mu.Unlock()
}
