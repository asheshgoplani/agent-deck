package testutil_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBootstrapLeakSentinelSparesOtherPackagesServers pins issue 2712.
//
// Under `go test ./...` the internal/tmux and internal/session test binaries
// run alongside internal/testutil, each with a live bootstrap server in its own
// /tmp/ad-tmux-* dir. Those dirs can appear while the sentinel's child run is
// in flight, so "new since the snapshot" does not mean "the child's". Killing
// such a server took down TestTmuxBootstrap_ServerIsRunning and every test
// behind it in the other package, and the sentinel reported its own kill as a
// leak. Only the server the tagged child started may be reaped.
func TestBootstrapLeakSentinelSparesOtherPackagesServers(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	const token = "sentinel-ownership-test"
	mine := startFakeBootstrapServer(t, token)
	foreign := startFakeBootstrapServer(t, "")

	leaked := reapLeakedBootstrapServers([]string{mine.dir, foreign.dir}, token)

	if len(leaked) != 1 || leaked[0] != mine.sock {
		t.Fatalf("reaped %v, want only the tagged child's server %s", leaked, mine.sock)
	}
	if !fakeBootstrapServerAlive(foreign.sock) {
		t.Fatalf("sentinel killed another package's live bootstrap server %s", foreign.sock)
	}
	if fakeBootstrapServerAlive(mine.sock) {
		t.Fatalf("sentinel left the tagged child's leaked server %s alive", mine.sock)
	}
}

type fakeBootstrapServer struct{ dir, sock string }

// startFakeBootstrapServer starts a tmux server laid out the way
// IsolateTmuxSocket and a package TestMain leave one: <dir>/tmux-<uid>/default
// holding a bootstrap session. token "" starts it without the sentinel tag.
func startFakeBootstrapServer(t *testing.T, token string) fakeBootstrapServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ad-sentinel-")
	if err != nil {
		t.Fatal(err)
	}
	sockDir := filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sockDir, "default")
	env := envWithoutTmux()
	if token != "" {
		env = append(env, bootstrapSentinelTokenEnv+"="+token)
	}
	cmd := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "agent-deck-test-bootstrap", "sleep", "600")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start fake bootstrap server: %v: %s", err, out)
	}
	t.Cleanup(func() {
		kill := exec.Command("tmux", "-S", sock, "kill-server")
		kill.Env = envWithoutTmux()
		_ = kill.Run()
		_ = os.RemoveAll(dir)
	})
	return fakeBootstrapServer{dir: dir, sock: sock}
}

func fakeBootstrapServerAlive(sock string) bool {
	cmd := exec.Command("tmux", "-S", sock, "has-session")
	cmd.Env = envWithoutTmux()
	return cmd.Run() == nil
}
