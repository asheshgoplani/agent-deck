package atomicfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

func TestWriteFile_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.json")

	if err := atomicfile.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("data = %q, want %q", got, "hello")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", fi.Mode().Perm())
	}
}

func TestWriteFile_OverwriteRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("data = %q, want %q", got, "new")
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("regular file became a symlink")
	}
}

func TestWriteFile_PreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(link, []byte("updated"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// The link must still be a symlink.
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was clobbered into a regular file")
	}
	// The real target must hold the new data, reachable through the link.
	viaLink, _ := os.ReadFile(link)
	if string(viaLink) != "updated" {
		t.Fatalf("data via link = %q, want %q", viaLink, "updated")
	}
	direct, _ := os.ReadFile(realPath)
	if string(direct) != "updated" {
		t.Fatalf("data at target = %q, want %q", direct, "updated")
	}
}

func TestWriteFile_PreservesSymlinkChain(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(dir, "mid.json")
	if err := os.Symlink(realPath, mid); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(dir, "top.json")
	if err := os.Symlink(mid, top); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(top, []byte("chain"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, l := range []string{top, mid} {
		fi, err := os.Lstat(l)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is no longer a symlink", l)
		}
	}
	got, _ := os.ReadFile(realPath)
	if string(got) != "chain" {
		t.Fatalf("data = %q, want %q", got, "chain")
	}
}

func TestWriteFile_DanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "absent.json")
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(link, []byte("revived"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling symlink was replaced by a regular file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target not created: %v", err)
	}
	if string(got) != "revived" {
		t.Fatalf("data = %q, want %q", got, "revived")
	}
}

func TestWriteFile_MultiHopDanglingChain(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "leaf.json") // missing target
	mid := filepath.Join(dir, "mid.json")
	top := filepath.Join(dir, "top.json")
	if err := os.Symlink(leaf, mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mid, top); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(top, []byte("multi"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Both intermediate links must survive — the write must land on the leaf.
	for _, l := range []string{top, mid} {
		fi, err := os.Lstat(l)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was clobbered into a regular file", l)
		}
	}
	got, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatalf("leaf not created: %v", err)
	}
	if string(got) != "multi" {
		t.Fatalf("data = %q, want %q", got, "multi")
	}
}

func TestWriteFile_SymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(a, []byte("x"), 0o600); err == nil {
		t.Fatal("expected an error writing through a symlink loop, got nil")
	}

	// Neither link may be clobbered into a regular file.
	for _, l := range []string{a, b} {
		fi, err := os.Lstat(l)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was clobbered into a regular file", l)
		}
	}
}

func TestWriteFile_NoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")

	if err := atomicfile.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".atomicfile-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
