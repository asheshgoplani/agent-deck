//go:build unix

package session

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO at info/exclude (reported at its lexical path inside the git dir)
// is refused after the open instead of blocking forever on the read.
func TestProjectMCPGitExclude_FIFOExcludeRefused(t *testing.T) {
	proj := hygieneSetup(t, map[string]string{"TOKEN": "s3cr3t"})
	hygieneGit(t, proj, "init", "-q")
	exclude := filepath.Join(proj, ".git", "info", "exclude")
	if err := os.Rename(exclude, exclude+".orig"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(exclude, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Real git would itself block opening the FIFO in check-ignore; the fake
	// answers "not ignored" so only agent-deck's own open is exercised.
	t.Setenv("FAKE_GIT_NOT_IGNORED", "1")
	installFakeGit(t, exclude)
	mcpFile := writeProjectMCPFile(t, proj)
	done := make(chan error, 1)
	go func() { done <- addProjectMCPGitExclude(proj, mcpFile) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO info/exclude not refused: %v", err)
		}
	case <-time.After(20 * time.Second):
		// Unblock the reader so the goroutine can finish.
		if w, err := os.OpenFile(exclude, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("blocked reading a FIFO info/exclude")
	}
}
