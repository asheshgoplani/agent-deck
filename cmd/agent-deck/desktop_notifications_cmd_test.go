package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDesktopHelperCreatesPrivateExecutable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(source, []byte("helper bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "bundle", "agent-deck-notifications")
	if err := os.Mkdir(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyDesktopHelper(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "helper bytes" {
		t.Fatalf("copied bytes = %q", got)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 0700", info.Mode().Perm())
	}
}
