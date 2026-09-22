package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

type echoIn struct {
	Profile string `json:"profile"`
	Text    string `json:"text"`
}

type echoOut struct {
	Profile string `json:"profile"`
	Text    string `json:"text"`
}

func testRegistry(t *testing.T) *core.Registry {
	t.Helper()
	reg := core.NewRegistry()
	echo := core.Typed(func(ctx context.Context, in echoIn) (echoOut, error) {
		if in.Text == "warn" {
			core.Warn(ctx, "careful")
		}
		if in.Text == "missing" {
			return echoOut{}, core.Errorf(core.CodeNotFound, "no such thing")
		}
		return echoOut(in), nil
	})
	reg.MustRegister(core.Def{ID: "test.echo", CLI: []string{"echo"}, Class: core.Query, Remote: core.RemoteAllow, In: echoIn{}, Out: echoOut{}, Exec: echo})
	reg.MustRegister(core.Def{ID: "test.local", Class: core.Mutate, Remote: core.RemoteDeny, In: echoIn{}, Out: echoOut{}, Exec: echo})
	return reg
}

// shortDir returns a temp dir under /tmp: unix socket paths are limited to
// about 104 bytes and the default TMPDIR on macOS is already long.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "addm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type testServer struct {
	paths Paths
	srv   *Server
	done  chan error
}

func startServer(t *testing.T, opts Options) *testServer {
	t.Helper()
	if opts.Registry == nil {
		opts.Registry = testRegistry(t)
	}
	if opts.Profile == "" {
		opts.Profile = "p1"
	}
	if opts.OwnerUID == 0 {
		opts.OwnerUID = os.Getuid()
	}
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	owner, err := Acquire(paths)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	opts.Socket = paths.Socket
	srv := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	ts := &testServer{paths: paths, srv: srv, done: make(chan error, 1)}
	go func() { ts.done <- srv.Serve(ctx, owner.Listener()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ts.done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancel")
		}
		_ = owner.Close()
	})
	return ts
}

// rawConn is a hand-driven protocol connection for the negative tests.
type rawConn struct {
	t  *testing.T
	c  net.Conn
	sc *bufio.Scanner
}

func dialRaw(t *testing.T, socket string) *rawConn {
	t.Helper()
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 4*MaxFrameBytes)
	return &rawConn{t: t, c: c, sc: sc}
}

