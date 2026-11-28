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
	actionSend    = "send"   // yes -> send Message to Target
	actionSwitch  = "switch" // yes -> show Target, move the mic
	actionChoose  = "choose" // choice -> continue Intent with the chosen session
	actionStop    = "stop"   // yes -> stop Target or Targets
	actionRestart = "restart"
	actionArchive = "archive"
	actionLaunch  = "launch" // yes -> start Launch
)

// Pending is one question the desk asked and waits on. It is stored under
// the profile's state dir, single-use, bound to the profile and to the
// exact action by Hash.
type Pending struct {
	Token     string      `json:"token"`
	Profile   string      `json:"profile"`
	Kind      string      `json:"kind"`   // yes | choice
	Action    string      `json:"action"` // send | switch | choose
	Intent    string      `json:"intent"` // the intent the question belongs to
	Lang      string      `json:"lang"`
	Target    *Ref        `json:"target,omitempty"`
	Choices   []Ref       `json:"choices,omitempty"`
	Message   string      `json:"message,omitempty"`
	Targets   []Ref       `json:"targets,omitempty"` // stop/restart/archive all: at most 5
	Left      int         `json:"left,omitempty"`    // how many more fit but were not asked
	Launch    *LaunchSpec `json:"launch,omitempty"`
	Retries   int         `json:"retries,omitempty"` // a question asked again once
	CreatedAt time.Time   `json:"created_at"`
	Hash      string      `json:"hash"`
}

func (p Pending) binding() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00", p.Token, p.Profile, p.Kind, p.Action, p.Intent)
	if p.Target != nil {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", p.Target.ID, p.Target.Host, p.Target.Profile)
	}
	for _, c := range p.Choices {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", c.ID, c.Host, c.Profile)
	}
	for _, c := range p.Targets {
		fmt.Fprintf(h, "t%s\x00%s\x00%s\x00", c.ID, c.Host, c.Profile)
	}
	if p.Launch != nil {
		fmt.Fprintf(h, "l%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00", p.Launch.Title, p.Launch.Group, p.Launch.Host, p.Launch.Profile, p.Launch.Path, p.Launch.Tool)
	}
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d", p.Message, p.Left, p.Retries, p.CreatedAt.UnixNano())
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
	// MkdirAll keeps the rights of a dir that already exists
	if err := os.Chmod(f.Dir, 0o700); err != nil {
		return fmt.Errorf("securing voice pending dir: %w", err)
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
	p, err := readPending(claimed)
	if err == nil && p.Action != "" {
		_ = os.WriteFile(path+".used", []byte(p.Action), 0o600)
	}
	return p, err
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
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".taken") && !strings.HasSuffix(name, ".used") && !strings.HasPrefix(name, ".pending-") {
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
type MemStore struct {
	m         map[string]Pending
	used      map[string]string
	restarted map[string]time.Time
}

func NewMemStore() *MemStore { return &MemStore{m: map[string]Pending{}, used: map[string]string{}} }

// UsedAction: see FileStore.UsedAction.
func (s *MemStore) UsedAction(token string) string { return s.used[token] }

func (s *MemStore) Put(p Pending) error { s.m[p.Token] = p; return nil }

func (s *MemStore) Sweep() {}

func (s *MemStore) Take(token string) (Pending, error) {
	p, ok := s.m[token]
	if !ok {
		return Pending{}, ErrNoPending
	}
	delete(s.m, token)
	if s.used != nil {
		s.used[token] = p.Action
	}
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

// UsedAction is the action of a token that was already taken ("stop",
// "send"), or "" when the store does not know it: the too-late line of a
// reused token then says "nothing done" or "nothing sent" as fits. Only
// the action word is kept, never the message, and it dies with the sweep.
func (f FileStore) UsedAction(token string) string {
	path, err := f.path(token)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path + ".used")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// usedActions is a Store that remembers the action of a taken token.
type usedActions interface {
	UsedAction(token string) string
}

// recentRestarts is a Store that remembers which sessions a bulk restart
// restarted a moment ago. A restart does not change a session's state, so
// without it "say it again for them" would ask about the same first five.
// Only the host and id are kept, with the time, never a title or message.
type recentRestarts interface {
	MarkRestarted(ss []Session, at time.Time)
	RestartedSince(since time.Time) map[string]bool // key: restartKey(host, id)
}

func restartKey(host, id string) string { return host + "\x00" + id }

const restartsFile = "recent-restarts"

// MarkRestarted: see recentRestarts. Entries older than RecentRestartWindow
// are dropped on every write.
func (f FileStore) MarkRestarted(ss []Session, at time.Time) {
	keep := f.readRestarts(at.Add(-RecentRestartWindow))
	for _, s := range ss {
		if s.ID == "" || strings.ContainsAny(s.ID+s.Host, " \n") {
			continue
		}
		keep = append(keep, restartEntry{at: at, host: s.Host, id: s.ID})
	}
	var b strings.Builder
	for _, e := range keep {
		host := e.host
		if host == "" {
			host = "-"
		}
		fmt.Fprintf(&b, "%d %s %s\n", e.at.UnixNano(), host, e.id)
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(f.Dir, ".restarts-*")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(b.String())
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Chmod(tmp.Name(), 0o600) != nil {
		os.Remove(tmp.Name())
		return
	}
	if os.Rename(tmp.Name(), filepath.Join(f.Dir, restartsFile)) != nil {
		os.Remove(tmp.Name())
	}
}

// RestartedSince: see recentRestarts.
func (f FileStore) RestartedSince(since time.Time) map[string]bool {
	out := map[string]bool{}
	for _, e := range f.readRestarts(since) {
		out[restartKey(e.host, e.id)] = true
	}
	return out
}

type restartEntry struct {
	at       time.Time
	host, id string
}

func (f FileStore) readRestarts(since time.Time) []restartEntry {
	b, err := os.ReadFile(filepath.Join(f.Dir, restartsFile))
	if err != nil {
		return nil
	}
	var out []restartEntry
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		var ns int64
		if _, err := fmt.Sscanf(parts[0], "%d", &ns); err != nil {
			continue
		}
		at := time.Unix(0, ns)
		if at.Before(since) {
			continue
		}
		host := parts[1]
		if host == "-" {
			host = ""
		}
		out = append(out, restartEntry{at: at, host: host, id: parts[2]})
	}
	return out
}

// MarkRestarted: see FileStore.MarkRestarted.
func (s *MemStore) MarkRestarted(ss []Session, at time.Time) {
	if s.restarted == nil {
		s.restarted = map[string]time.Time{}
	}
	for _, x := range ss {
		s.restarted[restartKey(x.Host, x.ID)] = at
	}
}

// RestartedSince: see FileStore.RestartedSince.
func (s *MemStore) RestartedSince(since time.Time) map[string]bool {
	out := map[string]bool{}
	for k, at := range s.restarted {
		if !at.Before(since) {
			out[k] = true
		}
	}
	return out
}
