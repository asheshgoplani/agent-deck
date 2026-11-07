package assistant

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// TokenPrefix starts every assistant token, so one is recognisable on sight
// (and refused on owner routes even when the web server has no token).
const TokenPrefix = "adk_"

// File names inside the agent-deck state directory.
const (
	storeFileName = "assistants.json"
	auditFileName = "assistants-audit.jsonl"
	tokenDirName  = "assistants"
)

// Record is one stored token. Only the SHA-256 of the token is kept.
type Record struct {
	Name        string    `json:"name"`
	Assistant   string    `json:"assistant"`
	Scopes      []Scope   `json:"scopes"`
	TokenSHA256 string    `json:"token_sha256"`
	Profile     string    `json:"profile,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type storeFile struct {
	Version int      `json:"version"`
	Tokens  []Record `json:"tokens"`
}

// Store keeps token records in <state dir>/assistants.json (0600). Readers
// re-read the file whenever its mtime or size changes, so a revoke from the
// CLI takes effect on the next call of a running server.
type Store struct {
	dir string

	mu      sync.Mutex
	records map[string]Record
	modTime time.Time
	size    int64
	loaded  bool
}

// NewStore returns a store rooted at the agent-deck state directory dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir is the state directory the store lives in.
func (s *Store) Dir() string { return s.dir }

// Path is the records file.
func (s *Store) Path() string { return filepath.Join(s.dir, storeFileName) }

// AuditPath is the audit log next to the records file.
func (s *Store) AuditPath() string { return filepath.Join(s.dir, auditFileName) }

// TokenFile is where `connect` writes the raw token for name.
func (s *Store) TokenFile(name string) string {
	return filepath.Join(s.dir, tokenDirName, name+".token")
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateName checks a token name: lowercase letters, digits, '.', '_' and
// '-', starting with a letter or digit, at most 64 characters.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: use lowercase letters, digits, '.', '_' or '-' (max 64)", name)
	}
	return nil
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken returns a fresh token: TokenPrefix + 32 random bytes in base32.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return TokenPrefix + strings.ToLower(tokenEncoding.EncodeToString(b[:])), nil
}

// reloadLocked re-reads the records file when it changed on disk.
func (s *Store) reloadLocked() error {
	info, err := os.Stat(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		s.records = map[string]Record{}
		s.loaded, s.modTime, s.size = true, time.Time{}, 0
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking assistant token store: %w", err)
	}
	if s.loaded && info.ModTime().Equal(s.modTime) && info.Size() == s.size {
		return nil
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		return fmt.Errorf("reading assistant token store: %w", err)
	}
	var f storeFile
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("parsing assistant token store %s: %w", s.Path(), err)
		}
	}
	recs := make(map[string]Record, len(f.Tokens))
	for _, r := range f.Tokens {
		recs[r.Name] = r
	}
	s.records, s.modTime, s.size, s.loaded = recs, info.ModTime(), info.Size(), true
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	f := storeFile{Version: 1}
	for _, r := range s.records {
		f.Tokens = append(f.Tokens, r)
	}
	sort.Slice(f.Tokens, func(i, j int) bool { return f.Tokens[i].Name < f.Tokens[j].Name })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.Path(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing assistant token store: %w", err)
	}
	// Force the next read to re-stat: two writes inside one mtime tick with
	// the same size must still be seen by other processes, and this process
	// already has the new records in memory.
	if info, err := os.Stat(s.Path()); err == nil {
		s.modTime, s.size = info.ModTime(), info.Size()
	}
	return nil
}

// Create makes a new token for name, replacing (rotating) any existing one.
// It writes the raw token to TokenFile(name) (0600, directory 0700) and
// returns it; only its hash is stored.
func (s *Store) Create(name, assistant string, scopes []Scope, profile string) (string, Record, error) {
	if err := ValidateName(name); err != nil {
		return "", Record{}, err
	}
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	token, err := NewToken()
	if err != nil {
		return "", Record{}, err
	}
	rec := Record{
		Name:        name,
		Assistant:   assistant,
		Scopes:      append([]Scope(nil), scopes...),
		TokenSHA256: HashToken(token),
		Profile:     profile,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return "", Record{}, err
	}
	tokenPath := s.TokenFile(name)
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return "", Record{}, fmt.Errorf("creating token dir: %w", err)
	}
	_ = os.Chmod(filepath.Dir(tokenPath), 0o700)
	if err := atomicfile.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		return "", Record{}, fmt.Errorf("writing token file: %w", err)
	}
	s.records[name] = rec
	if err := s.saveLocked(); err != nil {
		return "", Record{}, err
	}
	return token, rec, nil
}

// Revoke deletes the record for name and its token file. It reports whether
// a record existed. A running server refuses the token on its next call.
func (s *Store) Revoke(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return false, err
	}
	if _, ok := s.records[name]; !ok {
		return false, nil
	}
	delete(s.records, name)
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	if err := os.Remove(s.TokenFile(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, fmt.Errorf("removing token file: %w", err)
	}
	return true, nil
}

// List returns every record, sorted by name.
func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the record for name.
func (s *Store) Get(name string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return Record{}, false, err
	}
	r, ok := s.records[name]
	return r, ok, nil
}

// ErrUnknownToken is returned for a token that is malformed, revoked or was
// never issued.
var ErrUnknownToken = errors.New("unknown or revoked assistant token")

// Authenticate maps a raw token to its record, re-reading the store first
// if it changed on disk.
func (s *Store) Authenticate(token string) (Record, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 256 {
		return Record{}, ErrUnknownToken
	}
	hash := HashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return Record{}, err
	}
	for _, r := range s.records {
		if subtle.ConstantTimeCompare([]byte(r.TokenSHA256), []byte(hash)) == 1 {
			return r, nil
		}
	}
	return Record{}, ErrUnknownToken
}

// ReadTokenFile reads a token written by Create (or by hand), trimming
// whitespace. Files over 4 KiB are refused.
func ReadTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	if info.Size() > 4096 {
		return "", fmt.Errorf("token file %s is larger than 4 KiB", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}
