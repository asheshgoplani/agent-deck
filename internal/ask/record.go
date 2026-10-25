package ask

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Kind is what a request asks for.
type Kind string

const (
	KindSecret Kind = "secret"
	KindChoice Kind = "choice"
	KindText   Kind = "text"
)

// State is a request's lifecycle state. Every state but pending is final.
type State string

const (
	StatePending   State = "pending"
	StateStored    State = "stored"
	StateAnswered  State = "answered"
	StateCancelled State = "cancelled"
	StateExpired   State = "expired"
)

// Seen is one UI's announcement that it shows the request, with the nonce
// that UI must send back in the deliver header. Nonces stay in the record
// file; the public views (List, Show) drop them.
type Seen struct {
	By    string    `json:"by"`
	Nonce string    `json:"nonce,omitempty"`
	At    time.Time `json:"at"`
}

// Record is one request (<profile>/ask/<request_id>.json, 0600). It never
// holds a secret value; Answer (choice/text only) is cleared once the asker
// printed it, and List/Show never return it.
type Record struct {
	RequestID      string    `json:"request_id"`
	Kind           Kind      `json:"kind"`
	SessionID      string    `json:"session_id"`
	SessionTitle   string    `json:"session_title"`
	Host           string    `json:"host"`
	Profile        string    `json:"profile"`
	Name           string    `json:"name,omitempty"`
	Question       string    `json:"question,omitempty"`
	Options        []string  `json:"options,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	Replace        bool      `json:"replace"`
	CallerVerified bool      `json:"caller_verified"`
	RequestedAt    time.Time `json:"requested_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	PID            int       `json:"pid"`
	AskEventID     string    `json:"ask_event_id,omitempty"`
	State          State     `json:"state"`
	Answer         string    `json:"answer,omitempty"`
	AnsweredBy     string    `json:"answered_by,omitempty"`
	Replaced       string    `json:"replaced,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitzero"`
	SeenBy         []string  `json:"seen_by,omitempty"`
	Seen           []Seen    `json:"seen,omitempty"`
	// CancelledUnshown: cancelled with `ask cancel --unshown` (a UI that
	// never showed it, e.g. muted in the app), allowed only before any seen.
	CancelledUnshown bool `json:"cancelled_unshown,omitempty"`
}

// public is the view List and Show return: no answer, no nonces, and a
// pending request past its deadline reads as expired.
func (r Record) public() Record {
	r.Answer = ""
	if len(r.Seen) > 0 {
		seen := make([]Seen, len(r.Seen))
		for i, s := range r.Seen {
			seen[i] = Seen{By: s.By, At: s.At}
		}
		r.Seen = seen
	}
	if r.State == StatePending && !now().Before(r.ExpiresAt) {
		r.State = StateExpired
	}
	return r
}

func (r Record) finished() bool { return r.State != StatePending }

// now is the clock for deadlines and rate windows; tests swap it.
var now = time.Now

const (
	dirName        = "ask"
	trashDirName   = "trash"
	lockFileName   = ".lock"
	rateFileName   = "rate.json"
	maxRecordBytes = 256 << 10
	lockTimeout    = 45 * time.Second
	retention      = 24 * time.Hour
)

var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func validID(id string) bool { return idRe.MatchString(id) }

// newID returns 16 hex characters from crypto/rand.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Dir is the profile's ask dir (<profile dir>/ask). profile must already be
// resolved (session.ResolveProfileForStorage / GetEffectiveProfile).
func Dir(profile string) (string, error) {
	pd, err := session.GetProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(pd, dirName), nil
}

// openDir returns the ask dir, creating it (0700) when create is set. It
// refuses a dir that is a symlink, not owned by this user, or not 0700:
// records are read from it and a value's destination comes from them. With
// create unset, a missing dir is reported as ("", nil).
func openDir(profile string, create bool) (string, error) {
	dir, err := Dir(profile)
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create ask dir: %w", err)
		}
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) && !create {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("ask dir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("ask dir %s must be a real directory, not a symlink", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return "", fmt.Errorf("ask dir %s must be mode 0700, is %04o", dir, info.Mode().Perm())
	}
	if uid := ownerUID(info); uid != os.Getuid() {
		return "", fmt.Errorf("ask dir %s is owned by uid %d, not this user", dir, uid)
	}
	return dir, nil
}

