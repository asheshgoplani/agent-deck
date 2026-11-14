package session

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/terminal"
)

// controlPathFromOpts extracts the ControlPath value from ssh -o options.
func controlPathFromOpts(t *testing.T, opts []string) string {
	t.Helper()
	for _, o := range opts {
		if v, ok := strings.CutPrefix(o, "ControlPath="); ok {
			return v
		}
	}
	t.Fatalf("no ControlPath in %q", opts)
	return ""
}

// TestSSHControlDir_PerUserPrivateAndShared is TM-6: the ControlMaster
// directory must be per user (/tmp/agent-deck-ssh-<uid>), a private 0700
// real directory owned by us, and the same one for every ssh builder in
// session and for the terminal launcher, so masters stay shared.
func TestSSHControlDir_PerUserPrivateAndShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ownership only")
	}
	cp := controlPathFromOpts(t, sessionSSHConnOpts())
	if !strings.HasSuffix(cp, "/%C") {
		t.Fatalf("ControlPath %q lost the %%C suffix", cp)
	}
	dir := filepath.Dir(cp)
	wantPrefix := "/tmp/agent-deck-ssh-" + strconv.Itoa(os.Getuid())
	if !strings.HasPrefix(dir, wantPrefix) {
		t.Fatalf("ControlPath dir = %q, want per-user %q", dir, wantPrefix)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("control dir not created: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("control dir %s mode = %v, want private dir 0700", dir, info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		t.Fatalf("control dir owned by %d, want %d", st.Uid, os.Getuid())
	}

	inst := NewInstance("ssh-dir", "/tmp")
	inst.SSHHost = "me@box"
	if wrapped := inst.wrapForSSH("true"); !strings.Contains(wrapped, "ControlPath="+shellQuote(cp)+" ") {
		t.Fatalf("wrapForSSH does not use %q: %s", cp, wrapped)
	}
	if mosh := terminal.MoshArgs("me@box", "", []string{"x"}); !strings.Contains(mosh[1], "ControlPath="+cp+" ") {
		t.Fatalf("terminal.MoshArgs does not use %q: %q", cp, mosh[1])
	}
	attach := terminal.BuildAttachCommand(terminal.AttachRequest{Remote: &terminal.RemoteAttach{Host: "me@box"}, Name: "s"})
	if !strings.Contains(attach, "ControlPath="+shellQuote(cp)) {
		t.Fatalf("terminal.BuildAttachCommand does not use %q: %s", cp, attach)
	}
}

// TestSSHControlDir_CleanStaleSweepsPerUserDir: CleanStaleSSHSockets must
// probe the directory masters actually live in now.
func TestSSHControlDir_CleanStaleSweepsPerUserDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets only")
	}
	dir := filepath.Dir(controlPathFromOpts(t, sessionSSHConnOpts()))
	stale := filepath.Join(dir, fmt.Sprintf("tm6-%d", time.Now().UnixNano()))
	recreateOrphanSocket(t, stale)
	t.Cleanup(func() { _ = os.Remove(stale) })
	CleanStaleSSHSockets()
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale socket in %s survived CleanStaleSSHSockets: %v", dir, err)
	}
}
