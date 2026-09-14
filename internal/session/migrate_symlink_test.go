package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFileVerifiedRefusesWritableDestinationSymlink(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	outside := filepath.Join(dir, "outside.jsonl")
	dest := filepath.Join(dir, "dest.jsonl")
	seed := []byte("source context must remain intact\n")
	if err := os.WriteFile(source, seed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dest); err != nil {
		t.Fatal(err)
	}
	if err := copyFileVerified(source, dest); err == nil {
		t.Fatal("copy followed writable destination symlink")
	}
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(seed) {
		t.Fatalf("source changed after refused copy: %q", got)
	}
	got, err = os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside sentinel\n" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}