func ownerUID(info os.FileInfo) int {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// dirMu serializes goroutines of this process (flock is per open file
// description, so two goroutines would not exclude each other through it).
var dirMu sync.Map // dir -> *sync.Mutex

// lockDir takes the ask dir lock (in-process mutex plus flock on ask/.lock),
// waiting at most lockTimeout. Deliver holds it while the vault runs, so a
// state change is never interleaved with a vault write.
func lockDir(dir string) (func(), error) {
	muAny, _ := dirMu.LoadOrStore(dir, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	// Read-only: the lock file is never written, and flock needs no write
	// access, so no writable handle is left whose Close could lose data.
	f, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		mu.Unlock()
		return nil, fmt.Errorf("open ask lock: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			mu.Unlock()
			return nil, fmt.Errorf("lock ask dir: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		mu.Unlock()
	}, nil
}

// readPrivateFile reads dir/name without following a symlink at name.
func readPrivateFile(dir, name string) ([]byte, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink; refusing it", name)
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return io.ReadAll(io.LimitReader(f, maxRecordBytes))
}

// writePrivateFile replaces dir/name atomically with a new 0600 file. Unlike
// internal/atomicfile it never follows a symlink at name: rename replaces
// the link itself, so a planted link cannot redirect the write.
func writePrivateFile(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	ok = true
	return nil
}

func readRecord(dir, id string) (Record, error) {
	if !validID(id) {
		return Record{}, ErrNotFound
	}
	data, err := readPrivateFile(dir, id+".json")
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read request %s: %w", id, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("read request %s: %w", id, err)
	}
	if rec.RequestID != id {
		return Record{}, fmt.Errorf("read request %s: the record names another request", id)
	}
	return rec, nil
}

func writeRecord(dir string, rec Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(dir, rec.RequestID+".json", append(data, '\n')); err != nil {
		return fmt.Errorf("write request %s: %w", rec.RequestID, err)
	}
	return nil
}

// loadAll reads every record in dir, oldest first; unreadable ones are skipped.
func loadAll(dir string) []Record {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" || !validID(name[:len(name)-len(".json")]) {
			continue
		}
		rec, err := readRecord(dir, name[:len(name)-len(".json")])
		if err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out
}

// sweep moves finished records older than retention to ask/trash (never
// deleted) and returns the rest.
func sweep(dir string, recs []Record) []Record {
	var keep []Record
	for _, r := range recs {
		if r.finished() && !r.FinishedAt.IsZero() && now().Sub(r.FinishedAt) > retention {
			trash := filepath.Join(dir, trashDirName)
			if os.MkdirAll(trash, 0o700) == nil && os.Rename(filepath.Join(dir, r.RequestID+".json"), filepath.Join(trash, r.RequestID+".json")) == nil {
				continue
			}
		}
		keep = append(keep, r)
	}
	return keep
}

// rateFile is ask/rate.json: request times (unix seconds) per session and
// for the whole profile, for the sliding one-hour limits.
type rateFile struct {
	Sessions map[string][]int64 `json:"sessions"`
	Profile  []int64            `json:"profile"`
}

// Rate limits per sliding hour.
const (
	PerSessionPerHour = 5
	PerProfilePerHour = 20
)

func readRate(dir string) rateFile {
	r := rateFile{Sessions: map[string][]int64{}}
	if data, err := readPrivateFile(dir, rateFileName); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	if r.Sessions == nil {
		r.Sessions = map[string][]int64{}
	}
	cutoff := now().Add(-time.Hour).Unix()
	prune := func(ts []int64) []int64 {
		var out []int64
		for _, t := range ts {
			if t > cutoff {
				out = append(out, t)
			}
		}
		return out
	}
	for k, v := range r.Sessions {
		if v = prune(v); len(v) == 0 {
			delete(r.Sessions, k)
		} else {
			r.Sessions[k] = v
		}
	}
	r.Profile = prune(r.Profile)
	return r
}

func (r rateFile) allow(sessionID string) error {
	if len(r.Sessions[sessionID]) >= PerSessionPerHour || len(r.Profile) >= PerProfilePerHour {
		return ErrRateLimited
	}
	return nil
}

func (r *rateFile) add(sessionID string) {
	t := now().Unix()
	r.Sessions[sessionID] = append(r.Sessions[sessionID], t)
	r.Profile = append(r.Profile, t)
}

func writeRate(dir string, r rateFile) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writePrivateFile(dir, rateFileName, data)
}
