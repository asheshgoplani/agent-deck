package send

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// A valid ULID (Crockford base32: no I, L, O or U).
const piWakeNonce = "01J0ABCDEF0123456789ABCDEF"

// piWakeIDs are the record ids a wake carries: an explicit frame field, so
// dedup never depends on the wake line's format.
var piWakeIDs = []string{"01J0RECORD00000000000ABC123", "01J0RECORD00000000000DEF456"}

// piWakeHome keeps a test off the real home (#2790): nothing in these tests
// resolves an agent-deck path, but the rule is that no test may be able to.
func piWakeHome(t *testing.T) {
	t.Helper()
	t.Cleanup(testutil.IsolateHome())
}

// piWakeShortDir is a directory under /tmp: macOS limits a unix socket path to
// 103 bytes and the default TMPDIR is already longer than that
// (internal/core/daemon/daemon_test.go shortDir).
func piWakeShortDir(t *testing.T) string {
	t.Helper()
	piWakeHome(t)
	dir, err := os.MkdirTemp("/tmp", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakePi listens the way the extension does (0700 directory, 0600 socket).
// For each connection it reads the first line, calls respond (nil: stay
// silent), then drains until the client closes and reports every byte it saw.
type fakePi struct {
	path  string
	dir   string
	ln    *net.UnixListener
	conns chan []byte
}

// listenPrivately binds <short tmp>/pi/w.sock the way the extension does.
func listenPrivately(t *testing.T) (dir, path string, ln *net.UnixListener) {
	t.Helper()
	dir = filepath.Join(piWakeShortDir(t), "pi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "w.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return dir, path, l.(*net.UnixListener)
}

func newFakePi(t *testing.T, respond func(conn net.Conn, frame []byte)) *fakePi {
	t.Helper()
	dir, path, ln := listenPrivately(t)
	f := &fakePi{path: path, dir: dir, ln: ln, conns: make(chan []byte, 8)}
	go func() {
		for {
			conn, err := f.ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				line, err := br.ReadBytes('\n')
				if err == nil && respond != nil {
					respond(conn, line)
				}
				rest, _ := io.ReadAll(br)
				f.conns <- append(line, rest...)
			}()
		}
	}()
	return f
}

// connection returns the bytes of the next connection the fake served.
func (f *fakePi) connection(t *testing.T) []byte {
	t.Helper()
	select {
	case data := <-f.conns:
		return data
	case <-time.After(10 * time.Second):
		t.Fatal("the fake extension never saw a connection")
		return nil
	}
}

// assertNothingWritten fails if any connection carried a byte. A client that
// refuses before dialing never shows up here, which is also fine.
func (f *fakePi) assertNothingWritten(t *testing.T) {
	t.Helper()
	select {
	case data := <-f.conns:
		if len(data) != 0 {
			t.Errorf("wrote %q to the extension; the failure was supposed to come before any write", data)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func replyWith(frame string) func(net.Conn, []byte) {
	return func(conn net.Conn, _ []byte) { _, _ = conn.Write([]byte(frame + "\n")) }
}

func piWakeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// wakePi sends one doorbell as the daemon would: this process is the
// listener, so {os.Getpid(): true} is the in-pane-tree case.
func wakePi(t *testing.T, path, line string, opts PiWakeOptions) (PiWakeResult, error) {
	t.Helper()
	if opts.PanePIDs == nil {
		opts.PanePIDs = map[int]bool{os.Getpid(): true}
	}
	return SendPiWake(piWakeContext(t), PiWakeTarget{Path: path}, piWakeNonce, line, piWakeIDs, opts)
}

func piReasonOf(t *testing.T, err error) string {
	t.Helper()
	var unavail *Unavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("error = %v (%T), want *Unavailable", err, err)
	}
	var committed *CommittedError
	if errors.As(err, &committed) {
		t.Errorf("error %v is *Unavailable and *CommittedError: nothing may be both typed and hard-failed", err)
	}
	return string(unavail.Reason)
}

func assertNeitherCommittedNorUnavailable(t *testing.T, err error) {
	t.Helper()
	var committed *CommittedError
	if errors.As(err, &committed) {
		t.Errorf("error %v is a *CommittedError, which the caller treats as a hard failure", err)
	}
	var unavail *Unavailable
	if errors.As(err, &unavail) {
		t.Errorf("error %v is *Unavailable, which the caller retypes into the pane", err)
	}
}

func TestPiWakeSocket_RefusesForeignOwnerWideModeOrPeerOutsidePaneTree(t *testing.T) {
	t.Run("a private socket owned by the caller vets clean", func(t *testing.T) {
		f := newFakePi(t, nil)
		if err := VetPiSocket(f.path, os.Getuid()); err != nil {
			t.Fatalf("VetPiSocket = %v, want nil", err)
		}
		// Vetting reads the file only; the one connection a wake makes carries it.
		f.assertNothingWritten(t)
	})

	t.Run("socket owned by someone else", func(t *testing.T) {
		f := newFakePi(t, nil)
		if got := piReasonOf(t, VetPiSocket(f.path, os.Getuid()+1)); got != "socket_perms" {
			t.Errorf("reason = %q, want socket_perms", got)
		}
	})

	t.Run("socket reachable by the group", func(t *testing.T) {
		f := newFakePi(t, nil)
		if err := os.Chmod(f.path, 0o770); err != nil {
			t.Fatal(err)
		}
		if got := piReasonOf(t, VetPiSocket(f.path, os.Getuid())); got != "socket_perms" {
			t.Errorf("reason = %q, want socket_perms", got)
		}
	})

	t.Run("socket reachable by others", func(t *testing.T) {
		f := newFakePi(t, nil)
		if err := os.Chmod(f.path, 0o602); err != nil {
			t.Fatal(err)
		}
		if got := piReasonOf(t, VetPiSocket(f.path, os.Getuid())); got != "socket_perms" {
			t.Errorf("reason = %q, want socket_perms", got)
		}
	})

	t.Run("directory open to the group or others", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o750, 0o705} {
			f := newFakePi(t, nil)
			if err := os.Chmod(f.dir, mode); err != nil {
				t.Fatal(err)
			}
			if got := piReasonOf(t, VetPiSocket(f.path, os.Getuid())); got != "socket_perms" {
				t.Errorf("dir mode %#o: reason = %q, want socket_perms", mode, got)
			}
		}
	})

	t.Run("a regular file is not a socket", func(t *testing.T) {
		dir := filepath.Join(piWakeShortDir(t), "pi")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "w.sock")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := piReasonOf(t, VetPiSocket(path, os.Getuid())); got != "no_socket" {
			t.Errorf("reason = %q, want no_socket", got)
		}
	})

	t.Run("peer pid outside the pane tree is refused before any write", func(t *testing.T) {
		cases := map[string]map[int]bool{
			"listener is not in the target pane": {os.Getpid() + 1: true},
			"empty pane tree":                    {},
		}
		for name, pids := range cases {
			t.Run(name, func(t *testing.T) {
				f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
				// A non-nil empty map: nil would be defaulted to the in-tree case by wakePi.
				_, err := SendPiWake(piWakeContext(t), PiWakeTarget{Path: f.path},
					piWakeNonce, "wake", piWakeIDs, PiWakeOptions{PanePIDs: pids})
				if got := piReasonOf(t, err); got != "peer_not_in_pane_tree" {
					t.Errorf("reason = %q, want peer_not_in_pane_tree", got)
				}
				f.assertNothingWritten(t)
			})
		}
		t.Run("nil pane tree", func(t *testing.T) {
			f := newFakePi(t, nil)
			_, err := SendPiWake(piWakeContext(t), PiWakeTarget{Path: f.path},
				piWakeNonce, "wake", piWakeIDs, PiWakeOptions{})
			if got := piReasonOf(t, err); got != "peer_not_in_pane_tree" {
				t.Errorf("reason = %q, want peer_not_in_pane_tree", got)
			}
			f.assertNothingWritten(t)
		})
	})
}

func TestPiWakeSocket_DialTimeoutIsUnavailableBeforeWrite(t *testing.T) {
	// A real, vetted socket: SendPiWake vets first, so only the dial can fail.
	f := newFakePi(t, nil)
	path := f.path
	var asked []time.Duration
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		asked = append(asked, timeout)
		return nil, &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}
	}

	_, err := wakePi(t, path, "wake", PiWakeOptions{})
	if err == nil {
		t.Fatal("a dial that timed out reported success")
	}
	_ = piReasonOf(t, err) // pre-write, so the caller may type the line
	f.assertNothingWritten(t)
	if len(asked) != 1 {
		t.Fatalf("dialed %d times, want exactly once", len(asked))
	}
	if asked[0] != time.Second {
		t.Errorf("default dial timeout = %v, want 1s (design G)", asked[0])
	}

	asked = nil
	_, _ = wakePi(t, path, "wake", PiWakeOptions{DialTimeout: 150 * time.Millisecond})
	if len(asked) != 1 || asked[0] != 150*time.Millisecond {
		t.Errorf("dial timeouts asked = %v, want [150ms] when PiWakeOptions.DialTimeout is set", asked)
	}
}

