package procowner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
)

// ErrGenerationConflict is returned when a write would overwrite a receipt from
// a newer spawn, or from a different spawn of the same generation. It is how
// two racing restarts are kept from interleaving: the loser's write is refused,
// not silently applied on top of the winner's.
var ErrGenerationConflict = errors.New("ownership receipt was superseded by a newer generation")

// ErrReceiptGone is returned when an update finds no receipt to update.
//
// This is the resurrection guard. An attribution pass that started before a
// teardown can still be in flight when the receipt is cleared; if that pass
// were allowed to WRITE, a receipt that was verifiably retired would come back
// from the dead — and everything downstream trusts a receipt. An update is
// therefore never a create.
var ErrReceiptGone = errors.New("ownership receipt was cleared while this update was in flight")

// ErrWindowClosed is returned when a caller's own cancellation check fires
// under the store lock, so a cancelled writer's in-flight write is dropped
// rather than applied.
var ErrWindowClosed = errors.New("ownership update was cancelled before it could be applied")

// ErrNoChange lets an update's mutator say "nothing to write" without that
// being an error at the call site.
var ErrNoChange = errors.New("ownership receipt unchanged")

// ErrBadInstanceID rejects an instance id that cannot be a single path element.
var ErrBadInstanceID = errors.New("invalid instance id for an ownership receipt")

// LockFunc takes exclusive, cross-process access to a receipt path and returns
// the release function.
//
// It is a seam rather than an implementation because the lock this package
// needs already exists in the tree — one implementation of that rule, shared —
// and internal/procowner cannot import the package that holds it without a
// cycle. The session layer wires the real one in; see ownershipStoreAt.
type LockFunc func(path string) (release func(), err error)

// NoCrossProcessLock is an explicit opt-out, for single-process tests that
// document why they do not need cross-process serialization. It is a named,
// greppable function rather than a nil default: a store that silently skips its
// lock is the failure this whole file exists to prevent.
func NoCrossProcessLock(string) (func(), error) { return func() {}, nil }

// Store persists receipts as one file per instance.
//
// A file, not a row: the receipt has to be readable and writable by a different
// agent-deck process from the one that wrote it (a CLI restart reconciling what
// a TUI spawned), and it has to survive a crash mid-write.
//
// Every public method takes the lock for the WHOLE load → check → write cycle.
// Atomic replacement alone is not enough and never was: two writers that each
// read, decide and rename produce a last-writer-wins result in which the
// loser's decision was made on state the winner had already invalidated. The
// rename being atomic only makes the wrong answer durable.
type Store struct {
	dir  string
	lock LockFunc
}

// NewStore returns a store rooted at dir, serialized by lock.
func NewStore(dir string, lock LockFunc) *Store {
	if lock == nil {
		panic("procowner: NewStore needs a lock; pass NoCrossProcessLock to opt out")
	}
	return &Store{dir: dir, lock: lock}
}

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

// withLock runs fn holding exclusive access to the instance's receipt path.
//
// The directory is created before locking because the lock file is a sibling of
// the receipt: there is nowhere to put it otherwise.
func (s *Store) withLock(instanceID string, fn func(path string) error) error {
	path, err := s.Path(instanceID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create ownership dir: %w", err)
	}
	release, err := s.lock(path)
	if err != nil {
		return fmt.Errorf("lock ownership receipt: %w", err)
	}
	defer release()
	return fn(path)
}

