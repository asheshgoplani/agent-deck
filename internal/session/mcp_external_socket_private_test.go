package session

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

func shortSessTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ss")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func listenUnixSess(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
}

// CLI-mode discovery and stale-disk recovery find the TUI's pool socket in the
// per-user socket dir the pool creates sockets in, and reject a symlinked one.
func TestGetExternalSocketPath_UsesPrivatePoolSocketDir(t *testing.T) {
	home := shortSessTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "d"))

	dir, err := agentpaths.EffectiveDataPath("sockets", "sockets")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "mcp-own.sock")
	listenUnixSess(t, own)
	if got := getExternalSocketPath("own"); got != own {
		t.Fatalf("getExternalSocketPath(own) = %q, want %q", got, own)
	}

	live := filepath.Join(shortSessTempDir(t), "x.sock")
	listenUnixSess(t, live)
	if err := os.Symlink(live, filepath.Join(dir, "mcp-link.sock")); err != nil {
		t.Fatal(err)
	}
	if got := getExternalSocketPath("link"); got != "" {
		t.Fatalf("getExternalSocketPath(link) = %q, want empty for a symlinked socket", got)
	}

	// Own live socket, but the socket dir is world-writable: refused.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if got := getExternalSocketPath("own"); got != "" {
		t.Fatalf("getExternalSocketPath(own) = %q, want empty in a world-writable dir", got)
	}
}

// The last-resort temp state root is a per-user 0700 directory owned by us,
// never a shared "<TempDir>/.agent-deck" that another user could plant.
func TestTempAgentDeckPath_PrivatePerUserRoot(t *testing.T) {
	check := func(t *testing.T, tmp string) {
		t.Helper()
		p := tempAgentDeckPath("runtime", "x")
		rel, err := filepath.Rel(tmp, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("tempAgentDeckPath = %q, not under TMPDIR %q", p, tmp)
		}
		root := filepath.Join(tmp, strings.Split(rel, string(filepath.Separator))[0])
		fi, err := os.Lstat(root)
		if err != nil {
			t.Fatalf("root %s: %v", root, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			t.Fatalf("root %s is not a real dir (mode %v)", root, fi.Mode())
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("root %s mode = %v, want 0700", root, fi.Mode().Perm())
		}
		if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() {
			t.Fatalf("root %s uid = %d, want %d", root, st.Uid, os.Getuid())
		}
	}

	t.Run("shared_name_planted", func(t *testing.T) {
		tmp := shortSessTempDir(t)
		t.Setenv("TMPDIR", tmp)
		if err := os.Symlink(shortSessTempDir(t), filepath.Join(tmp, ".agent-deck")); err != nil {
			t.Fatal(err)
		}
		check(t, tmp)
	})
	t.Run("per_user_name_planted", func(t *testing.T) {
		tmp := shortSessTempDir(t)
		t.Setenv("TMPDIR", tmp)
		planted := filepath.Join(tmp, fmt.Sprintf(".agent-deck-%d", os.Getuid()))
		if err := os.Symlink(shortSessTempDir(t), planted); err != nil {
			t.Fatal(err)
		}
		check(t, tmp)
		if strings.HasPrefix(tempAgentDeckPath("x"), planted+"/") {
			t.Fatalf("tempAgentDeckPath adopted planted symlink %s", planted)
		}
	})
}