func TestPiWakeSocket_WritesOneFramedLineAndNothingElse(t *testing.T) {
	// Newlines, quotes and non-ASCII must survive inside one NDJSON line.
	line := "agent-deck: 2 new\n\"quoted\"\té /not-a-command"
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	if _, err := wakePi(t, f.path, line, PiWakeOptions{}); err != nil {
		t.Fatalf("SendPiWake: %v", err)
	}
	data := f.connection(t)
	if !strings.HasSuffix(string(data), "\n") || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("wire bytes %q, want exactly one newline-terminated line", data)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("frame %q is not JSON: %v", data, err)
	}
	if len(frame) != 4 || frame["v"] != float64(1) || frame["id"] != piWakeNonce || frame["line"] != line {
		t.Errorf("frame = %v, want exactly {v:1, id:%q, line:%q, ids}", frame, piWakeNonce, line)
	}
	gotIDs, _ := frame["ids"].([]any)
	if len(gotIDs) != len(piWakeIDs) {
		t.Fatalf("frame ids = %v, want %v", frame["ids"], piWakeIDs)
	}
	for i, id := range piWakeIDs {
		if gotIDs[i] != id {
			t.Errorf("frame ids[%d] = %v, want %q", i, gotIDs[i], id)
		}
	}
}

func TestPiWakeSocket_MarkupLineAtTheCapIsWrittenUnescaped(t *testing.T) {
	// A 900-byte line (the extension's line cap) of <, > and & is a normal
	// wake. HTML-escaped, each byte is six (\u003c), so the frame would be
	// about 5.4 KB, refused too_large, and the records would never be rung.
	line := strings.Repeat("<>&", 300)
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	res, err := wakePi(t, f.path, line, PiWakeOptions{})
	if err != nil || res.State != PiWakeSubmitted {
		t.Fatalf("a 900-byte markup line gave (%q, %v), want submitted", res.State, err)
	}
	data := f.connection(t)
	if len(data) > piWakeFrameCap || !strings.Contains(string(data), line) {
		t.Fatalf("wire frame is %d bytes and does not carry the line verbatim; want <= %d bytes with <, > and & unescaped", len(data), piWakeFrameCap)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil || frame["line"] != line {
		t.Fatalf("frame does not decode to the line: %v", err)
	}
}

func TestPiWakeSocket_MapsEachReplyToItsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		want        PiWakeState
	}{
		{"submitted", `"state":"submitted"`, PiWakeSubmitted},
		{"queued", `"state":"queued"`, PiWakeQueued},
		{"submitted with ok:true", `"ok":true,"state":"submitted"`, PiWakeSubmitted},
		{"queued with ok:true", `"ok":true,"state":"queued"`, PiWakeQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`",`+tc.reply+`}`))
			res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
			if err != nil {
				t.Fatalf("SendPiWake: %v", err)
			}
			if res.State != tc.want {
				t.Errorf("State = %q, want %q", res.State, tc.want)
			}
		})
	}
}