func (r *rawConn) send(line string) {
	r.t.Helper()
	if _, err := r.c.Write([]byte(line + "\n")); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

// recv returns the next frame, or ok=false when the server closed the
// connection.
func (r *rawConn) recv() (Frame, bool) {
	r.t.Helper()
	if !r.sc.Scan() {
		return Frame{}, false
	}
	var f Frame
	if err := json.Unmarshal(r.sc.Bytes(), &f); err != nil {
		r.t.Fatalf("bad frame %q: %v", r.sc.Text(), err)
	}
	return f, true
}

func (r *rawConn) hello() Frame {
	r.t.Helper()
	f, ok := r.recv()
	if !ok || f.Type != TypeHello || len(f.Token) != 32 {
		r.t.Fatalf("hello = %+v (ok=%v)", f, ok)
	}
	return f
}

func (r *rawConn) expectClosed() {
	r.t.Helper()
	if f, ok := r.recv(); ok {
		r.t.Fatalf("connection still open, got %+v", f)
	}
}

func TestSocketIsOwnerOnly(t *testing.T) {
	ts := startServer(t, Options{})
	st, err := os.Stat(ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want socket 0600", st.Mode())
	}
	dir, err := os.Stat(ts.paths.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir mode = %v, want 0700", dir.Mode().Perm())
	}
}

func TestPeerUIDOfLocalConnectionIsCaller(t *testing.T) {
	dir := shortDir(t)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := net.Dial("unix", filepath.Join(dir, "s"))
		if err == nil {
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, err := peerUID(c.(*net.UnixConn))
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if uid != os.Getuid() {
		t.Fatalf("peer uid = %d, want %d", uid, os.Getuid())
	}
}

func TestNonOwnerUIDIsRejectedBeforeHello(t *testing.T) {
	ts := startServer(t, Options{OwnerUID: os.Getuid() + 1})
	r := dialRaw(t, ts.paths.Socket)
	f, ok := r.recv()
	if !ok || f.Type != TypeError || f.Error == nil || f.Error.Code != CodePeerRejected || f.Token != "" {
		t.Fatalf("first frame = %+v (ok=%v), want PEER_REJECTED without a token", f, ok)
	}
	r.expectClosed()
}

func TestTokenRequiredOnEveryFrame(t *testing.T) {
	ts := startServer(t, Options{})

	cases := map[string]func(token string) string{
		"missing": func(string) string { return `{"v":1,"type":"status","id":"1"}` },
		"wrong": func(string) string {
			return `{"v":1,"type":"status","id":"1","token":"` + strings.Repeat("0", 32) + `"}`
		},
		"other connection's": func(string) string {
			other := dialRaw(t, ts.paths.Socket).hello()
			return `{"v":1,"type":"status","id":"1","token":"` + other.Token + `"}`
		},
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			r := dialRaw(t, ts.paths.Socket)
			h := r.hello()
			r.send(frame(h.Token))
			f, ok := r.recv()
			if !ok || f.Type != TypeError || f.Error == nil || f.Error.Code != CodeAuthFailed || f.ID != "1" {
				t.Fatalf("reply = %+v (ok=%v), want AUTH_FAILED", f, ok)
			}
			r.expectClosed()
		})
	}

	t.Run("valid token then missing token", func(t *testing.T) {
		r := dialRaw(t, ts.paths.Socket)
		h := r.hello()
		r.send(`{"v":1,"type":"status","id":"1","token":"` + h.Token + `"}`)
		if f, ok := r.recv(); !ok || f.Type != TypeStatus || f.Status == nil || f.Status.Profile != "p1" {
			t.Fatalf("status reply = %+v", f)
		}
		r.send(`{"v":1,"type":"status","id":"2"}`)
		if f, ok := r.recv(); !ok || f.Error == nil || f.Error.Code != CodeAuthFailed {
			t.Fatalf("second reply = %+v, want AUTH_FAILED", f)
		}
		r.expectClosed()
	})
}

func TestProtocolErrors(t *testing.T) {
	ts := startServer(t, Options{})

	t.Run("bad json closes", func(t *testing.T) {
		r := dialRaw(t, ts.paths.Socket)
		r.hello()
		r.send(`{not json`)
		if f, ok := r.recv(); !ok || f.Error == nil || f.Error.Code != CodeBadFrame {
			t.Fatalf("reply = %+v", f)
		}
		r.expectClosed()
	})
	t.Run("oversized frame closes", func(t *testing.T) {
		r := dialRaw(t, ts.paths.Socket)
		r.hello()
		r.send(`{"v":1,"type":"call","pad":"` + strings.Repeat("x", MaxFrameBytes) + `"}`)
		if f, ok := r.recv(); !ok || f.Error == nil || f.Error.Code != CodeFrameTooLarge {
			t.Fatalf("reply = %+v", f)
		}
		r.expectClosed()
	})
	t.Run("version and type errors keep the connection", func(t *testing.T) {
		r := dialRaw(t, ts.paths.Socket)
		h := r.hello()
		r.send(`{"v":2,"type":"status","id":"a","token":"` + h.Token + `"}`)
		if f, ok := r.recv(); !ok || f.Error == nil || f.Error.Code != CodeUnsupportedVersion || f.ID != "a" {
			t.Fatalf("reply = %+v", f)
		}
		r.send(`{"v":1,"type":"bogus","id":"b","token":"` + h.Token + `"}`)
		if f, ok := r.recv(); !ok || f.Error == nil || f.Error.Code != CodeUnknownType || f.ID != "b" {
			t.Fatalf("reply = %+v", f)
		}
		r.send(`{"v":1,"type":"status","id":"c","token":"` + h.Token + `"}`)
		if f, ok := r.recv(); !ok || f.Type != TypeStatus || f.ID != "c" {
			t.Fatalf("reply = %+v", f)
		}
	})
}

