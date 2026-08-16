package procowner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
)

// ErrGenerationConflict is returned when a write would overwrite a receipt from
// a newer spawn. It is how two racing restarts are kept from interleaving: the
// loser's write is refused, not silently applied on top of the winner's.
var ErrGenerationConflict = errors.New("ownership receipt was superseded by a newer generation")

// ErrBadInstanceID rejects an instance id that cannot be a single path element.
var ErrBadInstanceID = errors.New("invalid instance id for an ownership receipt")

// Store persists receipts as one file per instance.
//
// A file, not a row: the receipt has to be readable and writable by a different
// agent-deck process from the one that wrote it (a CLI restart reconciling what
// a TUI spawned), and it has to survive a crash mid-write. safeio's temp →
// fsync → rename gives that without a schema migration or a new dependency.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.dir }

// Path returns the receipt path for an instance.
func (s *Store) Path(instanceID string) (string, error) {
	id := strings.TrimSpace(instanceID)
	// The id becomes a filename, so anything that could climb out of the
	// directory is refused rather than sanitised into something that still
	// resolves somewhere unexpected.
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("%w: %q", ErrBadInstanceID, instanceID)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Load returns the receipt for an instance, (nil, nil) when there is none, or
// ErrCorruptReceipt when one exists but cannot be trusted.
//
// The corrupt case is deliberately NOT folded into "no receipt". A truncated
// receipt is a session whose ownership we recorded and can no longer read;
// treating that as "nothing was owned" is how a duplicate tree gets spawned.
func (s *Store) Load(instanceID string) (*Receipt, error) {
	path, err := s.Path(instanceID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadFrom(path, instanceID)
}

func loadFrom(path, instanceID string) (*Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: read %s: %v", ErrCorruptReceipt, path, err)
	}
	r, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if r.InstanceID != instanceID {
		return nil, fmt.Errorf("%w: receipt at %s belongs to instance %q, not %q",
			ErrCorruptReceipt, path, r.InstanceID, instanceID)
	}
	return r, nil
}

// Save writes a receipt, refusing to overwrite a newer generation.
//
// Equal generations are allowed to rewrite: that is how the attribution pass
// adds members to the receipt it just committed. A lower generation is refused
// — that writer belongs to a superseded spawn.
func (s *Store) Save(r *Receipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	path, err := s.Path(r.InstanceID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, loadErr := loadFrom(path, r.InstanceID)
	switch {
	case loadErr != nil && !errors.Is(loadErr, ErrCorruptReceipt):
		return loadErr
	case loadErr != nil:
		// A corrupt receipt on disk cannot report a generation, so it cannot
		// claim to be newer. Replacing it with a valid one is strictly an
		// improvement — and the spawn lock means only one writer is here.
	case existing != nil && existing.Generation > r.Generation:
		return fmt.Errorf("%w: on disk %d, writing %d",
			ErrGenerationConflict, existing.Generation, r.Generation)
	case existing != nil && existing.Generation == r.Generation &&
		existing.Leader.Key() != r.Leader.Key():
		// Same generation, different leader: two different spawns. Generations
		// restart at 1 once a receipt has been verifiably cleared (a cleared
		// receipt is an absent file, by design — a tombstone would be a second
		// claim of ownership sitting next to the live one), so the number alone
		// cannot tell a stale writer from the current one. The leader identity
		// can, and it is already the thing that defines the receipt. This is
		// what stops an attribution pass from a reaped generation, still alive
		// in another process, from overwriting the receipt of the spawn that
		// replaced it.
		return fmt.Errorf("%w: generation %d on disk is owned by %s, not %s",
			ErrGenerationConflict, existing.Generation, existing.Leader, r.Leader)
	}

	data, err := Encode(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create ownership dir: %w", err)
	}
	// SkipBackup: a stale .bak receipt is worse than no receipt — it would be a
	// second, older claim of ownership sitting next to the live one.
	return safeio.SafeOverwrite(path, data, safeio.Options{Perm: 0o600, SkipBackup: true})
}

// NextGeneration returns the generation a new spawn should commit under: one
// more than whatever is on disk. A corrupt receipt is counted as generation 0
// so the fresh receipt still supersedes it.
func (s *Store) NextGeneration(instanceID string) (uint64, error) {
	existing, err := s.Load(instanceID)
	if err != nil && !errors.Is(err, ErrCorruptReceipt) {
		return 0, err
	}
	if existing == nil {
		return 1, nil
	}
	return existing.Generation + 1, nil
}

// Clear removes a receipt the caller has verified as retired.
//
// It takes the receipt rather than an id so the same two checks that guard Save
// guard the delete: a newer generation is never removed by an older caller, and
// a receipt belonging to a different leader is never removed by a stale one. A
// late clear from a superseded spawn must not delete the live receipt of the
// spawn that replaced it.
func (s *Store) Clear(r *Receipt) error {
	if r == nil {
		return errors.New("procowner: clear needs the receipt it is retiring")
	}
	path, err := s.Path(r.InstanceID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, loadErr := loadFrom(path, r.InstanceID)
	if loadErr != nil && !errors.Is(loadErr, ErrCorruptReceipt) {
		return loadErr
	}
	if existing != nil {
		if existing.Generation > r.Generation {
			return fmt.Errorf("%w: on disk %d, clearing %d",
				ErrGenerationConflict, existing.Generation, r.Generation)
		}
		if existing.Generation == r.Generation && existing.Leader.Key() != r.Leader.Key() {
			return fmt.Errorf("%w: generation %d on disk is owned by %s, not %s",
				ErrGenerationConflict, existing.Generation, existing.Leader, r.Leader)
		}
	}
	return safeio.SafeRemove(path, safeio.RemoveOptions{})
}

// ForceClear removes the receipt regardless of generation. Reserved for the
// explicit operator abandon path, which is the only place where discarding an
// unverifiable claim is a decision a human has made.
func (s *Store) ForceClear(instanceID string) error {
	path, err := s.Path(instanceID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return safeio.SafeRemove(path, safeio.RemoveOptions{})
}