func TestPiWakeSocket_WideModeSocketIsUnavailableAndNothingWritten(t *testing.T) {
	// One case: SendPiWake vets internally, so a caller cannot skip the check.
	// The other vet refusals are pinned on VetPiSocket above.
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	if err := os.Chmod(f.path, 0o770); err != nil {
		t.Fatal(err)
	}
	_, err := wakePi(t, f.path, "wake", PiWakeOptions{})
	if got := piReasonOf(t, err); got != "socket_perms" {
		t.Errorf("reason = %q, want socket_perms", got)
	}
	f.assertNothingWritten(t)
}

func TestPiWakeSocket_NoReplyIsPushedNeverRetyped(t *testing.T) {
	cases := map[string]func(net.Conn, []byte){
		"silent extension":         nil,
		"reply that is not JSON":   func(conn net.Conn, _ []byte) { _, _ = conn.Write([]byte("whatever\n")) },
		"reply without a state":    replyWith(`{"id":"` + piWakeNonce + `"}`),
		"reply with unknown state": replyWith(`{"id":"` + piWakeNonce + `","state":"teleported"}`),
	}
	for name, respond := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakePi(t, respond)
			start := time.Now()
			res, err := wakePi(t, f.path, "wake", PiWakeOptions{ReplyWindow: 250 * time.Millisecond})
			if err != nil {
				// The caller types on Unavailable and hard-fails on Committed;
				// a written frame with no usable reply is neither (design B, J).
				assertNeitherCommittedNorUnavailable(t, err)
				t.Fatalf("error = %v, want a pushed result with a nil error", err)
			}
			if res.State != PiWakePushed {
				t.Errorf("State = %q, want %q: the daemon's own silence is never a promotion", res.State, PiWakePushed)
			}
			if time.Since(start) > 5*time.Second {
				t.Errorf("took %v, the reply window is 250ms", time.Since(start))
			}
			if data := f.connection(t); strings.Count(string(data), "\n") != 1 {
				t.Errorf("wire bytes %q, want the one frame and never a second", data)
			}
			select {
			case extra := <-f.conns:
				t.Errorf("a second connection carried %q: a pushed wake is never re-sent", extra)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestPiWakeSocket_ReplyWindowDefaultsToTwoSeconds(t *testing.T) {
	f := newFakePi(t, nil)
	start := time.Now()
	res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
	if err != nil {
		t.Fatalf("SendPiWake: %v", err)
	}
	elapsed := time.Since(start)
	if res.State != PiWakePushed {
		t.Errorf("State = %q, want %q", res.State, PiWakePushed)
	}
	if elapsed < 1500*time.Millisecond || elapsed > 4*time.Second {
		t.Errorf("waited %v for a reply, want about 2s (design G)", elapsed)
	}
}

func TestPiWakeSocket_ReplyWithOtherNonceIsIgnored(t *testing.T) {
	t.Run("only another nonce replies: still pushed", func(t *testing.T) {
		for _, state := range []string{"submitted", "queued"} {
			f := newFakePi(t, replyWith(`{"id":"01J0SOMEBODYELSE0000000000","state":"`+state+`"}`))
			res, err := wakePi(t, f.path, "wake", PiWakeOptions{ReplyWindow: 250 * time.Millisecond})
			if err != nil {
				t.Fatalf("%s: SendPiWake: %v", state, err)
			}
			if res.State != PiWakePushed {
				t.Errorf("a %q reply for another nonce produced State %q, want %q", state, res.State, PiWakePushed)
			}
		}
	})

	t.Run("another nonce's refusal does not refuse ours", func(t *testing.T) {
		f := newFakePi(t, replyWith(`{"id":"01J0SOMEBODYELSE0000000000","ok":false,"state":"busy","notSent":true}`))
		res, err := wakePi(t, f.path, "wake", PiWakeOptions{ReplyWindow: 250 * time.Millisecond})
		if err != nil {
			t.Fatalf("SendPiWake: %v, want pushed", err)
		}
		if res.State != PiWakePushed {
			t.Errorf("State = %q, want %q", res.State, PiWakePushed)
		}
	})

	t.Run("a stray reply is skipped and ours still counts", func(t *testing.T) {
		f := newFakePi(t, func(conn net.Conn, _ []byte) {
			_, _ = conn.Write([]byte(`{"id":"01J0SOMEBODYELSE0000000000","state":"queued"}` + "\n"))
			_, _ = conn.Write([]byte(`{"id":"` + piWakeNonce + `","state":"submitted"}` + "\n"))
		})
		res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
		if err != nil {
			t.Fatalf("SendPiWake: %v", err)
		}
		if res.State != PiWakeSubmitted {
			t.Errorf("State = %q, want %q", res.State, PiWakeSubmitted)
		}
	})
}

func TestPiWakeSocket_RefusedReplyIsNeitherUnavailableNorCommitted(t *testing.T) {
	for _, reason := range []string{"busy", "duplicate", "bad_request", "too_large"} {
		t.Run(reason, func(t *testing.T) {
			f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","ok":false,"state":"`+reason+`","notSent":true}`))
			_, err := wakePi(t, f.path, "wake", PiWakeOptions{})
			if err == nil {
				t.Fatal("a refusal reported success")
			}
			assertNeitherCommittedNorUnavailable(t, err)
			var refused *PiWakeRefused
			if !errors.As(err, &refused) {
				t.Fatalf("error = %v (%T), want *PiWakeRefused", err, err)
			}
			if refused.Reason != reason {
				t.Errorf("Reason = %q, want %q", refused.Reason, reason)
			}
			if data := f.connection(t); strings.Count(string(data), "\n") != 1 {
				t.Errorf("wire bytes %q: a refused wake is never re-sent", data)
			}
		})
	}
}

func TestPiWakeSocket_NotOkWithoutNotSentIsPushed(t *testing.T) {
	// Only notSent:true says the extension kept the wake from pi; ok:false alone
	// may have been sent, so it is never a refusal and never retyped.
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","ok":false,"state":"busy"}`))
	res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
	if err != nil {
		t.Fatalf("SendPiWake: %v, want pushed with a nil error", err)
	}
	if res.State != PiWakePushed {
		t.Errorf("State = %q, want %q", res.State, PiWakePushed)
	}
}

func TestPiWakeSocket_OverCapFrameNeverWritten(t *testing.T) {
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	// Control: a line at the upstream cap (900 B) is written and answered, so
	// the refusals below are the cap and not a client that never sends.
	if res, err := wakePi(t, f.path, strings.Repeat("x", 900), PiWakeOptions{}); err != nil || res.State != PiWakeSubmitted {
		t.Fatalf("control: a 900-byte line gave (%q, %v), want submitted", res.State, err)
	}
	if data := f.connection(t); len(data) == 0 {
		t.Fatal("control: nothing reached the extension")
	}

	// The frame cap is 4 KiB, well below this line.
	// Refused, not Unavailable: Unavailable would type the oversize line.
	assertTooLarge := func(line string) {
		t.Helper()
		_, err := wakePi(t, f.path, line, PiWakeOptions{})
		if err == nil {
			t.Fatal("an over-cap frame was reported as sent")
		}
		assertNeitherCommittedNorUnavailable(t, err)
		var refused *PiWakeRefused
		if !errors.As(err, &refused) || refused.Reason != "too_large" {
			t.Fatalf("error = %v (%T), want *PiWakeRefused{Reason: too_large}", err, err)
		}
		f.assertNothingWritten(t)
	}
	assertTooLarge(strings.Repeat("x", 5000))
	// The cap counts the encoded frame: escapes grow a line past it.
	assertTooLarge(strings.Repeat("\n", 3000))
}

func TestVetPiSocket_RefusesASymlinkToAPrivateSocket(t *testing.T) {
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	link := filepath.Join(f.dir, "link.sock")
	if err := os.Symlink(f.path, link); err != nil {
		t.Fatal(err)
	}
	dialed := 0
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		dialed++
		return orig(path, timeout)
	}

	// The target is private and ours; the link is only a name someone else
	// could have placed, so it is not vetted by following it.
	if got := piReasonOf(t, VetPiSocket(link, os.Getuid())); got != "socket_perms" {
		t.Errorf("VetPiSocket(symlink) reason = %q, want socket_perms", got)
	}
	_, err := wakePi(t, link, "wake", PiWakeOptions{})
	if got := piReasonOf(t, err); got != "socket_perms" {
		t.Errorf("SendPiWake(symlink) reason = %q, want socket_perms", got)
	}
	if dialed != 0 {
		t.Errorf("dialed %d times through a symlink", dialed)
	}
	f.assertNothingWritten(t)
}

func TestPiWakeSocket_EmptyIdsIsRefusedBeforeDial(t *testing.T) {
	// A frame with no ids is a certain bad_request at the extension (it cannot
	// deduplicate it), so the client refuses it before spending a dial on it.
	for name, ids := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
			dialed := 0
			orig := dialUnix
			t.Cleanup(func() { dialUnix = orig })
			dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
				dialed++
				return orig(path, timeout)
			}
			_, err := SendPiWake(piWakeContext(t), PiWakeTarget{Path: f.path}, piWakeNonce, "wake", ids,
				PiWakeOptions{PanePIDs: map[int]bool{os.Getpid(): true}})
			var refused *PiWakeRefused
			if !errors.As(err, &refused) || refused.Reason != "bad_request" {
				t.Fatalf("error = %v (%T), want *PiWakeRefused{Reason: bad_request}", err, err)
			}
			assertNeitherCommittedNorUnavailable(t, err)
			if dialed != 0 {
				t.Errorf("dialed %d times for a frame that cannot succeed", dialed)
			}
			f.assertNothingWritten(t)
		})
	}
}

