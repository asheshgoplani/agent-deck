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
	// mute drops every reply and push once set: the link looks half-open.
	mute atomic.Bool
	// pings counts ping requests received.
	pings atomic.Int64
	// hung receives the id of every "hang" request, which never gets a
	// reply; hangOn names one more verb that hangs the same way ("" for
	// none), so a real read-only or mutating verb can be left unanswered.
	hung   chan int64
	hangOn atomic.Value
	// closeOut ends the agent's output (the transport dies from the far
	// side).
	closeOut func()
	// stamp is what every reply carries as its "stamp" (0 for an agent
	// that predates stamps); pushDataStamped pushes a listing with one.
	stamp           atomic.Int64
	pushDataStamped func(sessions, groups string, stamp int64)
	// cancelled receives the id of every cancel line.
	cancelled chan int64
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
	a := &fakeAgentT{watched: make(chan string, 16), refuseWatch: refuseWatch, hung: make(chan int64, 16), cancelled: make(chan int64, 16)}
	write := func(v any) {
		if a.mute.Load() {
			return
		}
		b, _ := json.Marshal(v)
		wmu.Lock()
		_, _ = outW.Write(append(b, '\n'))
		wmu.Unlock()
	}
	a.closeOut = func() { _ = outW.Close() }
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
			if req.Cancel {
				a.cancelled <- req.ID
				continue
			}
			if req.Ping {
				// Like an agent that predates pings: any reply proves life.
				a.pings.Add(1)
				write(remoteChannelReply{ID: req.ID, Code: 2, Error: "verb not allowed over the channel"})
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
			if len(req.Args) == 0 {
				write(remoteChannelReply{ID: req.ID, Code: 2, Error: "verb not allowed over the channel"})
				continue
			}
			hangOn, _ := a.hangOn.Load().(string)
			switch {
			case req.Args[0] == "hang" || (hangOn != "" && req.Args[0] == hangOn):
				a.hung <- req.ID
			case req.Args[0] == "fail":
				write(remoteChannelReply{ID: req.ID, Code: 1, Stderr: "no such session", Stamp: a.stamp.Load()})
			default:
				write(remoteChannelReply{ID: req.ID, Stdout: "out:" + strings.Join(req.Args, " "), Stamp: a.stamp.Load()})
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
	a.pushDataStamped = func(sessions, groups string, stamp int64) {
		write(remoteChannelReply{Event: "changed", Sessions: sessions, Groups: groups, Stamp: stamp})
	}
	a.pushPane = func(session, content, errText string) {
		write(remoteChannelReply{Event: "pane", Session: session, Stdout: content, Error: errText})
	}
	return a
}

// redialingAgent returns a dial that starts a fresh fake agent on every
// call (a real redial gets a new ssh process, not the pipes closeFn shut)
// and a getter for the agent currently serving.
func redialingAgent(t *testing.T) (dial func(context.Context) (io.WriteCloser, io.Reader, func(), error), current func() *fakeAgentT) {
	t.Helper()
	var mu sync.Mutex
	var cur *fakeAgentT
	dial = func(ctx context.Context) (io.WriteCloser, io.Reader, func(), error) {
		a := newFakeAgent(t, true, false)
		mu.Lock()
		cur = a
		mu.Unlock()
		return a.dial(ctx)
	}
	current = func() *fakeAgentT {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	return dial, current
}

func newTestChannel(dial func(context.Context) (io.WriteCloser, io.Reader, func(), error)) (*RemoteChannel, <-chan RemoteChange) {
	inbox := newRemoteChangeMailbox()
	ch := newRemoteChannel("box", "h\x00p\x00agent-deck", dial)
	ch.events = inbox
	ch.backoff = time.Millisecond
	return ch, inbox.Events()
}

// waitUntil polls cond for up to d.
func waitUntil(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
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

// A reply lost after the request went out is not the same as a request that
// never went out: the remote may have run the command (#3).
func TestRemoteChannel_LostReplyIsInterruptedNotDown(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, _ := newTestChannel(agent.dial)
	ch.ensureConnected()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ch.Request(ctx, []string{"hang"})
		done <- err
	}()
	select {
	case <-agent.hung:
	case <-time.After(2 * time.Second):
		t.Fatal("agent never received the request")
	}
	agent.closeOut()
	select {
	case err := <-done:
		if !errors.Is(err, errChannelInterrupted) {
			t.Fatalf("a request the agent received must fail as interrupted, got %v", err)
		}
		if errors.Is(err, errChannelDown) {
			t.Fatal("interrupted must not read as down, or the caller would re-run the command")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not fail when the transport dropped")
	}
	if ch.Connected() {
		t.Fatal("channel must be down after the agent's output ended")
	}
	if _, err := ch.Request(ctx, []string{"list"}); !errors.Is(err, errChannelDown) {
		t.Fatalf("a request on the down channel was never written, want errChannelDown, got %v", err)
	}
}

// SSHRunner.run re-runs only read-only verbs after an interrupted request;
// a mutating verb's interruption reaches the caller (#3). The next request
// after a drop redials.
func TestSSHRunnerRun_InterruptedMutatingVerbIsNotReexecuted(t *testing.T) {
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "1")
	t.Cleanup(CloseRemoteChannels)
	dial, agent := redialingAgent(t)
	// Host "" makes the exec fallback fail on validation, which is how the
	// test sees whether a fallback was attempted at all.
	r := &SSHRunner{name: "interrupt-test", Profile: "default", AgentDeckPath: "agent-deck", dialChannelFn: dial}
	ch := channelFor(r)
	if ch == nil {
		t.Fatal("channelFor returned nil")
	}
	if !waitUntil(t, 2*time.Second, ch.Connected) {
		t.Fatal("channel never connected")
	}
	// interrupted runs args, waits until the agent has the request, then
	// drops the transport under it.
	interrupted := func(args ...string) error {
		a := agent()
		a.hangOn.Store(args[0])
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := r.run(ctx, args...)
			done <- err
		}()
		select {
		case <-a.hung:
		case <-time.After(2 * time.Second):
			t.Fatal("agent never received the request")
		}
		ch.markDown()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			t.Fatal("run did not return after the transport dropped")
		}
		return nil
	}
	err := interrupted("remove", "s1")
	if !errors.Is(err, errChannelInterrupted) {
		t.Fatalf("a mutating verb interrupted after the write must surface errChannelInterrupted, got %v", err)
	}
	if strings.Contains(err.Error(), "ssh host") {
		t.Fatalf("a mutating verb must not be re-run over exec, got %v", err)
	}
	// The next request finds the channel down, kicks a redial and execs.
	// (A redial waits out the backoff; the test does not.)
	ch.mu.Lock()
	ch.lastAttempt = time.Time{}
	ch.mu.Unlock()
	if _, err := r.run(context.Background(), "list", "--json"); err == nil || !strings.Contains(err.Error(), "ssh host is empty") {
		t.Fatalf("a request on the dropped channel must fall back to exec, got %v", err)
	}
	if !waitUntil(t, 2*time.Second, ch.Connected) {
		t.Fatal("the request after a drop did not redial")
	}
	err = interrupted("list", "--json")
	if errors.Is(err, errChannelInterrupted) {
		t.Fatalf("an interrupted listing must be re-run over exec, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ssh host is empty") {
		t.Fatalf("the exec fallback must have been attempted, got %v", err)
	}
}

func TestRemoteVerbReadOnly(t *testing.T) {
	readOnly := [][]string{
		{"list", "--json"}, {"ls"}, {"accounts", "--json"}, {"group", "list", "--json"},
		{"costs", "summary", "--json"}, {"mcp", "list", "--quiet"}, {"skill", "list"},
		{"session", "output", "s1", "--json"}, {"session", "pane", "s1"}, {"session", "show", "s1"}, {"version"},
		{"inbox", "export", "--json"}, {"inbox", "writer-status", "--json"},
	}
	for _, args := range readOnly {
		if !remoteVerbReadOnly(args) {
			t.Errorf("%v must be read-only", args)
		}
	}
	mutating := [][]string{
		nil, {"add", "/p"}, {"remove", "s1"}, {"session", "restart", "s1"}, {"session", "fork", "--json", "s1"},
		{"session", "stop", "s1"}, {"group", "move", "g", "--position", "1"}, {"mcp", "attach", "s1", "x"}, {"costs"},
		{"session", "archive", "s1"}, {"session", "unarchive", "s1"}, {"inbox", "clear"},
	}
	for _, args := range mutating {
		if remoteVerbReadOnly(args) {
			t.Errorf("%v must count as mutating", args)
		}
	}
}

// Two requests in a row that get no reply before their deadline take the
// transport down, so the caller execs and the next request redials (#5).
func TestRemoteChannel_ConsecutiveTimeoutsMarkDown(t *testing.T) {
	dial, _ := redialingAgent(t)
	ch, _ := newTestChannel(dial)
	ch.pingEvery = time.Hour
	ch.ensureConnected()
	for i := 0; i < remoteChannelTimeoutsToDrop; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, err := ch.Request(ctx, []string{"hang"})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request %d: want deadline exceeded, got %v", i, err)
		}
		if i < remoteChannelTimeoutsToDrop-1 && !ch.Connected() {
			t.Fatalf("one timeout alone must not take the channel down")
		}
	}
	if ch.Connected() {
		t.Fatal("channel must be down after consecutive timeouts")
	}
	// A reply in between resets the count.
	ch.lastAttempt = time.Time{}
	ch.ensureConnected()
	if !ch.Connected() {
		t.Fatal("redial failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, _ = ch.Request(ctx, []string{"hang"})
	cancel()
	if _, err := ch.Request(context.Background(), []string{"list"}); err != nil {
		t.Fatalf("list after one timeout: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, _ = ch.Request(ctx, []string{"hang"})
	cancel()
	if !ch.Connected() {
		t.Fatal("a reply between two timeouts must reset the count")
	}
}

// A silent agent (half-open link) is found by the client's ping; an agent
// that answers pings, even with an error like an old build, keeps the
// channel up.
func TestRemoteChannel_PingDetectsSilentLink(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, _ := newTestChannel(agent.dial)
	ch.pingEvery = 20 * time.Millisecond
	ch.pingTimeout = 40 * time.Millisecond
	ch.ensureConnected()
	if !waitUntil(t, 2*time.Second, func() bool { return agent.pings.Load() >= 2 }) {
		t.Fatal("client never pinged the idle agent")
	}
	if !ch.Connected() {
		t.Fatal("an agent that answers pings must keep the channel up")
	}
	agent.mute.Store(true)
	if !waitUntil(t, 2*time.Second, func() bool { return !ch.Connected() }) {
		t.Fatal("a missed ping window must take the channel down")
	}
	if _, err := ch.Request(context.Background(), []string{"list"}); !errors.Is(err, errChannelDown) {
		t.Fatalf("after the ping failure requests must fall back, got %v", err)
	}
}

// Only readable non-JSON output from the remote (an old build) marks it
// unsupported for a long time; link trouble gets the short backoff (#7).
func TestRemoteChannel_DialFailuresBackOffInsteadOfDisabling(t *testing.T) {
	cases := []struct {
		name string
		dial func(context.Context) (io.WriteCloser, io.Reader, func(), error)
	}{
		{"dial error", func(context.Context) (io.WriteCloser, io.Reader, func(), error) {
			return nil, nil, nil, errors.New("ssh: connect: connection refused")
		}},
		{"eof before hello", func(context.Context) (io.WriteCloser, io.Reader, func(), error) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			_ = outW.Close()
			return inW, outR, func() { _ = inR.Close() }, nil
		}},
		{"slow hello", func(context.Context) (io.WriteCloser, io.Reader, func(), error) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			return inW, outR, func() { _ = inR.Close(); _ = outW.Close() }, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, _ := newTestChannel(tc.dial)
			ch.helloTimeout = 30 * time.Millisecond
			ch.ensureConnected()
			if ch.Connected() {
				t.Fatal("must not be connected")
			}
			if !ch.unsupportedUntil.IsZero() {
				t.Fatalf("%s must not mark the remote unsupported", tc.name)
			}
			if ch.backoff <= time.Millisecond || ch.backoff > 30*time.Second {
				t.Fatalf("backoff = %v, want doubled within the 30s cap", ch.backoff)
			}
			if ch.dialing {
				t.Fatal("dialing flag left set")
			}
		})
	}
	dial, _, _ := fakeAgent(t, false)
	ch, _ := newTestChannel(dial)
	ch.ensureConnected()
	if time.Until(ch.unsupportedUntil) < time.Minute {
		t.Fatal("non-JSON hello must still mark the remote unsupported")
	}
}

// A line longer than the cap costs the transport, not the process (#13).
func TestRemoteChannel_OversizeFrameDropsTransport(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, events := newTestChannel(agent.dial)
	ch.maxFrame = 4096
	ch.ensureConnected()
	agent.pushData(`[{"id":"s1","title":"one"}]`, "")
	select {
	case ev := <-events:
		if !ev.HasData {
			t.Fatalf("small frame must still arrive parsed, got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("small push did not arrive")
	}
	agent.pushData(`[{"id":"s1","title":"`+strings.Repeat("x", 8192)+`"}]`, "")
	if !waitUntil(t, 2*time.Second, func() bool { return !ch.Connected() }) {
		t.Fatal("an oversize frame must take the channel down")
	}
	select {
	case ev := <-events:
		t.Fatalf("the oversize frame must not be delivered, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestReadFrame(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", 100)+"\nshort\n"+strings.Repeat("b", 50)), 16)
	line, err := readFrame(r, 200)
	if err != nil || len(line) != 101 {
		t.Fatalf("long line across buffer refills: len %d, %v", len(line), err)
	}
	line, err = readFrame(r, 200)
	if err != nil || string(line) != "short\n" {
		t.Fatalf("second line = %q, %v", line, err)
	}
	line, err = readFrame(r, 200)
	if !errors.Is(err, io.EOF) || len(line) != 50 {
		t.Fatalf("unterminated tail: %q, %v", line, err)
	}
	r = bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", 100)+"\n"), 16)
	if _, err := readFrame(r, 64); !errors.Is(err, errChannelFrameTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
}

// The mailbox keeps only the newest snapshot per remote and the newest pane
// per session, and delivers slots in first-arrival order (#14).
func TestRemoteChangeMailbox_LatestWinsPerSlot(t *testing.T) {
	m := newRemoteChangeMailbox()
	one := func(id string) []RemoteSessionInfo { return []RemoteSessionInfo{{ID: id}} }
	m.put(RemoteChange{Remote: "a", HasData: true, Sessions: one("a1")})
	m.put(RemoteChange{Remote: "b", HasData: true, Sessions: one("b1")})
	m.put(RemoteChange{Remote: "a", Pane: &RemotePaneEvent{Session: "s1", Content: "old"}})
	m.put(RemoteChange{Remote: "a", HasData: true, Sessions: one("a2")})
	m.put(RemoteChange{Remote: "a", HasData: true, Sessions: one("a3")})
	m.put(RemoteChange{Remote: "a", Pane: &RemotePaneEvent{Session: "s1", Content: "new"}})
	m.put(RemoteChange{Remote: "a", Pane: &RemotePaneEvent{Session: "s2", Content: "other"}})

	next := func() RemoteChange {
		t.Helper()
		select {
		case ev := <-m.Events():
			return ev
		case <-time.After(2 * time.Second):
			t.Fatal("mailbox delivered nothing")
			return RemoteChange{}
		}
	}
	if ev := next(); ev.Remote != "a" || ev.Pane != nil || ev.Sessions[0].ID != "a3" {
		t.Fatalf("first delivery = %+v, want a's newest listing a3", ev)
	}
	if ev := next(); ev.Remote != "b" || ev.Sessions[0].ID != "b1" {
		t.Fatalf("second delivery = %+v, want b1", ev)
	}
	if ev := next(); ev.Pane == nil || ev.Pane.Session != "s1" || ev.Pane.Content != "new" {
		t.Fatalf("third delivery = %+v, want s1's newest pane", ev)
	}
	if ev := next(); ev.Pane == nil || ev.Pane.Session != "s2" {
		t.Fatalf("fourth delivery = %+v, want s2's pane", ev)
	}
	select {
	case ev := <-m.Events():
		t.Fatalf("superseded snapshots must not be delivered, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// A push during a slow receive still arrives after the one in flight.
	m.put(RemoteChange{Remote: "a", HasData: true, Sessions: one("a4")})
	time.Sleep(20 * time.Millisecond)
	m.put(RemoteChange{Remote: "a", HasData: true, Sessions: one("a5")})
	if ev := next(); ev.Sessions[0].ID != "a4" {
		t.Fatalf("in-flight delivery = %+v, want a4", ev)
	}
	if ev := next(); ev.Sessions[0].ID != "a5" {
		t.Fatalf("follow-up delivery = %+v, want a5", ev)
	}

	// drop discards a remote's waiting slots and nothing else.
	m.put(RemoteChange{Remote: "gone", HasData: true})
	m.put(RemoteChange{Remote: "gone", Pane: &RemotePaneEvent{Session: "x"}})
	m.put(RemoteChange{Remote: "kept", HasData: true})
	m.drop("gone")
	if ev := next(); ev.Remote != "kept" {
		t.Fatalf("after drop got %+v, want kept", ev)
	}
	select {
	case ev := <-m.Events():
		t.Fatalf("dropped remote must not be delivered, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Channels follow the config: a name re-pointed at another host gets a new
// channel, a removed name loses its channel, and shutdown closes all (#8).
func TestRemoteChannels_FollowConfig(t *testing.T) {
	t.Setenv("AGENT_DECK_REMOTE_CHANNEL", "1")
	t.Cleanup(CloseRemoteChannels)
	agent1 := newFakeAgent(t, true, false)
	r1 := &SSHRunner{name: "box", Host: "h1", Profile: "default", AgentDeckPath: "agent-deck", dialChannelFn: agent1.dial}
	ch1 := channelFor(r1)
	if ch1 == nil || channelFor(r1) != ch1 {
		t.Fatal("same identity must share one channel")
	}
	if !waitUntil(t, 2*time.Second, ch1.Connected) {
		t.Fatal("ch1 never connected")
	}
	// Same config again keeps it.
	ReconcileRemoteChannels(map[string]RemoteConfig{"box": {Host: "h1"}})
	if RemoteChannelFor("box") != ch1 || !ch1.Connected() {
		t.Fatal("reconcile with an unchanged config must keep the channel")
	}

	dial2, _ := redialingAgent(t)
	r2 := &SSHRunner{name: "box", Host: "h2", Profile: "default", AgentDeckPath: "agent-deck", dialChannelFn: dial2}
	ch2 := channelFor(r2)
	if ch2 == ch1 {
		t.Fatal("a re-pointed name must get a fresh channel")
	}
	if ch1.Connected() || !ch1.closed {
		t.Fatal("the old host's channel must be closed")
	}
	agent1.push("changed")
	if !waitUntil(t, 2*time.Second, ch2.Connected) {
		t.Fatal("ch2 never connected")
	}
	if RemoteChannelFor("box") != ch2 {
		t.Fatal("registry must point at the new channel")
	}
	ch1.lastAttempt = time.Time{}
	ch1.ensureConnected()
	if ch1.Connected() {
		t.Fatal("a closed channel must never redial")
	}

	// Profile change through reconcile.
	ReconcileRemoteChannels(map[string]RemoteConfig{"box": {Host: "h2", Profile: "work"}})
	if RemoteChannelFor("box") != nil || ch2.Connected() {
		t.Fatal("a profile change must close the channel so the next command redials")
	}
	ch3 := channelFor(r2)
	if !waitUntil(t, 2*time.Second, ch3.Connected) {
		t.Fatal("ch3 never connected")
	}
	ReconcileRemoteChannels(map[string]RemoteConfig{})
	if RemoteChannelFor("box") != nil || ch3.Connected() {
		t.Fatal("a removed remote must lose its channel")
	}
	ch4 := channelFor(r2)
	if !waitUntil(t, 2*time.Second, ch4.Connected) {
		t.Fatal("ch4 never connected")
	}
	CloseRemoteChannels()
	if RemoteChannelFor("box") != nil || ch4.Connected() {
		t.Fatal("shutdown must close every channel")
	}
}

// A closed channel publishes nothing: the removed remote's rows cannot come
// back through a late push.
func TestRemoteChannel_ClosedDropsPushes(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, events := newTestChannel(agent.dial)
	ch.ensureConnected()
	agent.pushData(`[{"id":"s1"}]`, "")
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("push before close did not arrive")
	}
	ch.publish(RemoteChange{Remote: "box", HasData: true})
	ch.Close()
	ch.publish(RemoteChange{Remote: "box", HasData: true, Sessions: []RemoteSessionInfo{{ID: "late"}}})
	select {
	case ev := <-events:
		t.Fatalf("closed channel must publish nothing, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// Finding 10: a pushed listing stamped before the reply to the last
// mutating command was taken before that command ran. Its data is dropped
// (the bare "changed" still goes out so the receiver fetches); a listing
// stamped at or after it applies, and read-only verbs never move the bar.
func TestRemoteChannel_StampGateDropsListingOlderThanMutation(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, events := newTestChannel(agent.dial)
	ch.ensureConnected()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	next := func() RemoteChange {
		t.Helper()
		select {
		case c := <-events:
			return c
		case <-time.After(2 * time.Second):
			t.Fatal("no event")
			return RemoteChange{}
		}
	}
	listing := `[{"id":"s1","title":"one","tool":"claude","status":"running"}]`

	// A read-only verb's stamp is not a mutation.
	agent.stamp.Store(500)
	if _, err := ch.Request(ctx, []string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastMutationStamp(); got != 0 {
		t.Fatalf("a listing must not record a mutation stamp, got %d", got)
	}
	agent.pushDataStamped(listing, "", 100)
	if c := next(); !c.HasData || c.Stamp != 100 {
		t.Fatalf("with no mutation yet every listing applies; got %+v", c)
	}

	// A mutating verb records its reply's stamp, a refused one too.
	agent.stamp.Store(1000)
	if _, err := ch.Request(ctx, []string{"remove", "s1"}); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastMutationStamp(); got != 1000 {
		t.Fatalf("LastMutationStamp = %d, want 1000", got)
	}
	agent.stamp.Store(1200)
	_, _ = ch.Request(ctx, []string{"fail"})
	if got := ch.LastMutationStamp(); got != 1200 {
		t.Fatalf("a refused mutating verb still moves the bar: got %d, want 1200", got)
	}
	agent.stamp.Store(900)
	_, _ = ch.Request(ctx, []string{"rename", "s1", "x"})
	if got := ch.LastMutationStamp(); got != 1200 {
		t.Fatalf("the bar never goes backwards: got %d, want 1200", got)
	}

	// Older listing: data dropped, event kept.
	agent.pushDataStamped(listing, "", 1100)
	if c := next(); c.HasData || c.Sessions != nil || c.Remote != "box" || c.Stamp != 1100 {
		t.Fatalf("a listing older than the last mutation must arrive bare; got %+v", c)
	}
	// Same stamp (the probe listed right after the command): applies.
	agent.pushDataStamped(listing, "", 1200)
	if c := next(); !c.HasData || len(c.Sessions) != 1 {
		t.Fatalf("a listing stamped at the mutation applies; got %+v", c)
	}
	// Newer: applies. Unstamped (old agent): applies, the receiver decides.
	agent.pushDataStamped(listing, "", 1300)
	if c := next(); !c.HasData || c.Stamp != 1300 {
		t.Fatalf("a newer listing applies; got %+v", c)
	}
	agent.pushData(listing, "")
	if c := next(); !c.HasData || c.Stamp != 0 {
		t.Fatalf("an unstamped listing is left to the receiver; got %+v", c)
	}
}

// A request whose context ends before its reply tells the agent to cancel
// it, so the remote does not keep running a verb nobody waits for.
func TestRemoteChannel_CancelSentWhenContextEnds(t *testing.T) {
	agent := newFakeAgent(t, true, false)
	ch, _ := newTestChannel(agent.dial)
	ch.pingEvery = time.Hour
	ch.ensureConnected()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := ch.Request(ctx, []string{"hang"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	var hungID int64
	select {
	case hungID = <-agent.hung:
	case <-time.After(time.Second):
		t.Fatal("agent never received the request")
	}
	select {
	case id := <-agent.cancelled:
		if id != hungID {
			t.Fatalf("cancel names request %d, want %d", id, hungID)
		}
	case <-time.After(time.Second):
		t.Fatal("no cancel line reached the agent")
	}
	if !ch.Connected() {
		t.Fatal("one cancelled request must not take the channel down")
	}
	// A late reply for the cancelled id is ignored, not delivered to the
	// next request.
	if out, err := ch.Request(context.Background(), []string{"list"}); err != nil || string(out) != "out:list" {
		t.Fatalf("list after a cancel = %q, %v", out, err)
	}
}

// HasData is set only when the payload parsed to a listing: a JSON null,
// garbage or an absent payload leave it unset so the receiver fetches; an
// empty listing is data (the remote has no sessions).
func TestRemoteChannel_ChangeFromReplyHasDataOnlyForListings(t *testing.T) {
	ch, _ := newTestChannel(nil)
	cases := map[string]bool{
		"":                          false,
		"null":                      false,
		"{not a list}":              false,
		`[{"id":"s1","title":"x"}]`: true,
		"[]":                        true,
	}
	for payload, want := range cases {
		got := ch.changeFromReply(remoteChannelReply{Event: "changed", Sessions: payload, Stamp: 7})
		if got.HasData != want {
			t.Errorf("payload %q: HasData = %v, want %v", payload, got.HasData, want)
		}
		if got.Stamp != 7 {
			t.Errorf("payload %q: stamp not carried", payload)
		}
	}
}
