package voicedesk

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Confirm windows (CONTRACT.md section 4 rule 3).
const (
	AnswerWindow = 10 * time.Second // a yes later than this sends nothing
	HardTTL      = 60 * time.Second // a token is dead after this, whatever the delay says
)

// Pending actions.
const (
	actionSend   = "send"   // yes -> send Message to Target
	actionSwitch = "switch" // yes -> show Target, move the mic
	actionChoose = "choose" // choice -> continue Intent with the chosen session
)

// Pending is one question the desk asked and waits on. It is stored under
// the profile's state dir, single-use, bound to the profile and to the
// exact action by Hash.
type Pending struct {
	Token     string    `json:"token"`
	Profile   string    `json:"profile"`
	Kind      string    `json:"kind"`   // yes | choice
	Action    string    `json:"action"` // send | switch | choose
	Intent    string    `json:"intent"` // the intent the question belongs to
	Lang      string    `json:"lang"`
	Target    *Ref      `json:"target,omitempty"`
	Choices   []Ref     `json:"choices,omitempty"`
	Message   string    `json:"message,omitempty"`
	Retries   int       `json:"retries,omitempty"` // a question asked again once
	CreatedAt time.Time `json:"created_at"`
	Hash      string    `json:"hash"`
}

func (p Pending) binding() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00", p.Token, p.Profile, p.Kind, p.Action, p.Intent)
	if p.Target != nil {
		fmt.Fprintf(h, "%s\x00%s\x00", p.Target.ID, p.Target.Host)
	}
	for _, c := range p.Choices {
		fmt.Fprintf(h, "%s\x00%s\x00", c.ID, c.Host)
	}
	fmt.Fprintf(h, "%s\x00%d\x00%d", p.Message, p.Retries, p.CreatedAt.UnixNano())
	return hex.EncodeToString(h.Sum(nil))
}

// Seal sets the binding hash.
func (p *Pending) Seal() { p.Hash = p.binding() }

// Valid checks the binding hash and the profile.
func (p Pending) Valid(profile string) bool {
	return p.Hash != "" && p.Hash == p.binding() && p.Profile == profile
}

// Store keeps pending questions.
type Store interface {
	Put(p Pending) error
	// Take returns the pending question and removes it (single use).
	Take(token string) (Pending, error)
	// Peek returns it without removing it (--dry-run).
	Peek(token string) (Pending, error)
	// Sweep removes every question older than HardTTL. It runs on every
	// handled call, so an unanswered message does not stay on disk.
	Sweep()
}

// ErrNoPending: unknown, used or swept token.
var ErrNoPending = errors.New("no pending question with this token")

var tokenShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NewToken is 128 random bits, hex.
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// FileStore keeps one 0600 file per token in Dir (0700). Nothing in it is
// a log: the file holds the pending message only until it is answered or
// dies, and a sweep removes every file older than HardTTL.
type FileStore struct {
	Dir string
	Now func() time.Time
}

func (f FileStore) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f FileStore) path(token string) (string, error) {
	if !tokenShape.MatchString(token) {
		return "", ErrNoPending
	}
	return filepath.Join(f.Dir, token+".json"), nil
}

func (f FileStore) Put(p Pending) error {
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("creating voice pending dir: %w", err)
	}
	f.Sweep()
	path, err := f.path(p.Token)
	if err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, ".pending-*")
	if err != nil {
		return fmt.Errorf("writing voice pending file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("writing voice pending file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Take claims the file by renaming it first, so two answers racing for the
// same token cannot both act.
func (f FileStore) Take(token string) (Pending, error) {
	path, err := f.path(token)
	if err != nil {
		return Pending{}, err
	}
	claimed := path + ".taken"
	if err := os.Rename(path, claimed); err != nil {
		return Pending{}, ErrNoPending
	}
	defer os.Remove(claimed)
	return readPending(claimed)
}

func (f FileStore) Peek(token string) (Pending, error) {
	path, err := f.path(token)
	if err != nil {
		return Pending{}, err
	}
	return readPending(path)
}

func readPending(path string) (Pending, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Pending{}, ErrNoPending
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return Pending{}, ErrNoPending
	}
	return p, nil
}

// Sweep removes dead tokens (older than HardTTL) and stale claims.
func (f FileStore) Sweep() {
	ents, err := os.ReadDir(f.Dir)
	if err != nil {
		return
	}
	cutoff := f.now().Add(-HardTTL)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".taken") && !strings.HasPrefix(name, ".pending-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(f.Dir, name))
	}
}

// MemStore is a Store in memory (tests).
type MemStore struct{ m map[string]Pending }

func NewMemStore() *MemStore { return &MemStore{m: map[string]Pending{}} }

func (s *MemStore) Put(p Pending) error { s.m[p.Token] = p; return nil }

func (s *MemStore) Sweep() {}

func (s *MemStore) Take(token string) (Pending, error) {
	p, ok := s.m[token]
	if !ok {
		return Pending{}, ErrNoPending
	}
	delete(s.m, token)
	return p, nil
}

func (s *MemStore) Peek(token string) (Pending, error) {
	p, ok := s.m[token]
	if !ok {
		return Pending{}, ErrNoPending
	}
	return p, nil
}

// Len is the number of stored questions (tests).
func (s *MemStore) Len() int { return len(s.m) }
