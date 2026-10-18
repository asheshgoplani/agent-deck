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
// The match is on turn identity first. A read records the transcript uuid of
// the Claude assistant record it printed; a record carries the uuid of the
// assistant record holding the turn's text (TurnUUID). Only equal uuids
// withdraw. Time alone cannot do this safely: a hook-driven record is
// stamped with the hook's whole second, and Stop can fire before the final
// assistant record is written (#1186), so a read that looks "after" the
// stamp can still have shown mid-turn or previous-turn text. Two more
// guards close the case where that interim text is what both sides name
// (text X, a tool call, then the final text Y not yet flushed, and a parent
// that peeked at X mid-turn):
//
//   - a record built while a tool call or tool result was newer than its
//     text (TurnFacts.FinalTextPending -> TurnTextInterim) is never
//     withdrawn: X is not the final reply;
//   - a read withdraws a record only when it came at or after the record's
//     time (the turn's end), so a mid-turn peek never counts. A record
//     stamped with a whole second (the hook's) needs a read at least one
//     second later, since the turn may have ended anywhere in that second.
//
// A record without a TurnUUID, a read that was clipped, quiet (-q), JSON,
// copied or from the pane, and a non-Claude read never match: the rule only
// errs toward delivering. The comms consumers' ledger path does not consult
// reads either; there a read result is delivered once more, as before.
//
// Layout: <data>/runtime/result-reads/<reader>.json, a map of child id to
// the turn uuids the reader was shown (newest last, at most
// resultReadTurnsKept), the Unix millisecond of each turn's latest read and
// of the latest read overall. Entries older than the consumed-turn TTL are
// pruned on write; the file goes with the reader's other per-parent files
// when the reader session is removed.

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
	// ReadAt is the Unix ms of the latest read of each turn in Turns. A read
	// withdraws a record only when it came at or after the record's time
	// (the turn's end), so a mid-turn peek never counts. A file written
	// before this field existed has none and withdraws nothing.
	ReadAt map[string]int64 `json:"read_at,omitempty"`
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
	readAt := make(map[string]int64, len(entry.Turns))
	for _, turn := range entry.Turns {
		if ms, ok := entry.ReadAt[turn]; ok {
			readAt[turn] = ms
		}
	}
	readAt[turnUUID] = max(readAt[turnUUID], at.UnixMilli())
	entry.ReadAt = readAt
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
// uuid of its final assistant record (not interim text the final reply had
// not replaced yet), and the reader read that same record at or after the
// record's time, so the read came once the turn had ended.
func (r resultReads) covers(ev TransitionNotificationEvent) bool {
	if len(r) == 0 || !noticeCarriesResult(ev) || ev.TurnTextInterim || ev.Timestamp.IsZero() {
		return false
	}
	entry := r[strings.TrimSpace(ev.ChildSessionID)]
	uuid := strings.TrimSpace(ev.TurnUUID)
	readAt, ok := entry.ReadAt[uuid]
	return ok && slices.Contains(entry.Turns, uuid) && readAt >= turnEndedByMillis(ev.Timestamp)
}

// turnEndedByMillis is the earliest Unix millisecond a read must have, to
// have come after the turn the record announces ended. A hook-driven record
// is stamped with the hook's whole second, so the turn may have ended
// anywhere inside that second: a read in the same second can still be a
// mid-turn peek (Stop fired after the interim text was written but before
// the tool record). For a whole-second stamp the read must therefore come
// at least one second after it; a sub-second stamp is used as is.
func turnEndedByMillis(stamp time.Time) int64 {
	if stamp.Nanosecond() == 0 {
		return stamp.Add(time.Second).UnixMilli()
	}
	return stamp.UnixMilli()
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
