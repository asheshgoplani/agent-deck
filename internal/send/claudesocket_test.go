package send

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startFakeInbox starts a Unix-socket listener that reads newline-delimited
// lines off each accepted connection and hands the collected slice down
// conns once the peer closes. It never writes anything back — this is the
// load-bearing property of the real Claude inbox (§1.5): the receiver never
// replies on any path, so a fake that also never replies is what makes a
// success-vs-refusal-indistinguishable-in-band test honest.
//
// t.TempDir() paths embed the full test name and can exceed Darwin's
// 104-byte sun_path budget, so this uses a short os.MkdirTemp base instead
// (precedent: internal/session/issue1421_stale_ssh_socket_test.go).
func startFakeInbox(t *testing.T) (sockPath string, conns chan []string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath = filepath.Join(dir, "x.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	conns = make(chan []string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
				var lines []string
				for sc.Scan() {
					lines = append(lines, sc.Text())
				}
				conns <- lines
			}(c)
		}
	}()
	return sockPath, conns
}

func recvLines(t *testing.T, conns chan []string) []string {
	t.Helper()
	select {
	case lines := <-conns:
		return lines
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the fake inbox to receive a connection")
		return nil
	}
}

// newLiveSocket returns a real, listening Unix-socket path for a test that
// only needs ResolveClaudeSocketTarget to see a genuine socket file
// (ownership, ModeSocket) — it never accepts or reads a connection. Short
// os.MkdirTemp base, not t.TempDir(): t.TempDir() embeds the full (often
// long) test/subtest name, which can exceed Darwin's 104-byte sun_path
// budget (precedent: internal/session/issue1421_stale_ssh_socket_test.go).
func newLiveSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "x.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return sockPath
}

// writeKeyFile writes a well-formed <pid>.<sha256hex>.key file for
// sockPath/pid under claudeDir/sessions — the deterministic filename shape
// Claude's own client publishes (§1.3).
func writeKeyFile(t *testing.T, claudeDir string, pid int, sockPath, token, procStart string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(filepath.Clean(sockPath)))
	name := strconv.Itoa(pid) + "." + hex.EncodeToString(sum[:]) + ".key"
	return writeArbitraryKeyFile(t, claudeDir, name, token, procStart)
}

// writeArbitraryKeyFile writes a key file under claudeDir/sessions/<filename>
// with an arbitrary name — used for the <pid>.*.key glob-fallback tests,
// where the filename deliberately does NOT match the deterministic
// sha256(socketPath) shape.
func writeArbitraryKeyFile(t *testing.T, claudeDir, filename, token, procStart string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, filename)
	body := map[string]any{"peerToken": token}
	if procStart != "" {
		body["procStart"] = procStart
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal key body: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

const testUUIDRe = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

func TestSendOverClaudeSocket_WireFormat_TwoLinesAuthThenUser(t *testing.T) {
	sockPath, conns := startFakeInbox(t)
	target := ClaudeSocketTarget{
		SocketPath: sockPath,
		Pid:        1234,
		SessionID:  "sid-target",
		AuthToken:  "0123456789abcdef0123456789abcdef",
	}

	const content = "line one\nline two with \"quotes\" and a tab\t, and unicode: héllo 世界"
	msgID, err := SendOverClaudeSocket(target, content)
	if err != nil {
		t.Fatalf("SendOverClaudeSocket: %v", err)
	}
	if ok, _ := regexp.MatchString(testUUIDRe, msgID); !ok {
		t.Errorf("msgID %q does not look like a UUID v4", msgID)
	}

	lines := recvLines(t, conns)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want exactly 2: %#v", len(lines), lines)
	}

	var auth authFrame
	if err := json.Unmarshal([]byte(lines[0]), &auth); err != nil {
		t.Fatalf("line 1 is not a valid auth frame: %v", err)
	}
	if auth.Type != "auth" || auth.Token != target.AuthToken {
		t.Errorf("auth frame = %+v, want type=auth token=%s", auth, target.AuthToken)
	}

	var user userFrame
	if err := json.Unmarshal([]byte(lines[1]), &user); err != nil {
		t.Fatalf("line 2 is not a valid user frame: %v", err)
	}
	if user.Type != "user" {
		t.Errorf("user.Type = %q, want %q", user.Type, "user")
	}
	if user.Priority != "next" {
		t.Errorf("user.Priority = %q, want %q", user.Priority, "next")
	}
	if user.MsgV != 1 {
		t.Errorf("user.MsgV = %d, want 1", user.MsgV)
	}
	if user.MsgID != msgID {
		t.Errorf("user.MsgID = %q, want the returned msgID %q", user.MsgID, msgID)
	}
	if user.SessionID != target.SessionID {
		t.Errorf("user.SessionID = %q, want %q", user.SessionID, target.SessionID)
	}
	if user.Message.Content != content {
		t.Errorf("user.Message.Content = %q, want byte-identical %q", user.Message.Content, content)
	}
}

