// Package sendqueue is the durable outbox behind `agent-deck session send
// --queue`: one JSON record per send under <profile dir>/sendqueue/, walked
// in order by a detached per-target worker that waits for the target to be
// idle, delivers through the normal `session send` path, and watches the
// native transcript until the message lands. A queued send is never
// dropped silently: it ends as landed, or failed with a reason.
package sendqueue

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// States of a queued send, in order. failed is terminal like landed.
const (
	StateQueued    = "queued"    // waiting for the target to be idle
	StateTyped     = "typed"     // typed into the composer, submission not confirmed
	StateSubmitted = "submitted" // submission confirmed by the harness
	StateLanded    = "landed"    // the text is in the transcript (landed_row_id)
	StateFailed    = "failed"    // gave up; reason says why
)

// DefaultRetryBudget is how long a send may wait for a busy target.
const DefaultRetryBudget = 30 * time.Minute

// Record is one queued send. It is also the `send-status --json` object.
type Record struct {
	SendID         string   `json:"send_id"`
	State          string   `json:"state"`
	Reason         string   `json:"reason"`
	TargetStatus   string   `json:"target_status"`
	SessionID      string   `json:"session_id"`
	SessionTitle   string   `json:"session_title,omitempty"`
	Tool           string   `json:"tool,omitempty"`
	Message        string   `json:"message"`
	Images         []string `json:"images,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	Deadline       string   `json:"deadline"`
	Attempts       int      `json:"attempts"`
	SentAt         string   `json:"sent_at,omitempty"`
	TranscriptPath string   `json:"transcript_path,omitempty"`
	TranscriptFrom int64    `json:"transcript_from,omitempty"`
	LandedRowID    string   `json:"landed_row_id,omitempty"`
	LandedAt       string   `json:"landed_at,omitempty"`
	// Settled marks a typed/submitted send whose text was not found in the
	// transcript within the watch window: it is never typed again.
	Settled bool `json:"settled,omitempty"`
}

// Final reports whether the worker is done with the record.
func (r *Record) Final() bool { return r.State == StateLanded || r.State == StateFailed || r.Settled }

// Dir is the queue directory of a profile.
func Dir(profileDir string) string { return filepath.Join(profileDir, "sendqueue") }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID: 48-bit milliseconds then 80 random bits, Crockford
// base32, so ids sort by creation time.
func NewID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:])
	// 128 bits -> 26 chars, 5 bits each, most significant first.
	out := make([]byte, 26)
	var acc uint64
	bits := uint(2) // leading pad: 26*5 = 130 bits
	idx := 0
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[idx] = crockford[(acc>>bits)&31]
			idx++
		}
	}
	return string(out[:idx])
}

func validID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune(crockford, c) {
			return false
		}
	}
	return true
}

// Save writes a record atomically.
func Save(dir string, r *Record) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+r.SendID+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, r.SendID+".json"))
}

// ErrUnknown is returned for a send id with no record.
var ErrUnknown = errors.New("sendqueue: unknown send id")

// Load reads one record.
func Load(dir, id string) (*Record, error) {
	if !validID(id) {
		return nil, ErrUnknown
	}
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if os.IsNotExist(err) {
		return nil, ErrUnknown
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// List returns every record, oldest first; sessionID filters when set.
func List(dir, sessionID string) ([]*Record, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := Load(dir, strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if sessionID == "" || r.SessionID == sessionID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SendID < out[j].SendID })
	return out, nil
}

// Update loads, mutates and saves a record, stamping updated_at.
func Update(dir, id string, now time.Time, fn func(*Record)) (*Record, error) {
	r, err := Load(dir, id)
	if err != nil {
		return nil, err
	}
	fn(r)
	r.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	return r, Save(dir, r)
}

// Lock is a held per-target worker lock.
type Lock struct{ f *os.File }

// TryLock takes the target's worker lock without blocking; ok is false
// when another worker owns the target.
func TryLock(dir, sessionID string) (*Lock, bool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	name := strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(sessionID)
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sendqueue: lock %s: %w", sessionID, err)
	}
	return &Lock{f: f}, true, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	if l != nil && l.f != nil {
		_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
		l.f.Close()
		l.f = nil
	}
}
