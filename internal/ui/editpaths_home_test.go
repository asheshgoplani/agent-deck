package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTempDir returns t.TempDir() with symlinks resolved (macOS /var -> /private/var),
// because Show resolves symlinks before collapsing HOME to "~".
func realTempDir(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return tmp
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Opening Edit Paths on a session whose repos live under HOME and pressing
// Enter without editing must not count as a change (it would restart the agent).
func TestEditPaths_NoChangeAfterShowUnderHome(t *testing.T) {
	tmp := realTempDir(t)
	t.Setenv("HOME", tmp)
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	mkdirs(t, a, b)

	d := NewEditPathsDialog()
	d.Show(newTestMultiRepoInstance([]string{a, b}), nil)

	if d.paths[0] != "~/a" {
		t.Fatalf("display path = %q, want ~/a", d.paths[0])
	}
	if d.HasChanged() {
		t.Fatalf("HasChanged() = true right after Show for paths under HOME (paths=%v)", d.paths)
	}
}

// HOME=/x/ash must not collapse /x/ashesh/r1 into "~esh/r1".
func TestEditPaths_HomePrefixBoundary(t *testing.T) {
	tmp := realTempDir(t)
	home := filepath.Join(tmp, "ash")
	r1 := filepath.Join(tmp, "ashesh", "r1")
	r2 := filepath.Join(tmp, "ashesh", "r2")
	mkdirs(t, home, r1, r2)
	t.Setenv("HOME", home)

	d := NewEditPathsDialog()
	d.Show(newTestMultiRepoInstance([]string{r1, r2}), nil)

	if strings.HasPrefix(d.paths[0], "~") {
		t.Fatalf("path collapsed across HOME boundary: %q", d.paths[0])
	}
	if msg := d.Validate(); msg != "" {
		t.Fatalf("Validate() = %q, want \"\"", msg)
	}
	if d.HasChanged() {
		t.Fatal("HasChanged() = true right after Show")
	}
}
