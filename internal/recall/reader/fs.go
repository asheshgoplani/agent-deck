package reader

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
)

// Every filesystem call a reader makes goes through these wrappers so a
// test can count them: the sweep's cost is "one readdir per directory, one
// lstat per file, one open per changed file", and a regression that turns
// it into per-root or per-symlink work must fail a test, not a laptop.

// FSCounts is a snapshot of the wrapper counters.
type FSCounts struct {
	ReadDir      int64
	Lstat        int64
	Stat         int64
	Open         int64
	EvalSymlinks int64
}

var fsCounts struct {
	readDir, lstat, stat, open, eval atomic.Int64
}

// FSCallCounts returns the running totals since process start.
func FSCallCounts() FSCounts {
	return FSCounts{
		ReadDir:      fsCounts.readDir.Load(),
		Lstat:        fsCounts.lstat.Load(),
		Stat:         fsCounts.stat.Load(),
		Open:         fsCounts.open.Load(),
		EvalSymlinks: fsCounts.eval.Load(),
	}
}

// Sub returns c minus o.
func (c FSCounts) Sub(o FSCounts) FSCounts {
	return FSCounts{
		ReadDir:      c.ReadDir - o.ReadDir,
		Lstat:        c.Lstat - o.Lstat,
		Stat:         c.Stat - o.Stat,
		Open:         c.Open - o.Open,
		EvalSymlinks: c.EvalSymlinks - o.EvalSymlinks,
	}
}

// Total is every counted call.
func (c FSCounts) Total() int64 {
	return c.ReadDir + c.Lstat + c.Stat + c.Open + c.EvalSymlinks
}

func fsReadDir(path string) ([]fs.DirEntry, error) {
	fsCounts.readDir.Add(1)
	return os.ReadDir(path)
}

func fsStat(path string) (fs.FileInfo, error) {
	fsCounts.stat.Add(1)
	return os.Stat(path)
}

func fsOpen(path string) (*os.File, error) {
	fsCounts.open.Add(1)
	return os.Open(path)
}

func fsEvalSymlinks(path string) (string, error) {
	fsCounts.eval.Add(1)
	return filepath.EvalSymlinks(path)
}

// entryInfo is DirEntry.Info(), which on the walked trees is one lstat.
func entryInfo(d fs.DirEntry) (fs.FileInfo, error) {
	fsCounts.lstat.Add(1)
	return d.Info()
}

// FileIdentity extracts (dev, ino) from a FileInfo; zero when the platform
// does not expose them.
func FileIdentity(info fs.FileInfo) (dev, ino uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}
