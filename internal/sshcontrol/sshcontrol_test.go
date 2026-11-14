package sshcontrol

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func skipNoPOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ownership and modes only")
	}
}

func newTestResolver(t *testing.T) (*resolver, string) {
	t.Helper()
	parent := t.TempDir()
	r := &resolver{parent: parent, uid: os.Getuid(), owner: ownerUID}
	return r, filepath.Join(parent, dirName(os.Getuid()))
}

func assertPrivateDir(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat %s: %v", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is not a real directory: %v", dir, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("%s mode = %v, want 0700", dir, perm)
	}
	if owner, ok := ownerUID(info); ok && owner != os.Getuid() {
		t.Fatalf("%s owned by %d, want %d", dir, owner, os.Getuid())
	}
}

func TestEnsure_CreatesPerUserPrivateDir(t *testing.T) {
	skipNoPOSIX(t)
	r, want := newTestResolver(t)
	got, err := r.ensure()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ensure() = %q, want %q", got, want)
	}
	if filepath.Base(got) != "agent-deck-ssh-"+strconv.Itoa(os.Getuid()) {
		t.Fatalf("dir name %q is not per-user", filepath.Base(got))
	}
	assertPrivateDir(t, got)
	// Removed between calls: recreated, still the same path.
	if err := os.Remove(got); err != nil {
		t.Fatal(err)
	}
	if again, err := r.ensure(); err != nil || again != want {
		t.Fatalf("ensure() after removal = %q, %v", again, err)
	}
	assertPrivateDir(t, want)
}

func TestEnsure_TightensLooseModeOfOwnDir(t *testing.T) {
	skipNoPOSIX(t)
	r, want := newTestResolver(t)
	if err := os.Mkdir(want, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(want, 0o777); err != nil {
		t.Fatal(err)
	}
	got, err := r.ensure()
	if err != nil || got != want {
		t.Fatalf("ensure() = %q, %v; want %q", got, err, want)
	}
	assertPrivateDir(t, got)
}

func assertFallback(t *testing.T, r *resolver, unsafe, got string) {
	t.Helper()
	if got == unsafe {
		t.Fatalf("ensure() used the unsafe directory %q", unsafe)
	}
	if filepath.Dir(got) != r.parent || !strings.HasPrefix(filepath.Base(got), dirName(r.uid)+"-") {
		t.Fatalf("fallback %q is not a private sibling under %q", got, r.parent)
	}
	assertPrivateDir(t, got)
	again, err := r.ensure()
	if err != nil || again != got {
		t.Fatalf("second ensure() = %q, %v; want the same fallback %q", again, err, got)
	}
}

func TestEnsure_SymlinkIsNeverUsed(t *testing.T) {
	skipNoPOSIX(t)
	r, unsafe := newTestResolver(t)
	target := filepath.Join(r.parent, "attacker")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, unsafe); err != nil {
		t.Fatal(err)
	}
	got, err := r.ensure()
	if err != nil {
		t.Fatal(err)
	}
	assertFallback(t, r, unsafe, got)
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target was modified: %v %v", info.Mode(), err)
	}
}

func TestEnsure_ForeignOwnedDirIsNeverUsed(t *testing.T) {
	skipNoPOSIX(t)
	r, unsafe := newTestResolver(t)
	if err := os.Mkdir(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	// An unprivileged test cannot chown, so report the preferred directory as
	// another user's while every other file keeps its real owner.
	r.owner = func(info os.FileInfo) (int, bool) {
		if info.Name() == filepath.Base(unsafe) {
			return r.uid + 1, true
		}
		return ownerUID(info)
	}
	got, err := r.ensure()
	if err != nil {
		t.Fatal(err)
	}
	assertFallback(t, r, unsafe, got)
	if info, _ := os.Lstat(unsafe); info.Mode().Perm() != 0o777 {
		t.Fatalf("foreign directory was chmodded to %v", info.Mode().Perm())
	}
}

func TestEnsure_RegularFileIsNeverUsed(t *testing.T) {
	skipNoPOSIX(t)
	r, unsafe := newTestResolver(t)
	if err := os.WriteFile(unsafe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := r.ensure()
	if err != nil {
		t.Fatal(err)
	}
	assertFallback(t, r, unsafe, got)
}

func TestControlPath_ShortPerUserUnderTmp(t *testing.T) {
	skipNoPOSIX(t)
	want := "/tmp/agent-deck-ssh-" + strconv.Itoa(os.Getuid())
	if got := PreferredDir(); got != want {
		t.Fatalf("PreferredDir() = %q, want %q", got, want)
	}
	cp := ControlPath()
	if !strings.HasSuffix(cp, "/%C") || !strings.HasPrefix(cp, "/tmp/agent-deck-ssh-"+strconv.Itoa(os.Getuid())) {
		t.Fatalf("ControlPath() = %q", cp)
	}
	// %C expands to 40 hex characters and ssh binds "<path>.<16 chars>"
	// first; that must fit macOS sun_path (104 bytes incl. the NUL).
	if n := len(strings.TrimSuffix(cp, "%C")) + 40 + sshTempSuffixLen; n >= sunPathMax {
		t.Fatalf("ControlPath() %q expands to %d bytes, too long for unix sockets", cp, n)
	}
}