// countDials counts calls to the dial seam while still dialing for real.
func countDials(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		n++
		return orig(path, timeout)
	}
	return &n
}

// afterWriteOf reads PiWakeRefused.AfterWrite by reflection, so a missing field
// fails this test and not the build of the whole package.
func afterWriteOf(t *testing.T, refused *PiWakeRefused) bool {
	t.Helper()
	f := reflect.ValueOf(*refused).FieldByName("AfterWrite")
	if !f.IsValid() || f.Kind() != reflect.Bool {
		t.Fatal("PiWakeRefused has no bool AfterWrite field: a caller cannot tell a refusal after the write (never typed) from one before it")
	}
	return f.Bool()
}

func sendBad(t *testing.T, ctx context.Context, path, nonce, line string, ids []string) error {
	t.Helper()
	_, err := SendPiWake(ctx, PiWakeTarget{Path: path}, nonce, line, ids,
		PiWakeOptions{PanePIDs: map[int]bool{os.Getpid(): true}})
	return err
}

// Input the extension would refuse is refused locally, before any I/O: no
// vetting-then-dialing for a frame that cannot succeed, and no post-write
// bad_request for the caller to misread.
func TestPiWakeSocket_BadInputIsRefusedBeforeAnyIO(t *testing.T) {
	cases := []struct {
		name, nonce, line string
		ids               []string
	}{
		{"empty nonce", "", "wake", piWakeIDs},
		{"a nonce that is not a ULID", "not-a-ulid", "wake", piWakeIDs},
		{"a nonce one character short", piWakeNonce[:25], "wake", piWakeIDs},
		{"a nonce one character long", piWakeNonce + "A", "wake", piWakeIDs},
		{"a nonce with punctuation", piWakeNonce[:25] + "!", "wake", piWakeIDs},
		{"empty line", piWakeNonce, "", piWakeIDs},
		{"an empty id alone", piWakeNonce, "wake", []string{""}},
		{"an empty id among others", piWakeNonce, "wake", []string{"01J0RECORD00000000000ABC123", ""}},
		{"nil ids", piWakeNonce, "wake", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePi(t, replyWith(`{"id":"`+tc.nonce+`","state":"submitted"}`))
			dials := countDials(t)
			err := sendBad(t, piWakeContext(t), f.path, tc.nonce, tc.line, tc.ids)
			var refused *PiWakeRefused
			if !errors.As(err, &refused) || refused.Reason != "bad_request" {
				t.Fatalf("error = %v (%T), want *PiWakeRefused{Reason: bad_request}", err, err)
			}
			if afterWriteOf(t, refused) {
				t.Error("AfterWrite = true for a refusal made before any write")
			}
			assertNeitherCommittedNorUnavailable(t, err)
			if *dials != 0 {
				t.Errorf("dialed %d times for input that cannot succeed", *dials)
			}
			f.assertNothingWritten(t)
		})
	}
}

