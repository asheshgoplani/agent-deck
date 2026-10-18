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

func TestWriteFile_RelativeDanglingChain(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "leaf.json") // missing target
	mid := filepath.Join(dir, "mid.json")
	top := filepath.Join(dir, "top.json")
	// Relative link values, resolved against each link's own directory — the
	// common dotfiles-manager shape (e.g. GNU Stow).
	if err := os.Symlink("leaf.json", mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mid.json", top); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(top, []byte("relative"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

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
	if string(got) != "relative" {
		t.Fatalf("data = %q, want %q", got, "relative")
	}
}

func TestWriteFileDurable_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.toml")

	if err := atomicfile.WriteFileDurable(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFileDurable: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("data = %q, want %q", got, "hello")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", fi.Mode().Perm())
	}
}

func TestWriteFileDurable_PreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(realPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFileDurable(link, []byte("durable"), 0o600); err != nil {
		t.Fatalf("WriteFileDurable: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was clobbered into a regular file")
	}
	direct, _ := os.ReadFile(realPath)
	if string(direct) != "durable" {
		t.Fatalf("data at target = %q, want %q", direct, "durable")
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

func TestWriteFileWithin_RefusesLinkOutsideRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	outsideDir := filepath.Join(tmp, "elsewhere")
	for _, d := range []string{root, outsideDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(tmp, "victim.json")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "file.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "missing.json"), filepath.Join(root, "dangling.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"file.json", "dangling.json", filepath.Join("linkdir", "new.json"), filepath.Join("linkdir", "sub", "new.json")} {
		if err := atomicfile.WriteFileWithin(root, filepath.Join(root, rel), []byte("x"), 0o600); err == nil {
			t.Errorf("%s: expected refusal for a path that resolves outside the root", rel)
		}
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Fatalf("victim rewritten: %q", got)
	}
	for _, p := range []string{filepath.Join(tmp, "missing.json"), filepath.Join(outsideDir, "new.json"), filepath.Join(outsideDir, "sub")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s created outside the root (err %v)", p, err)
		}
	}
}

func TestWriteFileWithin_AllowsPathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "f.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "f.json")
	if err := os.Symlink(filepath.Join("real", "f.json"), link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFileWithin(root, link, []byte("new"), 0o600); err != nil {
		t.Fatalf("in-root link: %v", err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link not preserved (err %v)", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "real", "f.json")); string(got) != "new" {
		t.Fatalf("link target = %q, want new", got)
	}
	nested := filepath.Join(root, "a", "b", "c.json")
	if err := atomicfile.WriteFileWithin(root, nested, []byte("n"), 0o600); err != nil {
		t.Fatalf("new nested file: %v", err)
	}
}
