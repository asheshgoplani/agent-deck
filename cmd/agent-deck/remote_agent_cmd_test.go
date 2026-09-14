package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// The agent answers each request with its id and the command's output,
// refuses verbs that must not run over the channel, and pushes "changed"
// when the watched state file is written.
func TestRemoteAgent_RequestsEventsAndDenyList(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, "v1"); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	// The listing probe reflects the state file's content, so a write that
	// changes what the TUI would see produces exactly one "changed".
	run := func(ctx context.Context, args []string) (string, string, int) {
		for _, a := range args {
			if a == "--fail" {
				return "", "boom", 3
			}
		}
		if args[0] == "list" {
			content, _ := os.ReadFile(db)
			return "ran:" + strings.Join(args, " ") + ":" + string(content), "", 0
		}
		return "ran:" + strings.Join(args, " "), "", 0
	}
	// The probe is what the change feed compares; here it is the same
	// listing the request path returns, so the two stay in step. It is
	// wrapped in the shapes a real listing has (an array, an object) since
	// anything else counts as a failed probe and is pushed bare.
	probe := func() (string, string, error) {
		l, _, _ := run(context.Background(), []string{"list", "--json"})
		g, _, _ := run(context.Background(), []string{"group", "list", "--json"})
		lb, _ := json.Marshal([]string{l})
		gb, _ := json.Marshal(map[string]string{"groups": g})
		return string(lb), string(gb), nil
	}
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: run, Probe: probe, WatchPath: db, WatchEvery: 20 * time.Millisecond, ProbeQuiet: 30 * time.Millisecond})
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	next := func() remoteAgentReply {
		if !sc.Scan() {
			t.Fatalf("agent closed early: %v", sc.Err())
		}
		var r remoteAgentReply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad reply %q: %v", sc.Text(), err)
		}
		return r
	}
	if r := next(); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}

	send := func(id int64, args ...string) {
		b, _ := json.Marshal(remoteAgentRequest{ID: id, Args: args})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	send(7, "list", "--json")
	if r := next(); r.ID != 7 || r.Code != 0 || r.Stdout != "ran:list --json:v1" {
		t.Fatalf("list reply = %+v", r)
	}
	send(8, "status", "--fail")
	if r := next(); r.ID != 8 || r.Code != 3 || r.Stderr != "boom" {
		t.Fatalf("failing command reply = %+v", r)
	}
	send(9, "web")
	if r := next(); r.ID != 9 || r.Code != 2 || !strings.Contains(r.Error, "not allowed") {
		t.Fatalf("denied verb reply = %+v", r)
	}

	// A write that does not change the listing (same content, new mtime)
	// produces no event; a write that does produces one.
	time.Sleep(30 * time.Millisecond)
	if err := writeFileAtomic(db, "v1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	send(10, "status", "--noop")
	if r := next(); r.ID != 10 || r.Event != "" {
		t.Fatalf("a content-preserving write must not push an event; got %+v before the noop reply", r)
	}
	if err := writeFileAtomic(db, "v2-longer"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	got := make(chan remoteAgentReply, 1)
	go func() { got <- next() }()
	select {
	case r := <-got:
		if r.Event != "changed" {
			t.Fatalf("expected a changed event, got %+v", r)
		}
	case <-deadline:
		t.Fatal("no changed event after the state file was written")
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit when stdin closed")
	}
}

// The "changed" event reports how long the probe took, and a probe that
// fails still tells the TUI to refetch: an event without listings.
func TestRemoteAgent_ProbeTimingAndFailureFallback(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, "v1"); err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	run := func(context.Context, []string) (string, string, int) { return "", "", 0 }
	probe := func() (string, string, error) {
		content, _ := os.ReadFile(db)
		if string(content) == "broken" {
			return "", "", errors.New("storage gone")
		}
		time.Sleep(5 * time.Millisecond)
		return "[\"" + string(content) + "\"]", "{}", nil
	}
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: run, Probe: probe, WatchPath: db, WatchEvery: 20 * time.Millisecond, ProbeQuiet: 30 * time.Millisecond})
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	next := func() remoteAgentReply {
		t.Helper()
		got := make(chan remoteAgentReply, 1)
		go func() {
			if !sc.Scan() {
				t.Errorf("agent closed early: %v", sc.Err())
				got <- remoteAgentReply{}
				return
			}
			var r remoteAgentReply
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				t.Errorf("bad reply %q: %v", sc.Text(), err)
			}
			got <- r
		}()
		select {
		case r := <-got:
			return r
		case <-time.After(2 * time.Second):
			t.Fatal("no line from the agent within 2s")
			return remoteAgentReply{}
		}
	}
	if r := next(); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}

	time.Sleep(30 * time.Millisecond)
	if err := writeFileAtomic(db, "v2-longer"); err != nil {
		t.Fatal(err)
	}
	r := next()
	if r.Event != "changed" || r.Sessions != `["v2-longer"]` || r.Groups != "{}" {
		t.Fatalf("expected a changed event with listings, got %+v", r)
	}
	if r.ProbeMS < 5 {
		t.Fatalf("probe_ms = %d, want the probe's duration (>= 5ms)", r.ProbeMS)
	}

	// The feed re-reads the stamp after a probe to absorb the probe's own
	// writes; give it that moment so this write is not taken as seen.
	time.Sleep(30 * time.Millisecond)
	if err := writeFileAtomic(db, "broken"); err != nil {
		t.Fatal(err)
	}
	r = next()
	if r.Event != "changed" || r.Sessions != "" || r.Groups != "" {
		t.Fatalf("a failing probe must push a bare changed event, got %+v", r)
	}

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit when stdin closed")
	}
}

