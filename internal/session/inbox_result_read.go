package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// Self-withdrawing notices. A parent that already read a child's result
// (`agent-deck session output <child>` run from inside the parent) gains
// nothing from the inbox notice for that same result: waking it, blocking
// its Stop or injecting the record again only buys a turn that replies to
// nothing. So the read is remembered per reader as the exact turn it showed,
// and a notice for that turn withdraws itself: its wake is not typed, the
// Stop hook does not block for it, the digest does not count it, and the
// drain retires it as consumed without showing it.
//
// The match is on turn identity, never on time. A read records the
// transcript uuid of the Claude assistant record it printed; a record
// carries the uuid of the assistant record holding the turn's final text
// (TurnUUID). Only equal uuids withdraw. Timestamps cannot do this safely:
// a hook-driven record is stamped with the hook's whole second, and Stop can
// fire before the final assistant record is written (#1186), so a read that
// looks "after" the stamp can still have shown mid-turn or previous-turn
// text. With uuids such a read names an older record and the notice is
// delivered. A record without a TurnUUID, a read that was clipped or came
// from the pane, and a non-Claude read never match: the rule only errs
// toward delivering.
//
// Layout: <data>/runtime/result-reads/<reader>.json, a map of child id to
// the turn uuids the reader was shown (newest last, at most
// resultReadTurnsKept) and the Unix millisecond of the latest read. Entries
// older than the consumed-turn TTL are pruned on write.

// resultReadsLockWait bounds the wait for the per-reader file lock. The
// lock is held for one small read-modify-write.
const resultReadsLockWait = 2 * time.Second

func resultReadsPath(readerID string) string {
	return filepath.Join(runtimeDirOrTemp("result-reads"), sanitizeInboxName(readerID)+".json")
}

// resultReadTurnsKept bounds the turn uuids remembered per child: enough
// for a parent that reads a busy child several times before draining.
const resultReadTurnsKept = 8

// resultRead is what one reader read of one child.
type resultRead struct {
	Turns []string `json:"turns"`
	At    int64    `json:"at"` // Unix ms of the latest read; pruning only
}

// resultReads is one reader's reads: child id -> the turns it was shown.
type resultReads map[string]resultRead

func loadResultReads(readerID string) resultReads {
	out := resultReads{}
	if strings.TrimSpace(readerID) == "" {
		return out
	}
	raw, err := os.ReadFile(resultReadsPath(readerID))
	if err != nil {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		return resultReads{} // unreadable: withdraw nothing
	}
	return out
}

// NoteResultRead records that session readerID was shown childID's turn
// turnUUID (the transcript uuid of the assistant record it read) at the
// given time. A read of a session by itself, from outside any session, or
// with no turn identity is ignored.
func NoteResultRead(readerID, childID, turnUUID string, at time.Time) error {
	readerID, childID, turnUUID = strings.TrimSpace(readerID), strings.TrimSpace(childID), strings.TrimSpace(turnUUID)
	if readerID == "" || childID == "" || turnUUID == "" || readerID == childID {
		return nil
	}
	path := resultReadsPath(readerID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := AcquireConfigFileLockTimeout(path, resultReadsLockWait)
	if err != nil {
		return err
	}
	defer lock.Release()
	reads := loadResultReads(readerID)
	entry := reads[childID]
	if !slices.Contains(entry.Turns, turnUUID) {
		entry.Turns = append(entry.Turns, turnUUID)
		if extra := len(entry.Turns) - resultReadTurnsKept; extra > 0 {
			entry.Turns = entry.Turns[extra:]
		}
	}
	entry.At = max(entry.At, at.UnixMilli())
	reads[childID] = entry
	cutoff := at.Add(-consumedTurnsTTL).UnixMilli()
	for child, e := range reads {
		if e.At < cutoff {
			delete(reads, child)
		}
	}
	data, err := json.Marshal(reads)
	if err != nil {
		return err
	}
	// Atomic, not fsync'd: a marker lost to a crash only means the notice
	// is delivered, which is the behaviour without this file.
	return atomicfile.WriteFile(path, data, 0o600)
}

// covers reports whether the reader was already shown the turn ev
// announces: ev is a finished turn of a local child (a status edge to
// waiting or idle, or a completion sentinel), not a question, carries the
// uuid of its final assistant record, and the reader read that same record.
func (r resultReads) covers(ev TransitionNotificationEvent) bool {
	if len(r) == 0 || !noticeCarriesResult(ev) {
		return false
	}
	return slices.Contains(r[strings.TrimSpace(ev.ChildSessionID)].Turns, strings.TrimSpace(ev.TurnUUID))
}

// noticeCarriesResult reports whether a record only announces one result
// the reader can read with `session output`: a finished turn or a
// completion sentinel with a turn identity. A question, an error, an
// overflow digest (it stands for turns the reader never saw) or a record
// pulled from a remote host is never withdrawn.
func noticeCarriesResult(ev TransitionNotificationEvent) bool {
	if ev.Question || ev.SourceRemote != "" || ev.OverflowTurns > 0 ||
		strings.TrimSpace(ev.ChildSessionID) == "" || strings.TrimSpace(ev.TurnUUID) == "" {
		return false
	}
	if ev.Kind != "" {
		return ev.Kind == transitionKindFinished
	}
	return ev.ToStatus == string(StatusWaiting) || ev.ToStatus == string(StatusIdle)
}
