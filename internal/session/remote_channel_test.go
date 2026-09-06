package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAgentT is the test's handle on a fake remote agent.
type fakeAgentT struct {
	dial     func(context.Context) (io.WriteCloser, io.Reader, func(), error)
	push     func(string)
	pushData func(string, string)
	pushPane func(session, content, errText string)
	// watched receives every watch ("<id>") and unwatch ("") request;
	// lines is what the last watch asked for.
	watched chan string
	lines   atomic.Int64
	// refuseWatch makes the agent answer watch requests like an old build.
	refuseWatch bool
}

// fakeAgent answers requests like the remote agent would, over pipes.
func fakeAgent(t *testing.T, ready bool) (dial func(context.Context) (io.WriteCloser, io.Reader, func(), error), push func(string), pushData func(string, string)) {
	t.Helper()
	a := newFakeAgent(t, ready, false)
	return a.dial, a.push, a.pushData
}

func newFakeAgent(t *testing.T, ready, refuseWatch bool) *fakeAgentT {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var wmu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		_, _ = outW.Write(append(b, '\n'))
		wmu.Unlock()
	}
	a := &fakeAgentT{watched: make(chan string, 16), refuseWatch: refuseWatch}
	go func() {
		if ready {
			write(remoteChannelReply{Event: "ready"})
		} else {
			_, _ = outW.Write([]byte("Unknown command: remote-agent\n"))
			return
		}
		sc := bufio.NewScanner(inR)
		for sc.Scan() {
			var req remoteChannelRequest
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				continue
			}
			if req.Watch != "" || req.Unwatch {
				if a.refuseWatch {
					write(remoteChannelReply{ID: req.ID, Code: 2, Error: "verb not allowed over the channel"})
					continue
				}
				a.lines.Store(int64(req.Lines))
				a.watched <- req.Watch
				write(remoteChannelReply{ID: req.ID})
				continue
			}
			switch req.Args[0] {
			case "fail":
				write(remoteChannelReply{ID: req.ID, Code: 1, Stderr: "no such session"})
			default:
				write(remoteChannelReply{ID: req.ID, Stdout: "out:" + strings.Join(req.Args, " ")})
			}
		}
	}()
	a.dial = func(context.Context) (io.WriteCloser, io.Reader, func(), error) {
		return inW, outR, func() { _ = inW.Close(); _ = outW.Close() }, nil
	}
	a.push = func(event string) { write(remoteChannelReply{Event: event}) }
	a.pushData = func(sessions, groups string) {
		write(remoteChannelReply{Event: "changed", Sessions: sessions, Groups: groups})
	}
	a.pushPane = func(session, content, errText string) {
		write(remoteChannelReply{Event: "pane", Session: session, Stdout: content, Error: errText})
	}
	return a
}

func newTestChannel(dial func(context.Context) (io.WriteCloser, io.Reader, func(), error)) (*RemoteChannel, chan RemoteChange) {
	events := make(chan RemoteChange, 8)
	ch := &RemoteChannel{name: "box", dial: dial, events: events, pending: map[int64]chan remoteChannelReply{}, backoff: time.Millisecond}
	return ch, events
}