// Load returns the receipt for an instance, (nil, nil) when there is none, or
// ErrCorruptReceipt when one exists but cannot be trusted.
//
// The corrupt case is deliberately NOT folded into "no receipt". A truncated
// receipt is a session whose ownership we recorded and can no longer read;
// treating that as "nothing was owned" is how a duplicate tree gets spawned.
//
// The read takes the lock so it cannot observe a receipt that a concurrent
// commit is midway through deciding on.
func (s *Store) Load(instanceID string) (*Receipt, error) {
	var (
		receipt *Receipt
		loadErr error
	)
	err := s.withLock(instanceID, func(path string) error {
		receipt, loadErr = loadFrom(path, instanceID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipt, loadErr
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

// Commit performs one atomic read-modify-write of an instance's receipt.
//
// build receives whatever is on disk (nil when there is none, and nil when what
// is there is unreadable — a receipt that cannot be parsed cannot claim a
// generation) and returns the receipt to write. Returning an error aborts with
// the file untouched, which is how a caller declines to claim: no receipt is
// always safer than a receipt it could not substantiate.
//
// This is the ONLY way a new receipt reaches disk. Selecting a generation and
// writing it are one critical section, so two spawns cannot both read the same
// predecessor and both decide they are the successor.
func (s *Store) Commit(instanceID string, build func(existing *Receipt) (*Receipt, error)) (*Receipt, error) {
	var committed *Receipt
	err := s.withLock(instanceID, func(path string) error {
		existing, loadErr := loadFrom(path, instanceID)
		if loadErr != nil && !errors.Is(loadErr, ErrCorruptReceipt) {
			return loadErr
		}
		next, buildErr := build(existing)
		if buildErr != nil {
			return buildErr
		}
		if next == nil {
			return errors.New("procowner: commit produced no receipt")
		}
		if next.InstanceID != instanceID {
			return fmt.Errorf("procowner: commit for %q produced a receipt for %q", instanceID, next.InstanceID)
		}
		if err := checkSupersession(existing, next); err != nil {
			return err
		}
		if err := writeReceipt(path, next); err != nil {
			return err
		}
		committed = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

// Update applies mutate to the receipt on disk, and never creates one.
//
// mutate receives the CURRENT receipt — loaded inside the lock, never nil — and
// edits it in place. Returning an error aborts with the file untouched;
// returning ErrNoChange aborts without reporting one.
//
// Handing the mutator the on-disk receipt rather than taking a prepared one is
// the difference between a compare-and-swap and a hope. A caller that loads,
// edits its own copy and writes it back has a gap between the load and the
// write in which another process's edit can land — and its write then erases
// that edit. Making the read, the decision AND the write one critical section
// is the only shape that does not lose updates; atomic replacement makes the
// write indivisible, never the sequence around it.
//
// This is also where cancellation becomes authoritative rather than advisory: a
// mutator that checks its own window here is checking it after the lock has
// been won, so a teardown that lands while this call was queued disarms the
// write instead of merely stopping the next attempt.
func (s *Store) Update(instanceID string, mutate func(current *Receipt) error) error {
	if mutate == nil {
		return errors.New("procowner: update needs a mutator")
	}
	return s.withLock(instanceID, func(path string) error {
		existing, loadErr := loadFrom(path, instanceID)
		if loadErr != nil {
			// Unreadable: refuse rather than overwrite. The operator's recovery
			// path (abandon) is an explicit act, not a side effect of an
			// attribution pass.
			return loadErr
		}
		if existing == nil {
			return fmt.Errorf("%w: instance %s", ErrReceiptGone, instanceID)
		}
		if err := mutate(existing); err != nil {
			if errors.Is(err, ErrNoChange) {
				return nil
			}
			return err
		}
		if err := existing.Validate(); err != nil {
			return err
		}
		return writeReceipt(path, existing)
	})
}

// RequireGeneration is the guard an update's mutator uses to assert that the
// receipt on disk is still the one it belongs to. It is the same rule Commit
// and Clear enforce, called rather than restated.
func RequireGeneration(current *Receipt, generation uint64, leader Member) error {
	if current.Generation != generation || current.Leader.Key() != leader.Key() {
		return fmt.Errorf("%w: on disk generation %d owned by %s, updating generation %d owned by %s",
			ErrGenerationConflict, current.Generation, current.Leader, generation, leader)
	}
	return nil
}

// Clear removes a receipt the caller has verified as retired.
//
// It takes the receipt rather than an id so the same two checks that guard
// every write guard the delete: a newer generation is never removed by an older
// caller, and a receipt belonging to a different leader is never removed by a
// stale one. A late clear from a superseded spawn must not delete the live
// receipt of the spawn that replaced it.
func (s *Store) Clear(r *Receipt) error {
	if r == nil {
		return errors.New("procowner: clear needs the receipt it is retiring")
	}
	return s.withLock(r.InstanceID, func(path string) error {
		existing, loadErr := loadFrom(path, r.InstanceID)
		if loadErr != nil {
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
	})
}

// ForceClear removes the receipt regardless of generation. Reserved for the
// explicit operator abandon path, which is the only place where discarding an
// unverifiable claim is a decision a human has made.
func (s *Store) ForceClear(instanceID string) error {
	return s.withLock(instanceID, func(path string) error {
		return safeio.SafeRemove(path, safeio.RemoveOptions{})
	})
}

// checkSupersession refuses a write that a concurrent spawn has already made
// stale.
//
// Generations restart at 1 once a receipt has been verifiably cleared (a
// cleared receipt is an absent file, by design — a tombstone would be a second
// claim of ownership sitting next to the live one), so the number alone cannot
// always tell a stale writer from the current one. The leader identity can, and
// it is already the thing that defines the receipt.
func checkSupersession(existing, next *Receipt) error {
	if existing == nil {
		return nil
	}
	if existing.Generation > next.Generation {
		return fmt.Errorf("%w: on disk %d, writing %d",
			ErrGenerationConflict, existing.Generation, next.Generation)
	}
	if existing.Generation == next.Generation && existing.Leader.Key() != next.Leader.Key() {
		return fmt.Errorf("%w: generation %d on disk is owned by %s, not %s",
			ErrGenerationConflict, existing.Generation, existing.Leader, next.Leader)
	}
	return nil
}

func writeReceipt(path string, r *Receipt) error {
	data, err := Encode(r)
	if err != nil {
		return err
	}
	// SkipBackup: a stale .bak receipt is worse than no receipt — it would be a
	// second, older claim of ownership sitting next to the live one.
	return safeio.SafeOverwrite(path, data, safeio.Options{Perm: 0o600, SkipBackup: true})
}
