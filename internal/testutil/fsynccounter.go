package testutil

import (
	"os"
	"sync"
)

// FsyncCounter is the reusable Tier 2 instrumentation the perf-budget doc
// anticipates (docs/perf-budget-suite.md, "real-disk count assertions"):
//
//	"Disk walltime varies 100× across SSDs / HDDs / cloud volumes —
//	 un-normalizable. Instead instrument the storage layer (*sql.DB and
//	 *os.File wrappers exposing fsync count, transaction count, rows written)
//	 and assert on counts: 'create one group = exactly 1 transaction + 1 fsync'."
//
// It wraps a `func(*os.File) error` fsync seam (e.g. the one a package routes
// (*os.File).Sync through) and tallies the calls, bucketed by whether the
// descriptor is a regular file or a directory. The two carry different
// regression signals: a durable atomic write fsyncs the DATA file (content
// durability) and then best-effort fsyncs the PARENT directory (rename
// durability), so a test that asserts on both pins the exact I/O shape.
//
// Today only the durable per-parent outbox drain wires it (one caller), but it
// is package-level testutil rather than a one-off in that test because the doc
// frames the wrapper as shared infrastructure every future persistence-touching
// Tier 2 gate should reuse — a single counting convention beats a bespoke
// counter per save path.
//
// Construct with new(FsyncCounter); install via the seam's test hook:
//
//	fc := new(testutil.FsyncCounter)
//	restore := session.SetFsyncHookForTest(fc.Wrap(nil)) // nil → real Sync
//	defer restore()
//	... exercise the code under test ...
//	fc.Reset()                 // discard setup syncs
//	... run the timed/counted op ...
//	if fc.FileSyncs() != 2 { ... }
//
// All methods are safe for concurrent use; the seam may fire from background
// goroutines in the code under test.
type FsyncCounter struct {
	mu        sync.Mutex
	fileSyncs int
	dirSyncs  int
}

// Wrap returns a fsync-seam function that records each call then delegates to
// real. Pass real=nil to delegate to (*os.File).Sync (the production default),
// which is the common case — the count is the assertion, the underlying fsync
// is left intact (and is cheap on the tmpfs scratch dir a Tier 2 test uses).
func (c *FsyncCounter) Wrap(real func(*os.File) error) func(*os.File) error {
	if real == nil {
		real = (*os.File).Sync
	}
	return func(f *os.File) error {
		// Bucket BEFORE delegating so a fsync that returns an error (e.g. a
		// directory fsync rejected with EINVAL/ENOTSUP on some filesystems —
		// which the durable-write path treats as benign) is still counted: the
		// regression signal is "was the syscall issued", not "did it succeed".
		if info, err := f.Stat(); err == nil && info.IsDir() {
			c.mu.Lock()
			c.dirSyncs++
			c.mu.Unlock()
		} else {
			c.mu.Lock()
			c.fileSyncs++
			c.mu.Unlock()
		}
		return real(f)
	}
}

// FileSyncs returns the number of fsyncs issued against regular files.
func (c *FsyncCounter) FileSyncs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fileSyncs
}

// DirSyncs returns the number of fsyncs issued against directories.
func (c *FsyncCounter) DirSyncs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirSyncs
}

// Reset zeroes both counters. Call it after fixture setup (whose own durable
// writes are not part of the operation under test) and before the counted op.
func (c *FsyncCounter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fileSyncs = 0
	c.dirSyncs = 0
}