// A reply without our id never settles the request, and a nonce reused by a
// later request cannot be settled by anything but a reply on that request's own
// connection.
func TestPiWakeSocket_ReplyWithoutAnIdNeverSettles(t *testing.T) {
	for name, reply := range map[string]string{
		"no id":    `{"state":"submitted"}`,
		"empty id": `{"id":"","state":"submitted"}`,
		"null id":  `{"id":null,"state":"queued"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakePi(t, replyWith(reply))
			res, err := wakePi(t, f.path, "wake", PiWakeOptions{ReplyWindow: 250 * time.Millisecond})
			if err != nil || res.State != PiWakePushed {
				t.Errorf("got (%q, %v), want (pushed, nil): a reply without our id is not ours", res.State, err)
			}
		})
	}
}

func TestPiWakeSocket_AReusedNonceIsSettledOnlyByItsOwnConnection(t *testing.T) {
	var served int
	f := newFakePi(t, func(conn net.Conn, _ []byte) {
		served++
		if served == 1 { // the first request is answered; the second connection stays silent
			_, _ = conn.Write([]byte(`{"id":"` + piWakeNonce + `","state":"submitted"}` + "\n"))
		}
	})
	first, err := wakePi(t, f.path, "wake", PiWakeOptions{})
	if err != nil || first.State != PiWakeSubmitted {
		t.Fatalf("first request = (%q, %v), want submitted", first.State, err)
	}
	second, err := wakePi(t, f.path, "wake", PiWakeOptions{ReplyWindow: 250 * time.Millisecond})
	if err != nil || second.State != PiWakePushed {
		t.Errorf("second request with the same nonce = (%q, %v), want pushed: the first request's reply cannot settle it", second.State, err)
	}
}

// The context is checked first, at entry: a cancelled context is context_done
// whatever else is wrong with the call.
func TestPiWakeSocket_CancelledContextIsContextDoneBeforeAnythingElse(t *testing.T) {
	f := newFakePi(t, nil)
	missing := filepath.Join(f.dir, "none.sock")
	cases := []struct {
		name, path, nonce, line string
		ids                     []string
	}{
		{"empty nonce", f.path, "", "wake", piWakeIDs},
		{"empty ids", f.path, piWakeNonce, "wake", nil},
		{"empty line", f.path, piWakeNonce, "", piWakeIDs},
		{"an oversize line", f.path, piWakeNonce, strings.Repeat("x", 5000), piWakeIDs},
		{"a socket that does not exist", missing, piWakeNonce, "wake", piWakeIDs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			dials := countDials(t)
			err := sendBad(t, ctx, tc.path, tc.nonce, tc.line, tc.ids)
			if got := piReasonOf(t, err); got != string(ReasonContextDone) {
				t.Errorf("reason = %q, want %q", got, ReasonContextDone)
			}
			if *dials != 0 {
				t.Errorf("dialed %d times with a cancelled context", *dials)
			}
		})
	}
}

func TestPiWakeSocket_CancelDuringADialThatFailsIsContextDone(t *testing.T) {
	f := newFakePi(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(string, time.Duration) (net.Conn, error) {
		cancel() // the cancel lands while the dial is in flight, which then fails
		return nil, errors.New("connection refused")
	}
	err := sendBad(t, ctx, f.path, piWakeNonce, "wake", piWakeIDs)
	if got := piReasonOf(t, err); got != string(ReasonContextDone) {
		t.Errorf("reason = %q, want %q: the dial failed because the caller gave up", got, ReasonContextDone)
	}
}

// A frame is written whole or the send is committed: a short write with no
// error, and a partial write followed by an error, are both *CommittedError,
// never a nil error and never Unavailable. The seam is the package var
// writeFrame(conn, frame) (n, err); peerPID needs the real *net.UnixConn, so
// the conn itself cannot be wrapped.
func TestPiWakeSocket_ShortAndPartialWritesAreCommitted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"short write with a nil error", nil},
		{"partial write then an error", errors.New("boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
			var sent int
			orig := writeFrame
			t.Cleanup(func() { writeFrame = orig })
			writeFrame = func(conn net.Conn, frame []byte) (int, error) {
				sent = len(frame) / 2
				n, err := conn.Write(frame[:sent])
				if err != nil {
					return n, err
				}
				return n, tc.err
			}
			res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
			var committed *CommittedError
			if !errors.As(err, &committed) {
				t.Fatalf("error = %v (%T), want *CommittedError (result %+v)", err, err, res)
			}
			assertNeitherUnavailable(t, err)
			if res != (PiWakeResult{}) {
				t.Errorf("result = %+v, want the zero value", res)
			}
			data := f.connection(t)
			if len(data) != sent || sent == 0 {
				t.Errorf("the fake holds %d bytes, want exactly the %d that were written", len(data), sent)
			}
			select {
			case extra := <-f.conns:
				t.Errorf("a second connection carried %q: a committed send is never re-sent", extra)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func assertNeitherUnavailable(t *testing.T, err error) {
	t.Helper()
	var unavail *Unavailable
	if errors.As(err, &unavail) {
		t.Errorf("error %v is *Unavailable, which the caller retypes into the pane", err)
	}
}

// A symlinked parent directory could point the wake at a socket somewhere else,
// the same hole as a symlinked leaf.
func TestVetPiSocket_RefusesASymlinkedParentDirectory(t *testing.T) {
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	linkDir := filepath.Join(piWakeShortDir(t), "linked")
	if err := os.Symlink(f.dir, linkDir); err != nil {
		t.Fatal(err)
	}
	through := filepath.Join(linkDir, filepath.Base(f.path))
	dials := countDials(t)

	if got := piReasonOf(t, VetPiSocket(through, os.Getuid())); got != "socket_perms" {
		t.Errorf("VetPiSocket reason = %q, want socket_perms", got)
	}
	err := sendBad(t, piWakeContext(t), through, piWakeNonce, "wake", piWakeIDs)
	if got := piReasonOf(t, err); got != "socket_perms" {
		t.Errorf("SendPiWake reason = %q, want socket_perms", got)
	}
	if *dials != 0 {
		t.Errorf("dialed %d times through a symlinked directory", *dials)
	}
	f.assertNothingWritten(t)
}

// A refusal after the frame was written means the peer may have seen it, so the
// caller must never type the line. PiWakeRefused.AfterWrite is the marker.
func TestPiWakeSocket_RefusalsMarkWhetherTheFrameWasWritten(t *testing.T) {
	t.Run("a notSent reply is after the write", func(t *testing.T) {
		for _, reason := range []string{"busy", "duplicate", "bad_request", "too_large"} {
			f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","ok":false,"state":"`+reason+`","notSent":true}`))
			_, err := wakePi(t, f.path, "wake", PiWakeOptions{})
			var refused *PiWakeRefused
			if !errors.As(err, &refused) {
				t.Fatalf("%s: error = %v (%T), want *PiWakeRefused", reason, err, err)
			}
			if !afterWriteOf(t, refused) {
				t.Errorf("%s: AfterWrite = false for a refusal the peer sent after reading our frame", reason)
			}
		}
	})

	t.Run("a local refusal is before the write", func(t *testing.T) {
		f := newFakePi(t, nil)
		for name, call := range map[string]func() error{
			"too_large": func() error {
				return sendBad(t, piWakeContext(t), f.path, piWakeNonce, strings.Repeat("x", 5000), piWakeIDs)
			},
			"empty ids": func() error { return sendBad(t, piWakeContext(t), f.path, piWakeNonce, "wake", nil) },
			"bad nonce": func() error { return sendBad(t, piWakeContext(t), f.path, "nope", "wake", piWakeIDs) },
		} {
			var refused *PiWakeRefused
			if err := call(); !errors.As(err, &refused) {
				t.Fatalf("%s: error = %v, want *PiWakeRefused", name, err)
			} else if afterWriteOf(t, refused) {
				t.Errorf("%s: AfterWrite = true for a refusal made before any write", name)
			}
		}
		f.assertNothingWritten(t)
	})
}

