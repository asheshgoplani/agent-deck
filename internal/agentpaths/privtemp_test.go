//go:build unix

package agentpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSecureTempDir(t *testing.T) {
	assertPrivate := func(t *testing.T, dir string) {
		t.Helper()
		fi, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: mode %v, want real 0700 dir", dir, fi.Mode())
		}
		if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() {
			t.Fatalf("%s uid %d, want %d", dir, st.Uid, os.Getuid())
		}
	}
	stableName := func(tmp string) string {
		return filepath.Join(tmp, fmt.Sprintf("p-%d", os.Getuid()))
	}

	t.Run("creates_stable", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		dir, note := SecureTempDir("p")
		if dir != stableName(tmp) || note != "" {
			t.Fatalf("got (%q, %q), want (%q, \"\")", dir, note, stableName(tmp))
		}
		assertPrivate(t, dir)
		if again, _ := SecureTempDir("p"); again != dir {
			t.Fatalf("second call = %q, want stable %q", again, dir)
		}
	})
	t.Run("tightens_own", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("TMPDIR", tmp)
		if err := os.Mkdir(stableName(tmp), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stableName(tmp), 0o755); err != nil {
			t.Fatal(err)
		}
		dir, note := SecureTempDir("p")
		if dir != stableName(tmp) || note == "" {
			t.Fatalf("got (%q, %q), want stable dir with a notice", dir, note)
		}
		assertPrivate(t, dir)
	})
	for _, kind := range []string{"symlink", "file"} {
		t.Run("refuses_"+kind, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), stableName(tmp)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(stableName(tmp), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			dir, note := SecureTempDir("p")
			if dir == stableName(tmp) || note == "" {
				t.Fatalf("got (%q, %q), want a fresh private dir and a notice", dir, note)
			}
			assertPrivate(t, dir)
			if again, note2 := SecureTempDir("p"); again != dir || note2 != "" {
				t.Fatalf("second call = (%q, %q), want cached %q with no repeat notice", again, note2, dir)
			}
		})
	}
}
