package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Per-instance hook event history (status-light audit defect B, review round
// 3 P1-2). The hook status file holds only the LAST event, so once a Stop
// arrives late nothing on disk says when the events before it were written.
// Every status-file write also appends one line to
// ~/.agent-deck/hooks/<id>.events.jsonl, bounded to the last
// hookEventHistoryMax events, so the next late Stop can be traced against
// the pane and the transcript after the fact.
//
// Diagnostic only: nothing reads this file on any status path (the hook
// watcher ignores non-.json names, the CLI cold load reads <id>.json). It is
// pruned with the instance's other hook artifacts (session.PruneHookArtifacts)
// and disabled together with the runtime health sampler by
// `[health] enabled = false` in config.toml — the same kill switch, so one
// setting turns off every local runtime observation.

const (
	hookEventHistorySuffix = ".events.jsonl"
	hookEventHistoryMax    = 200
	// hookEventHistoryReadLimit bounds the rewrite read: 200 lines of ~150
	// bytes is ~30 KiB; anything beyond is cut to the tail.
	hookEventHistoryReadLimit = 256 << 10
	// hookEventMinLineBytes is a floor no event line goes under (the "at"
	// timestamp alone is 30 bytes), so a file of at most max×floor bytes
	// cannot hold more than max lines and the append needs no read.
	hookEventMinLineBytes = 48
)

// hookEvent is one line of the history.
type hookEvent struct {
	// At is the hook-handler's wall clock at write time, to the millisecond
	// (the status file's ts is whole seconds, too coarse to order a Stop
	// against the turn_duration record in the transcript).
	At        string `json:"at"`
	Event     string `json:"event"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	PID       int    `json:"pid"`
	// Sequence is the Hermes generation-lock sequence when one applies.
	Sequence uint64 `json:"sequence,omitempty"`
}

// hookEventHistoryEnabled shares the health sampler's kill switch.
func hookEventHistoryEnabled() bool {
	config, err := session.LoadUserConfig()
	return err == nil && config.Health.IsEnabled()
}

// hookEventHistoryPath is the history file for instanceID under hooksDir.
func hookEventHistoryPath(hooksDir, instanceID string) string {
	return filepath.Join(hooksDir, filepath.Base(instanceID)+hookEventHistorySuffix)
}

// appendHookEvent records one written status-file event. Errors are logged
// at debug level and never affect the status write that preceded it.
func appendHookEvent(instanceID string, sf hookStatusFile) {
	if !hookEventHistoryEnabled() {
		return
	}
	ev := hookEvent{
		At: time.Now().UTC().Format(time.RFC3339Nano), Event: sf.Event, Status: sf.Status,
		SessionID: sf.SessionID, PID: os.Getpid(), Sequence: sf.Sequence,
	}
	if err := appendBoundedJSONL(hookEventHistoryPath(getHooksDir(), instanceID), ev, hookEventHistoryMax); err != nil {
		hookHandlerLog.Debug("hook_event_history_failed",
			slog.String("instance", instanceID),
			slog.String("error", err.Error()),
		)
	}
}

// appendBoundedJSONL appends v as one JSON line to path and, when the file
// then holds more than keep lines, rewrites it atomically with the last keep.
// The append itself is a single O_APPEND write, so concurrent hook handlers
// for one instance interleave whole lines; once the file is at the cap every
// append rewrites (~30 KiB read + atomic rename, a few times per minute at
// most), and a rewrite can drop a line appended by another handler between
// its read and its rename, which a diagnostic history tolerates.
func appendBoundedJSONL(path string, v any, keep int) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return trimJSONLTail(path, keep)
}

// trimJSONLTail keeps only the last keep lines of path. A file that cannot
// hold more than keep lines (size ≤ keep×hookEventMinLineBytes) is left
// untouched, so the steady-state append costs one stat and no read; past
// that only the last hookEventHistoryReadLimit bytes are read, cut to a
// line boundary, and the rewrite goes through atomicHookWrite.
func trimJSONLTail(path string, keep int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size <= int64(keep)*hookEventMinLineBytes {
		return nil
	}
	start := max(size-hookEventHistoryReadLimit, 0)
	data := make([]byte, size-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:] // drop the partial first line
		}
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) <= keep {
		return nil
	}
	tail := append(bytes.Join(lines[len(lines)-keep:], []byte("\n")), '\n')
	return atomicHookWrite(path, tail)
}
