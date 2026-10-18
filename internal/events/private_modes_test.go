package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The profile log carries what sessions and the Mac app exchange, so it is
// private to its user: 0o700 directories and 0o600 files, for a new log and
// for one an older core wrote with 0o755/0o644. Making it private does not
// make it a fsyncing ledger.
func TestBusFilesArePrivate(t *testing.T) {
	check := func(t *testing.T, dir string) {
		t.Helper()
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			want := os.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s: mode %o, want %o", path, got, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	exercise := func(t *testing.T, b *Bus) {
		t.Helper()
		release := b.Want(DemandKinds...)
		for i := 0; i < 3; i++ {
			b.Publish("macapp.browser.response", "s", map[string]int{"i": i})
		}
		if !b.Flush(2 * time.Second) {
			t.Fatal("flush")
		}
		release()
	}

	t.Run("new", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "events", "default")
		b, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		exercise(t, b)
		if b.durable() {
			t.Error("the profile log became a fsyncing ledger")
		}
		_ = b.Close()
		check(t, dir)
	})
	t.Run("existing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "events", "default")
		if err := os.MkdirAll(filepath.Join(dir, demandDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{activeSegmentName, "writer.lock", filepath.Join(demandDirName, "tmux.output.1.x")} {
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		exercise(t, b)
		_ = b.Close()
		check(t, dir)
	})
}