func TestRemoteChannel_RequestReplyAndEvents(t *testing.T) {
	dial, push, pushData := fakeAgent(t, true)
	ch, events := newTestChannel(dial)
	ch.ensureConnected()
	if !ch.Connected() {
		t.Fatal("channel must be up after the agent said ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := ch.Request(ctx, []string{"list", "--json"})
	if err != nil || string(out) != "out:list --json" {
		t.Fatalf("Request = %q, %v", out, err)
	}
	_, err = ch.Request(ctx, []string{"fail"})
	if err == nil || !strings.Contains(err.Error(), "exit status 1: no such session") {
		t.Fatalf("a failing command must surface stderr like ssh exec does, got %v", err)
	}
	if errors.Is(err, errChannelDown) {
		t.Fatal("a command failure is not a transport failure")
	}

	push("changed")
	select {
	case ch := <-events:
		if ch.Remote != "box" || ch.HasData {
			t.Fatalf("bare event = %+v, want remote box without data", ch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushed change did not reach the events channel")
	}

	// An event that carries listings arrives parsed, with the remote name
	// stamped on every session.
	pushData(`[{"id":"s1","title":"one","group":"work","tool":"claude","status":"running"}]`, `{"groups":[{"path":"work","children":[{"path":"work/api"}]},{"path":"empty"}]}`)
	select {
	case ch := <-events:
		if !ch.HasData || len(ch.Sessions) != 1 || ch.Sessions[0].RemoteName != "box" || ch.Sessions[0].Title != "one" {
			t.Fatalf("data event = %+v, want one session from box", ch)
		}
		if strings.Join(ch.Groups, ",") != "work,work/api,empty" {
			t.Fatalf("groups = %v, want the remote's own order work,work/api,empty", ch.Groups)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushed change with data did not reach the events channel")
	}
}

func TestRemoteChannel_OldRemoteIsUnsupportedAndFallsBack(t *testing.T) {
	dial, _, _ := fakeAgent(t, false)
	ch, _ := newTestChannel(dial)
	ch.ensureConnected()
	if ch.Connected() {
		t.Fatal("a remote that does not know remote-agent must not count as connected")
	}
	if time.Until(ch.unsupportedUntil) < time.Minute {
		t.Fatal("an unsupported remote must not be redialled immediately")
	}
	_, err := ch.Request(context.Background(), []string{"list"})
	if !errors.Is(err, errChannelDown) {
		t.Fatalf("Request on a down channel must report errChannelDown so the caller execs instead, got %v", err)
	}
}

// Watch sends the watch request once per session, Unwatch releases it, and
// pushed "pane" events arrive typed on the same fan-in as "changed".
func TestRemoteChannel_WatchAndPaneEvents(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, events := newTestChannel(agent.dial)
	ch.ensureConnected()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	expectWatched := func(want string) {
		t.Helper()
		select {
		case got := <-agent.watched:
			if got != want {
				t.Fatalf("agent got watch %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("agent never received the watch request for %q", want)
		}
	}
	if err := ch.Watch(ctx, "s1", 200); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	expectWatched("s1")
	if ch.Watching() != "s1" {
		t.Fatalf("Watching = %q, want s1", ch.Watching())
	}
	if got := agent.lines.Load(); got != 200 {
		t.Fatalf("the watch request must carry the line budget, agent got %d", got)
	}
	if err := ch.Watch(ctx, "s1", 200); err != nil {
		t.Fatalf("repeat Watch: %v", err)
	}
	select {
	case got := <-agent.watched:
		t.Fatalf("a repeated watch of the same session must not be sent again, agent got %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	agent.pushPane("s1", "screen text", "")
	select {
	case ev := <-events:
		if ev.Remote != "box" || ev.Pane == nil || ev.Pane.Session != "s1" || ev.Pane.Content != "screen text" || ev.Pane.Err != "" || ev.HasData {
			t.Fatalf("pane event = %+v (pane %+v)", ev, ev.Pane)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pane push did not reach the events channel")
	}
	agent.pushPane("s1", "", "session 's1' not found")
	select {
	case ev := <-events:
		if ev.Pane == nil || ev.Pane.Err == "" || ev.Pane.Content != "" {
			t.Fatalf("failed capture must arrive as a pane event with Err, got %+v", ev.Pane)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pane failure push did not reach the events channel")
	}
	// "changed" keeps flowing on the same channel, untyped as pane.
	agent.push("changed")
	select {
	case ev := <-events:
		if ev.Pane != nil || ev.Remote != "box" {
			t.Fatalf("changed event = %+v, must not carry a pane", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("changed push did not reach the events channel")
	}

	if err := ch.Watch(ctx, "s2", 200); err != nil {
		t.Fatalf("Watch s2: %v", err)
	}
	expectWatched("s2")
	if err := ch.Unwatch(ctx); err != nil {
		t.Fatalf("Unwatch: %v", err)
	}
	expectWatched("")
	if ch.Watching() != "" {
		t.Fatalf("Watching after Unwatch = %q", ch.Watching())
	}
	if err := ch.Unwatch(ctx); err != nil {
		t.Fatalf("second Unwatch: %v", err)
	}
	select {
	case got := <-agent.watched:
		t.Fatalf("Unwatch with nothing watched must not send, agent got %q", got)
	case <-time.After(50 * time.Millisecond):
	}

	// The watch dies with the transport: after markDown nothing is watched,
	// so the caller asks again once reconnected.
	if err := ch.Watch(ctx, "s3", 200); err != nil {
		t.Fatalf("Watch s3: %v", err)
	}
	expectWatched("s3")
	ch.markDown()
	if ch.Watching() != "" {
		t.Fatalf("Watching after the transport dropped = %q, want none", ch.Watching())
	}
	if err := ch.Watch(ctx, "s3", 200); !errors.Is(err, errChannelDown) {
		t.Fatalf("Watch on a down channel must report errChannelDown, got %v", err)
	}
	if !ch.PaneWatchSupported() {
		t.Fatal("a transport failure must not mark pane watching unsupported")
	}
}

// An agent that does not know watch requests (older build) answers with an
// error; the channel remembers that so the caller polls instead.
func TestRemoteChannel_WatchRefusedMarksUnsupported(t *testing.T) {
	agent := newFakeAgent(t, true, true)
	ch, _ := newTestChannel(agent.dial)
	ch.ensureConnected()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := ch.Watch(ctx, "s1", 200)
	if err == nil || errors.Is(err, errChannelDown) {
		t.Fatalf("a refused watch must fail without looking like a transport failure, got %v", err)
	}
	if ch.Watching() != "" || ch.PaneWatchSupported() {
		t.Fatalf("after a refusal: Watching=%q supported=%v, want none/false", ch.Watching(), ch.PaneWatchSupported())
	}
	if err := ch.Watch(ctx, "s2", 200); err == nil {
		t.Fatal("watch must not be retried on a remote that refused it")
	}
	if ch.Connected() {
		out, err := ch.Request(ctx, []string{"list"})
		if err != nil || string(out) != "out:list" {
			t.Fatalf("ordinary requests must keep working after a refused watch, got %q, %v", out, err)
		}
	}
}