func TestPiWakeSocket_PathOver103BytesIsDisabledWithReason(t *testing.T) {
	piWakeHome(t)
	long := filepath.Join("/tmp", "pw-long", "pi", strings.Repeat("a", 120)+".sock")
	if len(long) <= 103 {
		t.Fatalf("test setup: path is %d bytes", len(long))
	}

	err := VetPiSocket(long, os.Getuid())
	if got := piReasonOf(t, err); got != "socket_path_too_long" {
		t.Errorf("VetPiSocket reason = %q, want socket_path_too_long", got)
	}
	if err != nil && !strings.Contains(err.Error(), "103") {
		t.Errorf("VetPiSocket says %q; a status line needs the limit in the reason", err)
	}

	dialed := 0
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(string, time.Duration) (net.Conn, error) {
		dialed++
		return nil, errors.New("must not dial")
	}
	_, err = wakePi(t, long, "wake", PiWakeOptions{})
	if got := piReasonOf(t, err); got != "socket_path_too_long" {
		t.Errorf("SendPiWake reason = %q, want socket_path_too_long", got)
	}
	if dialed != 0 {
		t.Errorf("dialed %d times for a path the kernel cannot hold", dialed)
	}

	exact := "/tmp/" + strings.Repeat("b", 103-len("/tmp/")-len(".sock")) + ".sock"
	if len(exact) != 103 {
		t.Fatalf("test setup: boundary path is %d bytes", len(exact))
	}
	if err := VetPiSocket(exact, os.Getuid()); err != nil {
		if got := piReasonOf(t, err); got == "socket_path_too_long" {
			t.Errorf("a 103-byte path is within the limit but was refused as too long")
		}
	}
}