// A watch request starts pushing the session's pane: the first capture at
// once, then only when the text changes; a new watch replaces the old one,
// a capture failure is pushed once, and unwatch stops the pushes.
func TestRemoteAgent_WatchPushesPaneOnChange(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var mu sync.Mutex
	panes := map[string]string{"s1": "one-a", "s2": "two-a"}
	var captures atomic.Int32
	capture := func(_ context.Context, id string) (string, error) {
		captures.Add(1)
		mu.Lock()
		defer mu.Unlock()
		p, ok := panes[id]
		if !ok {
			return "", errors.New("session '" + id + "' not found")
		}
		return p, nil
	}
	setPane := func(id, text string) {
		mu.Lock()
		panes[id] = text
		mu.Unlock()
	}
	run := func(ctx context.Context, args []string) (string, string, int) { return "", "", 0 }
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: run, Capture: capture, PaneEvery: 10 * time.Millisecond})
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	replies := make(chan remoteAgentReply, 64)
	go func() {
		for sc.Scan() {
			var r remoteAgentReply
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				replies <- r
			}
		}
		close(replies)
	}()
	next := func(what string) remoteAgentReply {
		t.Helper()
		select {
		case r, ok := <-replies:
			if !ok {
				t.Fatalf("agent closed while waiting for %s", what)
			}
			return r
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for %s", what)
		}
		return remoteAgentReply{}
	}
	noneWithin := func(d time.Duration, what string) {
		t.Helper()
		select {
		case r := <-replies:
			t.Fatalf("unexpected %+v while expecting %s", r, what)
		case <-time.After(d):
		}
	}
	send := func(req remoteAgentRequest) {
		b, _ := json.Marshal(req)
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if r := next("ready"); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}

	send(remoteAgentRequest{ID: 1, Watch: "s1"})
	if r := next("watch ack"); r.ID != 1 || r.Code != 0 || r.Error != "" {
		t.Fatalf("watch ack = %+v", r)
	}
	if r := next("first pane"); r.Event != "pane" || r.Session != "s1" || r.Stdout != "one-a" {
		t.Fatalf("first pane push = %+v, want the current screen of s1", r)
	}
	// Unchanged pane: silence, however often it is captured.
	noneWithin(60*time.Millisecond, "silence on an unchanged pane")
	if captures.Load() < 3 {
		t.Fatalf("the pane must be polled while watched, got %d captures", captures.Load())
	}
	setPane("s1", "one-b")
	if r := next("changed pane"); r.Event != "pane" || r.Session != "s1" || r.Stdout != "one-b" {
		t.Fatalf("changed pane push = %+v", r)
	}

	// Asking again for the watched session restarts the watch: the current
	// screen is pushed again even though it did not change (the local side
	// re-asks after a lost ack and needs a frame to start from).
	send(remoteAgentRequest{ID: 5, Watch: "s1"})
	if r := next("rewatch ack"); r.ID != 5 || r.Code != 0 {
		t.Fatalf("rewatch ack = %+v", r)
	}
	if r := next("pane after rewatch"); r.Event != "pane" || r.Session != "s1" || r.Stdout != "one-b" {
		t.Fatalf("a repeated watch must push the current screen again, got %+v", r)
	}

	// The peer says how many lines it renders; the push keeps only those.
	setPane("s1", "l1\nl2\nl3\nl4\n")
	send(remoteAgentRequest{ID: 6, Watch: "s1", Lines: 2})
	if r := next("lines ack"); r.ID != 6 || r.Code != 0 {
		t.Fatalf("lines watch ack = %+v", r)
	}
	if r := next("trimmed pane"); r.Event != "pane" || r.Stdout != "l3\nl4\n" {
		t.Fatalf("pane push must be trimmed to the last 2 lines, got %q", r.Stdout)
	}
	// A change above the kept tail is not a change on the wire.
	setPane("s1", "L1\nl2\nl3\nl4\n")
	noneWithin(50*time.Millisecond, "silence when only trimmed lines changed")

	// Replacing the watch: s2's screen arrives, s1 changes go unnoticed.
	send(remoteAgentRequest{ID: 2, Watch: "s2"})
	if r := next("watch ack"); r.ID != 2 || r.Code != 0 {
		t.Fatalf("second watch ack = %+v", r)
	}
	if r := next("pane of s2"); r.Event != "pane" || r.Session != "s2" || r.Stdout != "two-a" {
		t.Fatalf("pane after rewatch = %+v", r)
	}
	setPane("s1", "one-c")
	noneWithin(50*time.Millisecond, "silence for the replaced session")

	// A capture failure is pushed once, as an error on the pane event.
	mu.Lock()
	delete(panes, "s2")
	mu.Unlock()
	if r := next("pane error"); r.Event != "pane" || r.Session != "s2" || !strings.Contains(r.Error, "not found") || r.Stdout != "" {
		t.Fatalf("pane failure push = %+v", r)
	}
	noneWithin(50*time.Millisecond, "one push per distinct failure")

	// Unwatch: acknowledged, then nothing more even when the pane changes.
	send(remoteAgentRequest{ID: 3, Unwatch: true})
	if r := next("unwatch ack"); r.ID != 3 || r.Code != 0 {
		t.Fatalf("unwatch ack = %+v", r)
	}
	setPane("s2", "two-b")
	noneWithin(50*time.Millisecond, "silence after unwatch")

	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit when stdin closed")
	}
}

