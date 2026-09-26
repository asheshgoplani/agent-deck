package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A missing session can make tmux has-session wait for the timeout. Readers
// must still be able to consume status while that external probe is pending.
func TestUpdateStatusMissingTmuxProbeDoesNotBlockReaders(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	shim := "#!/bin/sh\n: > '" + started + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.01; done\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	inst := &Instance{
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now().Add(-time.Minute),
		tmuxSession: &tmux.Session{Name: "biglist-missing"},
	}
	done := make(chan error, 1)
	go func() { done <- inst.UpdateStatus() }()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600); <-done })

	deadline := time.After(time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("tmux probe did not start")
		case <-time.After(time.Millisecond):
		}
	}

	read := make(chan Status, 1)
	go func() { read <- inst.GetStatusThreadSafe() }()
	select {
	case got := <-read:
		if got != StatusRunning {
			t.Fatalf("status during probe = %s", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("status reader blocked behind tmux probe")
	}
}
