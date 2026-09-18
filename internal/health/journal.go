package health

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session event kinds. One JSONL line per event, written by whichever process
// observed it: the transition daemon (status), the CLI (send, restart, stop)
// and the task-worker wrapper (worker_done).
const (
	KindStatus     = "status"
	KindSend       = "send"
	KindRestart    = "restart"
	KindStop       = "stop"
	KindWorkerDone = "worker_done"
)

// Send outcomes recorded in a send event's detail.outcome.
const (
	SendConfirmed   = "confirmed"
	SendUnconfirmed = "delivered-unconfirmed"
	SendFailed      = "failed"
)

// Event is one line of the per-profile session journal. Unknown fields are
// absent, never zero: From/To only carry observed statuses and Detail only
// what the writer actually measured.
type Event struct {
	TS        time.Time      `json:"ts"`
	SessionID string         `json:"session_id"`
	Kind      string         `json:"kind"`
	From      string         `json:"from,omitempty"`
	To        string         `json:"to,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Journal appends session events to sessions-YYYYMMDD.jsonl files in the
// profile's health directory: one file per UTC day, the same 1 MiB size cap
// (rotating to .1) and the same 7-day retention sweep as the health samples.
// A nil Journal is the kill switch: every Append is a no-op.
type Journal struct {
	dir       string
	mu        sync.Mutex
	lastClean time.Time
}

func NewJournal(dir string) *Journal { return &Journal{dir: dir} }

func validKind(kind string) bool {
	switch kind {
	case KindStatus, KindSend, KindRestart, KindStop, KindWorkerDone:
		return true
	}
	return false
}

// valid is the shared gate for writing and reading a line: a timestamp, a
// session id and a known kind.
func (e Event) valid() bool {
	return !e.TS.IsZero() && e.SessionID != "" && validKind(e.Kind)
}

// Append writes one event. It is safe across processes: lines are short
// O_APPEND writes, and a concurrent rotation loses at most the rename race.
func (j *Journal) Append(e Event) error {
	if j == nil {
		return nil
	}
	if !e.valid() {
		return fmt.Errorf("session event needs a timestamp, a session id and a known kind")
	}
	e.TS = e.TS.UTC()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.dir, 0700); err != nil {
		return err
	}
	if now := time.Now().UTC(); now.Sub(j.lastClean) > time.Hour {
		j.lastClean = now
		clean(j.dir, now)
	}
	return appendLine(filepath.Join(j.dir, "sessions-"+e.TS.Format("20060102")+".jsonl"), data)
}

func appendLine(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("journal path is not a regular file")
		}
		if info.Size()+int64(len(data)) > maxFileBytes {
			if err := os.Rename(path, path+".1"); err != nil {
				return err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func sessionEventFile(name string) bool {
	return strings.HasPrefix(name, "sessions-") && healthFile(name)
}

// ReadEvents returns the events with since <= ts < until, oldest first. Only
// bounded regular files are read (at most 8 under the retention rules).
// incomplete reports a partial or corrupt line, tolerated like Report does.
func ReadEvents(dir string, since, until time.Time) (events []Event, incomplete bool, err error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	for _, entry := range entries {
		if !sessionEventFile(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFileBytes {
			incomplete = true
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			incomplete = true
			continue
		}
		reader := bufio.NewReader(io.LimitReader(file, maxFileBytes+1))
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				var e Event
				if line[len(line)-1] != '\n' || json.Unmarshal(line, &e) != nil || !e.valid() {
					incomplete = true
				} else if !e.TS.Before(since) && e.TS.Before(until) {
					events = append(events, e)
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					incomplete = true
				}
				break
			}
		}
		_ = file.Close()
	}
	sort.SliceStable(events, func(i, k int) bool { return events[i].TS.Before(events[k].TS) })
	return events, incomplete, nil
}