func TestSendOverClaudeSocket_NoAuthToken_OmitsAuthLine(t *testing.T) {
	sockPath, conns := startFakeInbox(t)
	target := ClaudeSocketTarget{SocketPath: sockPath, Pid: 1, SessionID: "sid", AuthToken: ""}

	if _, err := SendOverClaudeSocket(target, "no auth line here"); err != nil {
		t.Fatalf("SendOverClaudeSocket: %v", err)
	}
	lines := recvLines(t, conns)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want exactly 1 (no auth line): %#v", len(lines), lines)
	}
	var user userFrame
	if err := json.Unmarshal([]byte(lines[0]), &user); err != nil {
		t.Fatalf("line is not a valid user frame: %v", err)
	}
	if user.Type != "user" {
		t.Errorf("Type = %q, want user", user.Type)
	}
}

func TestSendOverClaudeSocket_ServerWritesNothingBack_StillSuccess(t *testing.T) {
	// Encodes §1.5: the real Claude inbox never replies on any path. A fake
	// inbox that only reads (startFakeInbox) and a send that still reports
	// success proves the client doesn't wait for or require a reply.
	sockPath, _ := startFakeInbox(t)
	target := ClaudeSocketTarget{SocketPath: sockPath, Pid: 1, SessionID: "sid"}
	if _, err := SendOverClaudeSocket(target, "hello"); err != nil {
		t.Fatalf("expected success with no reply, got: %v", err)
	}
}

func TestSendOverClaudeSocket_WriteFailureAfterDial_YieldsCommittedError_NotUnavailable(t *testing.T) {
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		return &failingWriteConn{}, nil
	}

	target := ClaudeSocketTarget{SocketPath: "/does/not/matter", Pid: 1, SessionID: "sid"}
	_, err := SendOverClaudeSocket(target, "hello")
	if err == nil {
		t.Fatal("expected an error from a failing write")
	}
	var committed *CommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("got %T (%v), want *CommittedError — a write-phase failure must never look like a pre-write Unavailable (no tmux fallback)", err, err)
	}
	var unavail *Unavailable
	if errors.As(err, &unavail) {
		t.Fatalf("write-phase failure must not be an *Unavailable (would trigger a tmux fallback and risk double delivery)")
	}
}

// TestSendOverClaudeSocket_WriteDeadlineExpiry_YieldsCommittedError covers
// the write deadline added after the reviewer flagged that a stuck/wedged
// receiver on the other end could hang the CLI forever with no deadline. A
// deadline expiry surfaces as an ordinary Write() error, which is already
// the correct *CommittedError path (bytes may already be partway onto the
// wire when the deadline fires, so it is never a safe-to-retry refusal).
func TestSendOverClaudeSocket_WriteDeadlineExpiry_YieldsCommittedError(t *testing.T) {
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	conn := &timeoutWriteConn{}
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		return conn, nil
	}

	target := ClaudeSocketTarget{SocketPath: "/does/not/matter", Pid: 1, SessionID: "sid"}
	_, err := SendOverClaudeSocket(target, "hello")
	var committed *CommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("got %T (%v), want *CommittedError for a write-deadline expiry", err, err)
	}
	// A test that would pass identically whether or not SendOverClaudeSocket
	// ever calls SetWriteDeadline proves nothing about the deadline itself —
	// assert it was actually set, and set before Write ran.
	if conn.deadlineSet.IsZero() {
		t.Errorf("SetWriteDeadline was never called")
	}
	if !conn.writeSeenDeadline {
		t.Errorf("Write ran before SetWriteDeadline was called")
	}
}

// failingWriteConn is a minimal net.Conn whose Write always fails, used to
// deterministically exercise the "write started and failed" path without
// racing a real socket's kernel buffering (a genuinely short local message
// essentially never fails mid-write on a real Unix socket).
type failingWriteConn struct{ net.Conn }

func (f *failingWriteConn) Write(b []byte) (int, error) {
	return 0, errors.New("simulated write failure")
}
func (f *failingWriteConn) Close() error                     { return nil }
func (f *failingWriteConn) SetDeadline(time.Time) error      { return nil }
func (f *failingWriteConn) SetWriteDeadline(time.Time) error { return nil }

// timeoutErr is a net.Error-shaped timeout, standing in for what a real
// expired write deadline returns.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// timeoutWriteConn is a minimal net.Conn whose Write always returns a
// timeout error, simulating an expired write deadline against a
// stuck/wedged receiver. It records whether SetWriteDeadline was actually
// called, and by the time Write ran, so the test above can tell a real
// deadline-setting implementation from one that dropped the call.
type timeoutWriteConn struct {
	net.Conn
	deadlineSet       time.Time
	writeSeenDeadline bool
}

func (c *timeoutWriteConn) SetWriteDeadline(d time.Time) error {
	c.deadlineSet = d
	return nil
}
func (c *timeoutWriteConn) Write([]byte) (int, error) {
	c.writeSeenDeadline = !c.deadlineSet.IsZero()
	return 0, timeoutErr{}
}
func (c *timeoutWriteConn) Close() error { return nil }

func TestSendOverClaudeSocket_MessageTooLarge_IsUnavailable(t *testing.T) {
	target := ClaudeSocketTarget{SocketPath: "/does/not/matter", Pid: 1, SessionID: "sid", AuthToken: strings.Repeat("a", 32)}
	huge := strings.Repeat("x", maxLineChars) // guarantees the marshaled frame exceeds the cap
	_, err := SendOverClaudeSocket(target, huge)
	var unavail *Unavailable
	if !errors.As(err, &unavail) || unavail.Reason != ReasonTooLarge {
		t.Fatalf("got %v, want *Unavailable{Reason: ReasonTooLarge}", err)
	}
}

func TestSendOverClaudeSocket_TokenNeverAppearsInErrorStrings(t *testing.T) {
	const secret = "deadbeefdeadbeefdeadbeefdeadbeef"
	target := ClaudeSocketTarget{SocketPath: "/definitely/does/not/exist.sock", Pid: 1, SessionID: "sid", AuthToken: secret}
	_, err := SendOverClaudeSocket(target, "hello")
	if err == nil {
		t.Fatal("expected a dial error against a nonexistent socket path")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked into error string: %v", err)
	}
}

// TestResolveClaudeSocketTarget_TokenNeverAppearsInErrorStrings extends the
// token-leak invariant to the resolver: the one resolver path that returns
// an error while still holding a live, correctly-shaped token is a
// deterministic key file with a torn-pair procStart (ReasonNoKey), so that
// is the highest-risk spot for a leak.
func TestResolveClaudeSocketTarget_TokenNeverAppearsInErrorStrings(t *testing.T) {
	const secret = "cafebabecafebabecafebabecafebabe"
	claudeDir := t.TempDir()
	sockPath := newLiveSocket(t)
	livePid := os.Getpid()

	orig := psLstart
	t.Cleanup(func() { psLstart = orig })
	psLstart = func(int) (string, error) { return "matches-the-record", nil }

	writeKeyFile(t, claudeDir, livePid, sockPath, secret, "does-not-match-the-record")

	rec := ClaudeSocketRecord{
		Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1,
		ProcStart: "matches-the-record",
	}
	_, err := ResolveClaudeSocketTarget(rec, claudeDir)
	if err == nil {
		t.Fatal("expected a torn-pair ReasonNoKey error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked into resolver error string: %v", err)
	}
}

// --- ResolveClaudeSocketTarget resolver table ---

func TestResolveClaudeSocketTarget(t *testing.T) {
	livePid := os.Getpid() // this test process: genuinely alive, and its ps lstart= is queryable for real.

	t.Run("missing record", func(t *testing.T) {
		_, err := ResolveClaudeSocketTarget(ClaudeSocketRecord{}, t.TempDir())
		assertUnavailable(t, err, ReasonNoRecord)
	})

	t.Run("empty session id -> ReasonNoRecord", func(t *testing.T) {
		// An empty session_id in the frame is silently dropped by Claude
		// (session_id mismatch, §1.4), not delivered — reject it here rather
		// than reporting a false "queued_socket".
		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "", MessagingSocketPath: "/tmp/whatever.sock", PeerProtocol: 1}
		_, err := ResolveClaudeSocketTarget(rec, t.TempDir())
		assertUnavailable(t, err, ReasonNoRecord)
	})

	t.Run("no socket path", func(t *testing.T) {
		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", PeerProtocol: 1}
		_, err := ResolveClaudeSocketTarget(rec, t.TempDir())
		assertUnavailable(t, err, ReasonNoSocketPath)
	})

	t.Run("peerProtocol 0", func(t *testing.T) {
		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: "/tmp/whatever.sock"}
		_, err := ResolveClaudeSocketTarget(rec, t.TempDir())
		assertUnavailable(t, err, ReasonOldProtocol)
	})

	t.Run("peerProtocol 2 (future bump) is also unavailable, exact-match gate", func(t *testing.T) {
		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: "/tmp/whatever.sock", PeerProtocol: 2}
		_, err := ResolveClaudeSocketTarget(rec, t.TempDir())
		assertUnavailable(t, err, ReasonOldProtocol)
	})

	t.Run("dead pid", func(t *testing.T) {
		orig := killCheck
		t.Cleanup(func() { killCheck = orig })
		killCheck = func(pid int) error { return syscall.ESRCH }
		rec := ClaudeSocketRecord{Pid: 999999, SessionID: "sid", MessagingSocketPath: "/tmp/whatever.sock", PeerProtocol: 1}
		_, err := ResolveClaudeSocketTarget(rec, t.TempDir())
		assertUnavailable(t, err, ReasonDeadPid)
	})

	t.Run("procStart drift", func(t *testing.T) {
		claudeDir := t.TempDir()
		sockPath := newLiveSocket(t)
		rec := ClaudeSocketRecord{
			Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1,
			ProcStart: "definitely not the real ps lstart output",
		}
		_, err := ResolveClaudeSocketTarget(rec, claudeDir)
		assertUnavailable(t, err, ReasonProcStartDrift)
	})

	t.Run("key file 0644 (group/other bits) -> ReasonNoKey", func(t *testing.T) {
		claudeDir := t.TempDir()
		sockPath := newLiveSocket(t)
		keyPath := writeKeyFile(t, claudeDir, livePid, sockPath, strings.Repeat("a", 32), "")
		if err := os.Chmod(keyPath, 0o644); err != nil {
			t.Fatal(err)
		}

		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1}
		_, err := ResolveClaudeSocketTarget(rec, claudeDir)
		assertUnavailable(t, err, ReasonNoKey)
	})

	t.Run("key file with non-hex token -> ReasonNoKey", func(t *testing.T) {
		claudeDir := t.TempDir()
		sockPath := newLiveSocket(t)
		writeKeyFile(t, claudeDir, livePid, sockPath, "not-hex-not-32-chars", "")

		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1}
		_, err := ResolveClaudeSocketTarget(rec, claudeDir)
		assertUnavailable(t, err, ReasonNoKey)
	})

	t.Run("key file procStart disagrees with record (torn pair) -> ReasonNoKey", func(t *testing.T) {
		claudeDir := t.TempDir()
		sockPath := newLiveSocket(t)

		orig := psLstart
		t.Cleanup(func() { psLstart = orig })
		psLstart = func(pid int) (string, error) { return "matches-the-record", nil }

		writeKeyFile(t, claudeDir, livePid, sockPath, strings.Repeat("a", 32), "does-not-match-the-record")

		rec := ClaudeSocketRecord{
			Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1,
			ProcStart: "matches-the-record",
		}
		_, err := ResolveClaudeSocketTarget(rec, claudeDir)
		assertUnavailable(t, err, ReasonNoKey)
	})

	t.Run("no key file at all -> success, no auth line, not Unavailable", func(t *testing.T) {
		claudeDir := t.TempDir() // sessions dir has no key files
		sockPath := newLiveSocket(t)

		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1}
		target, err := ResolveClaudeSocketTarget(rec, claudeDir)
		if err != nil {
			t.Fatalf("expected success (absent key file is not a fallback reason), got: %v", err)
		}
		if target.AuthToken != "" {
			t.Errorf("AuthToken = %q, want empty when no key file exists", target.AuthToken)
		}
	})

	t.Run("well-formed key file -> success, AuthToken populated", func(t *testing.T) {
		claudeDir := t.TempDir()
		sockPath := newLiveSocket(t)
		writeKeyFile(t, claudeDir, livePid, sockPath, strings.Repeat("b", 32), "")

		rec := ClaudeSocketRecord{Pid: livePid, SessionID: "sid", MessagingSocketPath: sockPath, PeerProtocol: 1}
		target, err := ResolveClaudeSocketTarget(rec, claudeDir)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		if target.AuthToken != strings.Repeat("b", 32) {
			t.Errorf("AuthToken = %q, want the key file's peerToken", target.AuthToken)
		}
	})

	// No <pid>.*.key glob fallback (removed per review): only the
	// deterministic-name key is ever consulted, so an arbitrarily-named key
	// file sitting in sessions/ is simply invisible to resolveAuthToken —
	// covered implicitly by "no key file at all" above, since from the
	// resolver's point of view that's exactly what a non-deterministic-name
	// file looks like.

}

func assertUnavailable(t *testing.T, err error, want UnavailableReason) {
	t.Helper()
	var u *Unavailable
	if !errors.As(err, &u) {
		t.Fatalf("got %T (%v), want *Unavailable", err, err)
	}
	if u.Reason != want {
		t.Errorf("Reason = %q, want %q", u.Reason, want)
	}
}