// dialedConn dials the fake for real, so the peer-pid check still sees a
// *net.UnixConn, and hands it to SendPiWake through the dialUnix seam.
func dialedConn(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(string, time.Duration) (net.Conn, error) { return c, nil }
	return c.(*net.UnixConn)
}

// sendWithin runs SendPiWake and fails the test if it does not return within
// limit (a gate that is missing shows up as a hang, not a wrong value).
func sendWithin(t *testing.T, ctx context.Context, limit time.Duration, path string, opts PiWakeOptions) (PiWakeResult, time.Duration, error) {
	t.Helper()
	if opts.PanePIDs == nil {
		opts.PanePIDs = map[int]bool{os.Getpid(): true}
	}
	type out struct {
		res PiWakeResult
		err error
	}
	done := make(chan out, 1)
	start := time.Now()
	go func() {
		res, err := SendPiWake(ctx, PiWakeTarget{Path: path}, piWakeNonce, "wake", piWakeIDs, opts)
		done <- out{res, err}
	}()
	select {
	case o := <-done:
		return o.res, time.Since(start), o.err
	case <-time.After(limit):
		t.Fatalf("SendPiWake still running after %v", limit)
		return PiWakeResult{}, 0, nil
	}
}

func TestPiWakeSocket_WriteFailureAfterDialIsCommittedNeverUnavailable(t *testing.T) {
	f := newFakePi(t, nil)
	c := dialedConn(t, f.path)
	// Shut the write side after the dial: the frame write then fails, which
	// is after the send is committed and must never be typed (double delivery).
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	res, err := wakePi(t, f.path, "wake", PiWakeOptions{})
	var committed *CommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("error = %v (%T), want *CommittedError", err, err)
	}
	var unavail *Unavailable
	if errors.As(err, &unavail) {
		t.Errorf("error %v is *Unavailable: the caller would retype a line that may be on the wire", err)
	}
	if res != (PiWakeResult{}) {
		t.Errorf("result = %+v, want the zero value on a committed failure", res)
	}
}