func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCallReturnsTheRegistryEnvelope(t *testing.T) {
	reg := testRegistry(t)
	ts := startServer(t, Options{Registry: reg})
	c, err := Dial(context.Background(), ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	cases := []struct {
		name  string
		cmd   string
		input string
		want  echoIn
	}{
		{"ok with bound profile", "test.echo", `{"text":"hi"}`, echoIn{Profile: "p1", Text: "hi"}},
		{"explicit same profile", "test.echo", `{"profile":"p1","text":"hi"}`, echoIn{Profile: "p1", Text: "hi"}},
		{"warning", "test.echo", `{"text":"warn"}`, echoIn{Profile: "p1", Text: "warn"}},
		{"command error", "test.echo", `{"text":"missing"}`, echoIn{Profile: "p1", Text: "missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Call(tc.cmd, json.RawMessage(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			res := reg.Run(context.Background(), tc.cmd, tc.want)
			want, _ := json.Marshal(res.Envelope("x"))
			if g, w := scrubRequestID(t, got), scrubRequestID(t, want); g != w {
				t.Fatalf("envelope over socket:\n%s\nin process:\n%s", g, w)
			}
		})
	}

	errCases := []struct{ name, cmd, input, code string }{
		{"unknown command", "no.such", `{}`, core.CodeUnknownCommand},
		{"remote denied", "test.local", `{}`, CodeRemoteDenied},
		{"unknown field", "test.echo", `{"txt":"x"}`, core.CodeInvalidInput},
		{"other profile", "test.echo", `{"profile":"p2"}`, core.CodeInvalidInput},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Call(tc.cmd, json.RawMessage(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			var env core.Envelope
			if err := json.Unmarshal(got, &env); err != nil {
				t.Fatal(err)
			}
			if env.OK || env.Error == nil || env.Error.Code != tc.code || env.Schema != core.SchemaID(tc.cmd) {
				t.Fatalf("envelope = %s, want error %s", got, tc.code)
			}
		})
	}

	st, err := c.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Calls != uint64(len(cases)+len(errCases)) || st.PID != os.Getpid() || st.Socket != ts.paths.Socket {
		t.Fatalf("status = %+v", st)
	}
}

func scrubRequestID(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	m["request_id"] = "<ID>"
	b, _ := json.Marshal(m)
	return canonical(t, b)
}

func TestCatalogListsEveryRegisteredCommand(t *testing.T) {
	reg := testRegistry(t)
	ts := startServer(t, Options{Registry: reg})
	c, err := Dial(context.Background(), ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cmds, err := c.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	defs := reg.Defs()
	if len(cmds) != len(defs) {
		t.Fatalf("catalog has %d commands, registry %d", len(cmds), len(defs))
	}
	for i, d := range defs {
		if cmds[i].ID != d.ID || cmds[i].Class != string(d.Class) || cmds[i].Remote != string(d.Remote) || strings.Join(cmds[i].CLI, " ") != d.CLIPath() {
			t.Errorf("catalog[%d] = %+v, want %s", i, cmds[i], d.ID)
		}
	}
}

func TestSingleOwnerLock(t *testing.T) {
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	first, err := Acquire(paths)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(paths)
	var running *AlreadyRunningError
	if !errors.As(err, &running) || running.PID != os.Getpid() {
		t.Fatalf("second Acquire = %v, want AlreadyRunningError{pid %d}", err, os.Getpid())
	}
	if _, statErr := os.Stat(paths.Socket); statErr != nil {
		t.Fatalf("second Acquire disturbed the owner's socket: %v", statErr)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket left behind after Close: %v", err)
	}
	again, err := Acquire(paths)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	_ = again.Close()
}

// A daemon killed with SIGKILL leaves its socket file and a lock file naming
// a dead pid. The next Acquire takes over both.
func TestStaleSocketIsRecovered(t *testing.T) {
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if err := os.WriteFile(paths.Lock, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := Probe(paths); got.State != StateStale || got.PID != 999999 {
		t.Fatalf("Probe before recovery = %+v, want stale pid 999999", got)
	}

	owner, err := Acquire(paths)
	if err != nil {
		t.Fatalf("Acquire over a stale socket: %v", err)
	}
	defer owner.Close()
	if pid := readPID(paths); pid != os.Getpid() {
		t.Fatalf("lock names pid %d, want %d", pid, os.Getpid())
	}
	srv := New(Options{Registry: testRegistry(t), Profile: "p1", OwnerUID: os.Getuid()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, owner.Listener()) }()
	if got := Probe(paths); got.State != StateRunning || got.Status.PID != os.Getpid() {
		t.Fatalf("Probe after recovery = %+v, want running", got)
	}
}

func TestAcquireRefusesANonSocketFile(t *testing.T) {
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Socket, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(paths); err == nil {
		t.Fatal("Acquire replaced a regular file at the socket path")
	}
	if b, _ := os.ReadFile(paths.Socket); string(b) != "keep me" {
		t.Fatalf("regular file changed: %q", b)
	}
}

func TestProbeAbsent(t *testing.T) {
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	if got := Probe(paths); got.State != StateAbsent {
		t.Fatalf("Probe = %+v, want absent", got)
	}
}

func TestShutdownStopsServe(t *testing.T) {
	paths := PathsIn(filepath.Join(shortDir(t), "run"))
	owner, err := Acquire(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	srv := New(Options{Registry: testRegistry(t), Profile: "p1", OwnerUID: os.Getuid()})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), owner.Listener()) }()

	c, err := Dial(context.Background(), paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve still running after shutdown")
	}
}

// TestEventsStreamResumesThroughSocket is the slice-4 durability proof run
// through the daemon: a follower reads part of the stream, is killed
// (connection dropped), reconnects with the last cursor it saw, and ends up
// with every frame exactly once.
func TestEventsStreamResumesThroughSocket(t *testing.T) {
	bus, err := events.Open(filepath.Join(t.TempDir(), "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	publish := func(n int) {
		for i := 0; i < n; i++ {
			bus.Publish("test.tick", "s1", map[string]int{"i": i})
		}
		if !bus.Flush(5 * time.Second) {
			t.Fatal("bus flush timed out")
		}
	}
	publish(50)
	ts := startServer(t, Options{Bus: bus})

	seen := map[events.Cursor]string{}
	var last events.Cursor
	read := func(c *Client, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			raw, err := c.Next()
			if err != nil {
				t.Fatalf("Next after %d frames: %v", len(seen), err)
			}
			f, err := events.ParseFrameLine(raw)
			if err != nil {
				t.Fatalf("frame %s: %v", raw, err)
			}
			if f.Cursor != last+1 {
				t.Fatalf("cursor %d after %d: gap or duplicate", f.Cursor, last)
			}
			if _, dup := seen[f.Cursor]; dup {
				t.Fatalf("duplicate cursor %d", f.Cursor)
			}
			want, _ := f.CanonicalJSON()
			if string(raw) != string(want) {
				t.Fatalf("event frame is not canonical JSON:\n%s\n%s", raw, want)
			}
			seen[f.Cursor] = f.EventID
			last = f.Cursor
		}
	}

	first, err := Dial(context.Background(), ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Subscribe(0); err != nil {
		t.Fatal(err)
	}
	read(first, 20)
	_ = first.Close() // the follower dies mid-stream

	publish(30)
	second, err := Dial(context.Background(), ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Subscribe(uint64(last)); err != nil {
		t.Fatal(err)
	}
	read(second, 60)
	if len(seen) != 80 || last != 80 {
		t.Fatalf("saw %d frames up to cursor %d, want 80", len(seen), last)
	}

	if err := second.Subscribe(0); err == nil {
		t.Fatal("second Subscribe on one connection succeeded")
	}
}

func TestSubscribeWithoutBusFails(t *testing.T) {
	ts := startServer(t, Options{})
	c, err := Dial(context.Background(), ts.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Subscribe(0)
	var fe *FrameError
	if !errors.As(err, &fe) || fe.Code != CodeEventsUnavailable {
		t.Fatalf("Subscribe = %v, want %s", err, CodeEventsUnavailable)
	}
}
