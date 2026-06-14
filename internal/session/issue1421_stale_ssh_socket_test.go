package session

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestIssue1421_CleanStaleSSHSockets verifies the orphaned-ControlMaster-socket
// sweep: a stale socket (file present, no listener) is removed, while a live
// socket (listener present) and a non-socket regular file are left untouched.
//
// This is the #1421 regression: when an SSH master dies (remote update /
// network drop) the ControlPath socket is left behind, and the next
// ControlMaster=auto connect hangs forever because ConnectTimeout does not
// bound the Unix-socket dial. The sweep removes the dead socket so the next ssh
// opens a fresh master instead of blocking.
func TestIssue1421_CleanStaleSSHSockets(t *testing.T) {
	dir := t.TempDir()

	// (a) Stale socket: a leftover socket inode with no listener — exactly the
	// on-disk state a crashed/killed SSH master leaves behind. SetUnlinkOnClose
	// (false) keeps the socket file after Close so it becomes a true orphan.
	stalePath := filepath.Join(dir, "stale@host:22")
	recreateOrphanSocket(t, stalePath)

	// (b) Live socket: a listener that stays open for the duration of the test.
	livePath := filepath.Join(dir, "live@host:22")
	liveLn, err := net.Listen("unix", livePath)
	if err != nil {
		t.Fatalf("create live socket: %v", err)
	}
	defer liveLn.Close()

	// (c) Non-socket regular file: must never be touched.
	regularPath := filepath.Join(dir, "not-a-socket.txt")
	if err := os.WriteFile(regularPath, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}

	cleanStaleSSHSocketsIn(dir)

	// Stale socket must be gone.
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale socket should have been removed, stat err=%v", err)
	}
	// Live socket must survive.
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("live socket should have been kept, stat err=%v", err)
	}
	// Regular file must survive.
	if _, err := os.Stat(regularPath); err != nil {
		t.Errorf("regular file should have been kept, stat err=%v", err)
	}
}

// recreateOrphanSocket reproduces a leftover socket inode at path: bind a
// listener to materialize the socket file, then close just the FD via
// SyscallConn so the inode remains on disk with no process accepting on it
// (mirrors a dead SSH master's ControlPath).
func recreateOrphanSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("recreate orphan socket: %v", err)
	}
	// SetUnlinkOnClose(false) keeps the socket file on disk after Close so it
	// becomes a true orphan inode with no listener.
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatalf("orphan socket not created at %s", path)
	}
}

// TestIssue1421_CleanStaleSSHSockets_MissingDir is a no-op when the control dir
// does not exist (no remotes ever used). Must not panic or error.
func TestIssue1421_CleanStaleSSHSockets_MissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cleanStaleSSHSocketsIn(missing) // must not panic
}

// TestIssue1421_SSHConnOptsStillUseControlMaster guards that the fix did not
// accidentally drop ControlMaster (the sweep is a complement to, not a
// replacement for, connection multiplexing).
func TestIssue1421_SSHConnOptsStillUseControlMaster(t *testing.T) {
	r := &SSHRunner{Host: "user@host"}
	opts := r.sshConnOpts()
	joined := ""
	for _, o := range opts {
		joined += o + " "
	}
	for _, want := range []string{"ControlMaster=auto", "ControlPersist=600", "ConnectTimeout=10"} {
		found := false
		for _, o := range opts {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("sshConnOpts missing %q; got: %s", want, joined)
		}
	}
}

// timeoutErr is a net.Error that reports Timeout()==true, used to assert that
// a probe timeout does NOT classify a socket as stale.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestIssue1421_IsStaleSocketDialErr_OnlyRemovesOnConfirmedDead verifies that
// only an unambiguous "no listener" signal (ECONNREFUSED / ENOENT) marks a
// socket stale. A timeout or a resource/permission error must NOT — removing on
// those could tear down a LIVE master (#1421, Codex review).
func TestIssue1421_IsStaleSocketDialErr_OnlyRemovesOnConfirmedDead(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"econnrefused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		{"enoent", &net.OpError{Op: "dial", Err: syscall.ENOENT}, true},
		{"timeout", timeoutErr{}, false},
		{"eacces", &net.OpError{Op: "dial", Err: syscall.EACCES}, false},
		{"emfile", &net.OpError{Op: "dial", Err: syscall.EMFILE}, false},
		{"generic", errorsNew("some other error"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaleSocketDialErr(tc.err); got != tc.want {
				t.Errorf("isStaleSocketDialErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func errorsNew(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }
