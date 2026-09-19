package recall

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The hook queue (recall/queue.jsonl beside recall.db) is how a Claude
// Stop or SessionEnd hook, `session stop`, a restart or a task-worker
// completion tells the next sweep which transcript just moved, without
// opening the database or taking the sweep lock: one appended line, one
// syscall, behind recover() at every call site. A sweep drains it first
// and parses the listed files before anything else, so the file a user is
// about to search is fresh even when the bounded interactive budget would
// not reach it in a full walk. The queue is advisory: every path in it is
// re-validated against the recall roots before it is opened, and a sweep
// that never sees the queue still finds the same files by walking.

// QueueEntry is one appended line.
type QueueEntry struct {
	TS       int64  `json:"ts"`
	Harness  string `json:"harness"`
	Path     string `json:"path"`
	Event    string `json:"event,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// maxQueueBytes bounds the queue file: past it, Enqueue drops the line
// rather than grow without a sweep (the sweep's walk finds the file
// anyway).
const maxQueueBytes = 4 << 20

// Enqueue appends one entry to the queue at path. It never creates
// anything but the queue file and its directory, never blocks, and
// returns an error only for the caller's log.
func Enqueue(path string, e QueueEntry) error {
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("recall: enqueue: empty path")
	}
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > maxQueueBytes {
		return errors.New("recall: queue full; a sweep will drain it")
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// Drain moves the queue aside (rename, so a hook appending concurrently
// starts a fresh file), reads it, deletes it and returns the entries
// newest first with duplicate paths collapsed. No queue means no entries
// and no error.
func Drain(path string) ([]QueueEntry, error) {
	tmp := path + ".draining"
	if err := os.Rename(path, tmp); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer os.Remove(tmp)
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []QueueEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e QueueEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Path == "" {
			continue // a torn or foreign line is not worth failing the sweep
		}
		entries = append(entries, e)
	}
	seen := map[string]bool{}
	out := make([]QueueEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		if seen[entries[i].Path] {
			continue
		}
		seen[entries[i].Path] = true
		out = append(out, entries[i])
	}
	return out, sc.Err()
}

// QueueLen reports how many lines wait in the queue (0 when absent).
func QueueLen(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if len(strings.TrimSpace(sc.Text())) > 0 {
			n++
		}
	}
	return n
}
