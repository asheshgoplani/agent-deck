package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #2164: a self-update onto a binary whose directory the user cannot write
// (root-owned /usr/local/bin) must name the path, the user and the remedy
// instead of a bare "permission denied" on the staged .new file.
func TestInstallSelfUpdateBinary_NotWritableReportsRemedy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; the permission failure cannot be reproduced")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := installSelfUpdateBinary(execPath, []byte("new"))
	var notWritable *InstallPathNotWritableError
	if !errors.As(err, &notWritable) {
		t.Fatalf("got %v, want InstallPathNotWritableError", err)
	}
	if notWritable.Path != execPath || notWritable.User == "" {
		t.Errorf("error carries path %q user %q", notWritable.Path, notWritable.User)
	}
	msg := err.Error()
	for _, want := range []string{"install path " + execPath + " is not writable by " + notWritable.User, "~/.local/bin", "symlink", "sudo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	if got, _ := os.ReadFile(execPath); string(got) != "old" {
		t.Errorf("the installed binary must be untouched, got %q", got)
	}
}
