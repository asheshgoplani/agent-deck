package mcppool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// LOW-6: a live socket of this user that fails verification (here: its dir
// is group/other-writable, or the name is a symlink) is refused, never
// unlinked from under the process serving it.
func TestNewSocketProxy_RefusesUnverifiedLiveSocketWithoutUnlinking(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, socketFileName("busy"))
	listenUnixT(t, sock)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	p, err := NewSocketProxy(context.Background(), "busy", "true", nil, nil)
	if err == nil {
		t.Fatalf("NewSocketProxy accepted a socket in a world-writable dir (proxy %v)", p != nil)
	}
	if _, statErr := os.Lstat(sock); statErr != nil {
		t.Fatalf("live socket was unlinked: %v", statErr)
	}
	if !isSocketAlive(sock) {
		t.Fatal("live socket no longer accepts connections")
	}
}

func TestNewSocketProxy_SymlinkAtSocketPathNotRemoved(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := shortTempDir(t)
	live := filepath.Join(elsewhere, "x.sock")
	listenUnixT(t, live)
	link := filepath.Join(dir, socketFileName("linked"))
	if err := os.Symlink(live, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSocketProxy(context.Background(), "linked", "true", nil, nil); err == nil {
		t.Fatal("NewSocketProxy accepted a symlink at the socket path")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink was removed: %v", err)
	}
}

// Stopping a proxy that reused another process's live socket leaves the
// socket in place for that process.
func TestSocketProxyStop_ExternalLiveSocketKept(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, socketFileName("shared"))
	listenUnixT(t, sock)

	p, err := NewSocketProxy(context.Background(), "shared", "true", nil, nil)
	if err != nil {
		t.Fatalf("NewSocketProxy: %v", err)
	}
	if p.GetStatus() != StatusRunning {
		t.Fatalf("live own socket was not reused (status %v)", p.GetStatus())
	}
	_ = p.Stop()
	if !isSocketAlive(sock) {
		t.Fatal("stopping the reusing proxy unlinked the serving process's live socket")
	}
}

// Restarting a proxy adopted from another agent-deck process keeps that
// process's live socket instead of unlinking it and starting a duplicate.
func TestRestartProxy_AdoptedLiveSocketKept(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, socketFileName("adopted"))
	listenUnixT(t, sock)

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	if n := pool.DiscoverExistingSockets(); n != 1 {
		t.Fatalf("DiscoverExistingSockets = %d, want 1", n)
	}
	_ = pool.RestartProxy("adopted")
	if !isSocketAlive(sock) {
		t.Fatal("restart unlinked the serving process's live socket")
	}
}