// An agent started without a capture func (or an old build, which sees a
// request with no args) refuses the watch so the local side keeps polling.
func TestRemoteAgent_WatchRefusedWithoutCapture(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: func(context.Context, []string) (string, string, int) { return "", "", 0 }})
		_ = outW.Close()
	}()
	sc := bufio.NewScanner(outR)
	sc.Scan() // ready
	b, _ := json.Marshal(remoteAgentRequest{ID: 4, Watch: "s1"})
	_, _ = inW.Write(append(b, '\n'))
	if !sc.Scan() {
		t.Fatal("no reply")
	}
	var r remoteAgentReply
	_ = json.Unmarshal(sc.Bytes(), &r)
	if r.ID != 4 || r.Code == 0 || r.Error == "" {
		t.Fatalf("watch without capture must be refused with an error, got %+v", r)
	}
	_ = inW.Close()
}

func TestTailLines(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"a", 3, "a"},
		{"a\nb\nc", 0, "a\nb\nc"},
		{"a\nb\nc", 5, "a\nb\nc"},
		{"a\nb\nc", 2, "b\nc"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc", 1, "c"},
		{"\nb", 1, "b"},
		{"\n", 1, "\n"},
	}
	for _, c := range cases {
		if got := tailLines(c.in, c.n); got != c.want {
			t.Errorf("tailLines(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// writeFileAtomic replaces the watched file in one rename, so a probe running
// concurrently never observes a truncated, half-written file.
func writeFileAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TestRemoteAgent_RecyclesOnNewBinary pins the recycle-on-upgrade path:
// once the watcher sees a newer build the agent finishes the request it
// already accepted, answers it, and exits without a re-exec so the
// controller redials into the new binary.
func TestRemoteAgent_RecyclesOnNewBinary(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	run := func(ctx context.Context, args []string) (string, string, int) {
		if args[0] == "status" {
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-ctx.Done():
				return "", "cancelled", 1
			}
		}
		return "ran:" + strings.Join(args, " "), "", 0
	}
	var ticks atomic.Int64
	watch := &update.Watcher{
		Exe:            "/opt/agent-deck",
		RunningVersion: "1.16.0",
		Interval:       5 * time.Millisecond,
		Stat: func(string) (update.Fingerprint, error) {
			// The file changes after the request below is in flight.
			if ticks.Add(1) > 3 {
				return update.Fingerprint{ModTime: time.Unix(2, 0), Size: 2}, nil
			}
			return update.Fingerprint{ModTime: time.Unix(1, 0), Size: 1}, nil
		},
		Probe: func(string) (string, error) { return "1.16.1", nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{Run: run, BinaryWatch: watch})
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	next := func() remoteAgentReply {
		if !sc.Scan() {
			t.Fatalf("agent closed early: %v", sc.Err())
		}
		var r remoteAgentReply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad reply %q: %v", sc.Text(), err)
		}
		return r
	}
	if r := next(); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}
	b, _ := json.Marshal(remoteAgentRequest{ID: 1, Args: []string{"status"}})
	if _, err := inW.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	<-started
	// The newer build is noticed while the request runs: the agent must
	// keep serving until it is answered.
	time.Sleep(60 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("agent exited with a request in flight")
	default:
	}
	close(release)
	if r := next(); r.ID != 1 || r.Code != 0 || r.Stdout != "ran:status" {
		t.Fatalf("in-flight request must be answered before the recycle, got %+v", r)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not exit after the newer build was seen and it went idle")
	}
	_ = inW.Close()
}