func TestPiWakeSocket_WriteDeadlineBindsAndIsCommitted(t *testing.T) {
	// A listener that never reads, and a connection whose send buffer is
	// already full: the frame write blocks until SendPiWake's write deadline.
	_, path, ln := listenPrivately(t)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Held open and never read; closed with the goroutine, not via t.Cleanup.
			defer func() { _ = c.Close() }()
		}
	}()
	c := dialedConn(t, path)
	chunk := make([]byte, 64*1024)
	for {
		_ = c.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := c.Write(chunk); err != nil {
			break
		}
	}
	_ = c.SetWriteDeadline(time.Time{})

	res, elapsed, err := sendWithin(t, piWakeContext(t), 10*time.Second, path, PiWakeOptions{})
	var committed *CommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("error = %v (%T), want *CommittedError from the expired write deadline", err, err)
	}
	if res != (PiWakeResult{}) {
		t.Errorf("result = %+v, want the zero value", res)
	}
	if elapsed > 6*time.Second {
		t.Errorf("the write took %v to give up, want a bounded write deadline", elapsed)
	}
}

func TestPiWakeSocket_ContextDeadlineEndsTheWaitWithPushed(t *testing.T) {
	f := newFakePi(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, elapsed, err := sendWithin(t, ctx, 10*time.Second, f.path, PiWakeOptions{ReplyWindow: 5 * time.Second})
	if err != nil || res.State != PiWakePushed {
		t.Fatalf("got (%q, %v), want (pushed, nil)", res.State, err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("took %v, want about the context's 100ms and not the 5s window", elapsed)
	}
}

func TestPiWakeSocket_CancelAfterTheFrameIsReadReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel from inside the fake, after it has read the frame: no timer.
	f := newFakePi(t, func(net.Conn, []byte) { cancel() })
	res, elapsed, err := sendWithin(t, ctx, 10*time.Second, f.path, PiWakeOptions{ReplyWindow: 5 * time.Second})
	if elapsed > 1500*time.Millisecond {
		t.Errorf("took %v after the context was cancelled, want a prompt return", elapsed)
	}
	// The frame was written, so the outcome is unknown: the same class as no
	// reply, pushed with a nil error, never Unavailable (retype) or Committed.
	if err != nil || res.State != PiWakePushed {
		t.Errorf("got (%q, %v), want (pushed, nil)", res.State, err)
	}
	if data := f.connection(t); strings.Count(string(data), "\n") != 1 {
		t.Errorf("wire bytes %q, want the one frame written before the cancel", data)
	}
}

func TestPiWakeSocket_ContextCancelledBeforeDialIsContextDoneNothingWritten(t *testing.T) {
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := sendWithin(t, ctx, 10*time.Second, f.path, PiWakeOptions{})
	if got := piReasonOf(t, err); got != string(ReasonContextDone) { // pre-write, so the caller may type the line
		t.Errorf("reason = %q, want %q: a cancel is not a dial failure", got, ReasonContextDone)
	}
	f.assertNothingWritten(t)
}

func TestPiWakeSocket_CancelDuringThePeerCheckIsContextDoneNotPeerOutsideTheTree(t *testing.T) {
	f := newFakePi(t, replyWith(`{"id":"`+piWakeNonce+`","state":"submitted"}`))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orig := dialUnix
	t.Cleanup(func() { dialUnix = orig })
	dialUnix = func(path string, timeout time.Duration) (net.Conn, error) {
		c, err := orig(path, timeout)
		cancel() // after the dial, before the peer check
		return c, err
	}
	_, _, err := sendWithin(t, ctx, 10*time.Second, f.path, PiWakeOptions{})
	if got := piReasonOf(t, err); got != string(ReasonContextDone) {
		t.Errorf("reason = %q, want %q: the listener is in the pane tree, the cancel closed the conn", got, ReasonContextDone)
	}
	f.assertNothingWritten(t)
}

func TestPiWakeSocket_ReplyStreamIsCappedNotWaitedOut(t *testing.T) {
	// Well over the 64 KiB reply cap of lines that are not ours, then silence.
	stray := []byte(`{"id":"01J0SOMEBODYELSE0000000000","state":"queued"}` + "\n")
	f := newFakePi(t, func(conn net.Conn, _ []byte) {
		for sent := 0; sent < 300*1024; sent += len(stray) {
			if _, err := conn.Write(stray); err != nil {
				return
			}
		}
	})
	res, elapsed, err := sendWithin(t, piWakeContext(t), 15*time.Second, f.path, PiWakeOptions{ReplyWindow: 5 * time.Second})
	if err != nil || res.State != PiWakePushed {
		t.Fatalf("got (%q, %v), want (pushed, nil)", res.State, err)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("took %v: the reply cap should end the read long before the 5s window", elapsed)
	}
}
