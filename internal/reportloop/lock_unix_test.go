//go:build unix

package reportloop

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Re-review N4: the lock file is opened read-write (flock on some file
// systems needs it), and a lock failure on Load is best effort.

func TestReportLoopLockOpensReadWrite(t *testing.T) {
	if _, err := os.Stat("/proc/self/fdinfo"); err != nil {
		t.Skip("needs /proc/self/fdinfo to read the open flags")
	}
	p := filepath.Join(t.TempDir(), "reports.json.lock")
	release, err := lockFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode: %v %v", fi, err)
	}
	// Find the descriptor lockFile holds on p and read its open flags.
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if target, err := os.Readlink("/proc/self/fd/" + e.Name()); err != nil || target != p {
			continue
		}
		info, err := os.ReadFile("/proc/self/fdinfo/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(info), "\n") {
			if v, ok := strings.CutPrefix(line, "flags:"); ok {
				flags, err := strconv.ParseInt(strings.TrimSpace(v), 8, 64)
				if err != nil {
					t.Fatal(err)
				}
				if flags&int64(os.O_RDWR) == 0 {
					t.Fatalf("lock file opened with flags %o, want O_RDWR", flags)
				}
				return
			}
		}
	}
	t.Fatal("no open descriptor on the lock file")
}

func TestReportLoopLoadWithoutLockIsBestEffort(t *testing.T) {
	s := tempStore(t)
	if err := s.Record(Report{Number: 90, Title: "[bug] x", FiledVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	// The lock path is a symlink: O_NOFOLLOW refuses it.
	lock := s.Path + ".lock"
	_ = os.Rename(lock, lock+".moved") // move the real lock file aside
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), lock); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil || st.Find(90) == nil {
		t.Fatalf("Load must read without the lock when locking fails: %v %+v", err, st)
	}
	if err := s.Record(Report{Number: 91, Title: "[bug] y", FiledVersion: "1.0.0"}); err == nil {
		t.Fatal("a change must still refuse without the lock")
	}
}

// CodeQL go/unhandled-writable-file-close: the lock file is opened
// writable, so its Close error is returned, never dropped.
func TestReportLoopLockReleaseReturnsCloseError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reports.json.lock")
	release, err := lockFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := release(); err == nil {
		t.Fatalf("a second release closes a closed file: its error must be returned")
	}
}
