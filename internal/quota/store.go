package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// DefaultStaleAfter is how long a cached snapshot is presented without a stale
// marker.
//
// The shortest window a provider reports is five hours, and its percentage
// therefore moves on a scale of tens of minutes even under a fleet of agents.
// Fifteen minutes is short enough that a number the user acts on is still
// roughly current, and long enough that the common `agent-deck usage` in a
// terminal loop does not turn into a request per invocation.
//
// KNOWN LIMITATION, accepted deliberately: this is a freshness marker, not a
// refresh. Claude's provider is PUSH-only — its snapshot is written by whatever
// statusLine invocation last ran — so a Claude snapshot goes stale whenever the
// user simply is not running Claude, and no amount of asking will refresh it.
// That is the honest state and it is shown as such rather than hidden, because
// the alternative (dropping the last known numbers) leaves the user with less
// than they had.
const DefaultStaleAfter = 15 * time.Minute

// maxProviderIDLen bounds the provider id, which becomes a filename. Provider
// ids are compile-time constants in this package, so this guards against a
// future caller rather than against today's data.
const maxProviderIDLen = 64

// Store is the on-disk quota cache for one profile.
//
// ONE FILE PER PROVIDER, deliberately. Claude's snapshot is PUSHED by the
// statusLine ingester — potentially by several concurrent Claude sessions —
// while Z.ai's is PULLED by `agent-deck usage`. Separate files mean those
// writers never read-modify-write the same file, so there is no lock to hold
// and no lost update to reason about. Load simply reads the directory.
//
// The per-profile subdirectory keeps a future multi-account story open without
// a schema change, and matches how the rest of agent-deck keys on-disk state.
type Store struct {
	dir string
	// StaleAfter is per-store so a caller with a different tolerance (a status
	// bar refreshing on a tick, say) can set its own without a global.
	StaleAfter time.Duration
}

// NewStore returns the quota cache for a profile. It does not create anything
// on disk: `agent-deck usage --help` must not leave a directory behind, which
// the repo's help-is-read-only test enforces for every registered command.
func NewStore(profile string) (*Store, error) {
	cacheDir, err := agentpaths.CacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolving cache dir: %w", err)
	}
	cleanProfile := strings.TrimSpace(profile)
	if cleanProfile == "" || !validProviderID(cleanProfile) {
		return nil, fmt.Errorf("unusable profile name %q for quota cache", profile)
	}
	return &Store{dir: filepath.Join(cacheDir, "quota", cleanProfile), StaleAfter: DefaultStaleAfter}, nil
}

// Dir is the directory holding this profile's provider files.
func (s *Store) Dir() string { return s.dir }

// Save writes one provider's snapshot.
//
// Stale is cleared before writing: it is a read-time computation, and
// persisting it would let a snapshot that was fresh at write time claim to be
// fresh forever.
func (s *Store) Save(snapshot Snapshot) error {
	if !validProviderID(snapshot.ID) {
		return fmt.Errorf("unusable provider id %q", snapshot.ID)
	}
	snapshot.Stale = false

	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s snapshot: %w", snapshot.ID, err)
	}
	// 0o700: the numbers are not secret, but the set of providers a user has
	// configured is theirs alone, and a private directory costs nothing.
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("creating quota cache dir: %w", err)
	}
	// internal/atomicfile documents itself as being for user-managed config
	// files, in contrast with the unexported temp+rename helpers agent-deck
	// uses for its own state. It is used here anyway: those helpers are
	// unexported, this one is stdlib-only so it introduces no import cycle, and
	// its temp+rename semantics for a regular file are identical. The only
	// difference — it preserves a symlink AT the path — is immaterial for a
	// file that only agent-deck writes.
	//
	// 0o644: the file holds percentages and reset timestamps, never a token.
	// The store round-trip test asserts that against a sentinel credential.
	path := filepath.Join(s.dir, snapshot.ID+".json")
	if err := atomicfile.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("writing %s snapshot: %w", snapshot.ID, err)
	}
	return nil
}

// Load returns every cached provider snapshot, ordered by provider id so the
// rendering does not reshuffle between invocations.
//
// A missing cache directory is the normal state before anything has been
// ingested or fetched, and yields an empty report rather than an error.
func (s *Store) Load() ([]Snapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading quota cache: %w", err)
	}

	staleAfter := s.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	now := time.Now()

	var snapshots []Snapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		snapshots = append(snapshots, s.loadOne(id, now, staleAfter))
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ID < snapshots[j].ID })
	return snapshots, nil
}

// loadOne turns one cache file into a snapshot. An unreadable or corrupt file
// becomes a snapshot carrying the failure rather than disappearing: a provider
// the user configured going silently missing from the output is the one
// outcome that would send them to a vendor dashboard, which is the thing this
// feature exists to avoid.
func (s *Store) loadOne(id string, now time.Time, staleAfter time.Duration) Snapshot {
	raw, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return Snapshot{ID: id, Label: ProviderLabel(id), Error: "cached snapshot unreadable", Stale: true}
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{ID: id, Label: ProviderLabel(id), Error: "cached snapshot is corrupt", Stale: true}
	}
	// The filename is authoritative for the id: it is what Load enumerates and
	// what a second writer would collide on.
	snapshot.ID = id
	if snapshot.Label == "" {
		snapshot.Label = ProviderLabel(id)
	}
	snapshot.Stale = snapshot.UpdatedAt <= 0 || now.Sub(time.Unix(snapshot.UpdatedAt, 0)) > staleAfter
	return snapshot
}

// ProviderLabel is the display name for a provider id, falling back to the id
// itself for a cache file this build does not know about — a downgrade should
// show the entry, not hide it.
func ProviderLabel(id string) string {
	switch id {
	case ProviderClaude:
		return claudeLabel
	case ProviderZai:
		return "Z.ai"
	default:
		return id
	}
}

// validProviderID keeps the id usable as a single filename component. The id
// reaches filepath.Join, so a traversal component would write outside the
// cache directory.
func validProviderID(id string) bool {
	if id == "" || len(id) > maxProviderIDLen {
		return false
	}
	if id != filepath.Base(id) || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
