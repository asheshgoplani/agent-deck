package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// agentHarness drives serveRemoteAgent over pipes: send writes one request
// line, replies delivers every decoded line, closeIn ends stdin, done closes
// when the agent loop returned.
type agentHarness struct {
	t       *testing.T
	inW     *io.PipeWriter
	replies chan remoteAgentReply
	done    chan struct{}
}

func startAgent(t *testing.T, cfg remoteAgentConfig) *agentHarness {
	t.Helper()
	return startAgentOn(t, cfg, nil)
}

// startAgentOn lets a test replace the agent's stdout (wrap wraps the pipe
// writer the harness reads from).
func startAgentOn(t *testing.T, cfg remoteAgentConfig, wrap func(io.Writer) io.Writer) *agentHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var out io.Writer = outW
	if wrap != nil {
		out = wrap(outW)
	}
	h := &agentHarness{t: t, inW: inW, replies: make(chan remoteAgentReply, 256), done: make(chan struct{})}
	go func() {
		serveRemoteAgent(context.Background(), inR, out, cfg)
		_ = outW.Close()
		close(h.done)
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
		for sc.Scan() {
			var r remoteAgentReply
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				h.replies <- r
			}
		}
		close(h.replies)
	}()
	if r := h.next("ready"); r.Event != "ready" {
		t.Fatalf("first line must announce ready, got %+v", r)
	}
	t.Cleanup(func() { _ = inW.Close() })
	return h
}

func (h *agentHarness) send(req remoteAgentRequest) {
	h.t.Helper()
	b, _ := json.Marshal(req)
	if _, err := h.inW.Write(append(b, '\n')); err != nil {
		h.t.Fatal(err)
	}
}

// next returns the next reply that is not a liveness ping.
func (h *agentHarness) next(what string) remoteAgentReply {
	h.t.Helper()
	for {
		select {
		case r, ok := <-h.replies:
			if !ok {
				h.t.Fatalf("agent closed while waiting for %s", what)
			}
			if r.Event == "ping" {
				continue
			}
			return r
		case <-time.After(3 * time.Second):
			h.t.Fatalf("timeout waiting for %s", what)
		}
	}
}

func (h *agentHarness) noneWithin(d time.Duration, what string) {
	h.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case r, ok := <-h.replies:
			if !ok {
				h.t.Fatalf("agent closed while expecting %s", what)
			}
			if r.Event == "ping" {
				continue
			}
			h.t.Fatalf("unexpected %+v while expecting %s", r, what)
		case <-deadline:
			return
		}
	}
}

func (h *agentHarness) closeAndWait() {
	h.t.Helper()
	_ = h.inW.Close()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		h.t.Fatal("agent did not exit when stdin closed")
	}
}

// Finding 2: a probe runs no sooner than the quiet interval after the last
// one ended, so a burst of state writes costs one probe (with the final
// listing) instead of one per tick; a write that lands while a probe runs
// is probed again rather than taken as seen.
func TestRemoteAgent_ProbeDebounceAndReprobe(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, `["v0"]`); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	var touchDuringProbe atomic.Bool
	probe := func() (string, string, error) {
		probes.Add(1)
		content, _ := os.ReadFile(db)
		if touchDuringProbe.CompareAndSwap(true, false) {
			// A write landing mid-probe: same content, new stamp.
			time.Sleep(2 * time.Millisecond)
			_ = writeFileAtomic(db, string(content)+" ")
		}
		return strings.TrimSpace(string(content)), "{}", nil
	}
	const quiet = 150 * time.Millisecond
	h := startAgent(t, remoteAgentConfig{
		Run:        func(context.Context, []string) (string, string, int) { return "", "", 0 },
		Probe:      probe,
		WatchPath:  db,
		WatchEvery: 5 * time.Millisecond,
		ProbeQuiet: quiet,
	})
	// Let the seed probe and the quiet interval after it pass.
	time.Sleep(quiet + 50*time.Millisecond)
	if seed := probes.Load(); seed != 1 {
		t.Fatalf("expected exactly the seed probe before any change, got %d", seed)
	}

	// One write after a quiet interval is probed at once (no added lag).
	if err := writeFileAtomic(db, `["v1"]`); err != nil {
		t.Fatal(err)
	}
	if r := h.next("changed for v1"); r.Event != "changed" || r.Sessions != `["v1"]` {
		t.Fatalf("expected the v1 listing, got %+v", r)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("a lone write costs one probe, got %d in total", got)
	}

	// Ten writes inside the quiet interval: one probe, one changed event
	// carrying the final listing.
	for i := 0; i < 10; i++ {
		if err := writeFileAtomic(db, `["v1","`+strings.Repeat("x", i)+`"]`); err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Millisecond)
	}
	if r := h.next("changed after burst"); r.Event != "changed" || r.Sessions != `["v1","xxxxxxxxx"]` {
		t.Fatalf("expected one changed event with the final listing, got %+v", r)
	}
	h.noneWithin(quiet+100*time.Millisecond, "silence after one probe for the burst")
	if got := probes.Load(); got != 3 {
		t.Fatalf("a burst of writes must cost one probe, got %d in total", got)
	}

	// A stamp that moves during the probe: exactly one more probe (the
	// reprobe finds nothing new and pushes nothing).
	touchDuringProbe.Store(true)
	if err := writeFileAtomic(db, `["v2"]`); err != nil {
		t.Fatal(err)
	}
	if r := h.next("changed for v2"); r.Event != "changed" || r.Sessions != `["v2"]` {
		t.Fatalf("expected the v2 listing, got %+v", r)
	}
	h.noneWithin(2*quiet+100*time.Millisecond, "no push for a reprobe that finds the same listing")
	if got := probes.Load(); got != 5 {
		t.Fatalf("a stamp moved during the probe must be probed once more (v2 probe + reprobe), got %d in total", got)
	}
	h.closeAndWait()
}

// Finding 9 and 13: a probe whose listing is not a JSON array (an error
// message on stdout) or whose groups are not an object pushes a bare
// "changed"; a valid listing is pushed compacted, with the stamp it was
// taken at; a listing over the push cap goes bare too.
func TestRemoteAgent_PushShapeCompactionAndCap(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, "start"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	list, groups := "[]", "{}"
	setListing := func(l, g string) {
		mu.Lock()
		list, groups = l, g
		mu.Unlock()
		// Any write moves the stamp; the content is irrelevant to the probe.
		if err := writeFileAtomic(db, l+g); err != nil {
			t.Fatal(err)
		}
	}
	probe := func() (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		return list, groups, nil
	}
	h := startAgent(t, remoteAgentConfig{
		Run:          func(context.Context, []string) (string, string, int) { return "", "", 0 },
		Probe:        probe,
		WatchPath:    db,
		WatchEvery:   5 * time.Millisecond,
		ProbeQuiet:   10 * time.Millisecond,
		MaxPushBytes: 200,
	})
	time.Sleep(30 * time.Millisecond)

	setListing("Error: database is locked\n", "{}")
	if r := h.next("bare changed for an error listing"); r.Event != "changed" || r.Sessions != "" || r.Groups != "" {
		t.Fatalf("an error listing must push a bare changed event, got %+v", r)
	}
	setListing("[]\n", "Error: database is locked\n")
	if r := h.next("bare changed for error groups"); r.Event != "changed" || r.Sessions != "" || r.Groups != "" {
		t.Fatalf("an error group listing must push a bare changed event, got %+v", r)
	}

	before := time.Now().Add(-time.Second).UnixNano()
	setListing("[\n  {\n    \"id\": \"a\",\n    \"title\": \"x y\"\n  }\n]\n", "{\n  \"groups\": [],\n  \"total_groups\": 0\n}\n")
	r := h.next("compact listing")
	if r.Event != "changed" || r.Sessions != `[{"id":"a","title":"x y"}]` || r.Groups != `{"groups":[],"total_groups":0}` {
		t.Fatalf("listings must be pushed compacted, got %+v", r)
	}
	if r.Stamp < before {
		t.Fatalf("a changed event must carry the DB stamp it was taken at, got %d", r.Stamp)
	}

	setListing("["+strings.Repeat(`{"id":"b"},`, 30)+`{"id":"c"}]`, "{}")
	if r := h.next("bare changed for an oversize listing"); r.Event != "changed" || r.Sessions != "" || r.Stamp == 0 {
		t.Fatalf("a listing over the cap must push a bare changed event with a stamp, got %+v", r)
	}
	h.closeAndWait()
}

// Finding 4: identical read-only requests in flight share one subprocess
// and each get the answer under their own id; distinct requests do not.
func TestRemoteAgent_SingleFlightReadOnlyRequests(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	run := func(ctx context.Context, args []string) (string, string, int) {
		runs.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "out:" + strings.Join(args, " "), "", 0
	}
	h := startAgent(t, remoteAgentConfig{Run: run})
	h.send(remoteAgentRequest{ID: 1, Args: []string{"list", "--json"}})
	h.send(remoteAgentRequest{ID: 2, Args: []string{"list", "--json"}})
	h.send(remoteAgentRequest{ID: 3, Args: []string{"group", "list", "--json"}})
	h.send(remoteAgentRequest{ID: 4, Args: []string{"rename", "s1", "t"}})
	h.send(remoteAgentRequest{ID: 5, Args: []string{"rename", "s1", "t"}})
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 4 {
		t.Fatalf("two identical list requests must share one run (list, group list, rename x2 = 4), got %d", got)
	}
	close(release)
	seen := map[int64]string{}
	for i := 0; i < 5; i++ {
		r := h.next("reply")
		seen[r.ID] = r.Stdout
	}
	if seen[1] != "out:list --json" || seen[2] != "out:list --json" || seen[3] != "out:group list --json" || seen[4] != "out:rename s1 t" || seen[5] != "out:rename s1 t" {
		t.Fatalf("every request must be answered under its own id, got %v", seen)
	}
	// The shared run is over: a new identical request runs afresh.
	h.send(remoteAgentRequest{ID: 6, Args: []string{"list", "--json"}})
	if r := h.next("fresh list"); r.ID != 6 || r.Stdout != "out:list --json" {
		t.Fatalf("fresh list reply = %+v", r)
	}
	if got := runs.Load(); got != 5 {
		t.Fatalf("a request after the shared run finished must run again, got %d runs", got)
	}
	h.closeAndWait()
}

// Finding 4: {"id":N,"cancel":true} ends request N's subprocess and no
// reply follows; a shared run survives until its last sharer cancels.
func TestRemoteAgent_CancelKillsRequest(t *testing.T) {
	var cancelled atomic.Int32
	run := func(ctx context.Context, args []string) (string, string, int) {
		select {
		case <-ctx.Done():
			cancelled.Add(1)
			return "", "killed", 137
		case <-time.After(2 * time.Second):
			return "finished", "", 0
		}
	}
	h := startAgent(t, remoteAgentConfig{Run: run})
	h.send(remoteAgentRequest{ID: 1, Args: []string{"rename", "s1", "t"}})
	h.send(remoteAgentRequest{ID: 2, Args: []string{"list", "--json"}})
	h.send(remoteAgentRequest{ID: 3, Args: []string{"list", "--json"}})
	time.Sleep(30 * time.Millisecond)
	h.send(remoteAgentRequest{ID: 1, Cancel: true})
	h.send(remoteAgentRequest{ID: 2, Cancel: true})
	time.Sleep(50 * time.Millisecond)
	if got := cancelled.Load(); got != 1 {
		t.Fatalf("cancelling a lone request kills it, cancelling one sharer of a run does not: want 1 kill, got %d", got)
	}
	h.noneWithin(50*time.Millisecond, "silence for cancelled requests")
	h.send(remoteAgentRequest{ID: 3, Cancel: true})
	time.Sleep(50 * time.Millisecond)
	if got := cancelled.Load(); got != 2 {
		t.Fatalf("the last sharer's cancel must kill the shared run, got %d kills", got)
	}
	h.noneWithin(50*time.Millisecond, "no reply for any cancelled request")
	// Cancelling an unknown id is harmless.
	h.send(remoteAgentRequest{ID: 99, Cancel: true})
	h.send(remoteAgentRequest{ID: 4, Args: []string{"status"}})
	// status is not run in this test's runner; it just needs to answer.
	if r := h.next("status reply"); r.ID != 4 {
		t.Fatalf("reply after cancels = %+v", r)
	}
	h.closeAndWait()
}

// Finding 4: at most MaxConcurrent subprocesses run at once; the rest wait
// their turn, and a queued request that is cancelled never runs.
func TestRemoteAgent_ConcurrencyCap(t *testing.T) {
	var running, peak atomic.Int32
	var started atomic.Int32
	release := make(chan struct{})
	run := func(ctx context.Context, args []string) (string, string, int) {
		started.Add(1)
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		running.Add(-1)
		return strings.Join(args, " "), "", 0
	}
	h := startAgent(t, remoteAgentConfig{Run: run, MaxConcurrent: 2})
	for i := int64(1); i <= 5; i++ {
		h.send(remoteAgentRequest{ID: i, Args: []string{"rename", "s", strings.Repeat("x", int(i))}})
	}
	time.Sleep(50 * time.Millisecond)
	if got := started.Load(); got != 2 {
		t.Fatalf("only MaxConcurrent requests may run at once, got %d started", got)
	}
	h.send(remoteAgentRequest{ID: 5, Cancel: true})
	close(release)
	ids := map[int64]bool{}
	for i := 0; i < 4; i++ {
		ids[h.next("reply").ID] = true
	}
	if ids[5] || len(ids) != 4 {
		t.Fatalf("four queued requests must answer and the cancelled one must not, got %v", ids)
	}
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds the cap of 2", peak.Load())
	}
	if started.Load() != 4 {
		t.Fatalf("a request cancelled while queued must never start, got %d starts", started.Load())
	}
	h.closeAndWait()
}

// Finding 6: the agent pushes ping events, answers the peer's pings under
// their id, and exits once stdin stays silent past the idle deadline.
func TestRemoteAgent_PingAndIdleExit(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, outW, remoteAgentConfig{
			Run:       func(context.Context, []string) (string, string, int) { return "", "", 0 },
			PingEvery: 20 * time.Millisecond,
			IdleAfter: 300 * time.Millisecond,
		})
		_ = outW.Close()
		close(done)
	}()
	sc := bufio.NewScanner(outR)
	lines := make(chan remoteAgentReply, 64)
	go func() {
		for sc.Scan() {
			var r remoteAgentReply
			_ = json.Unmarshal(sc.Bytes(), &r)
			lines <- r
		}
		close(lines)
	}()
	if r := <-lines; r.Event != "ready" {
		t.Fatalf("ready first, got %+v", r)
	}
	if r := <-lines; r.Event != "ping" {
		t.Fatalf("expected a ping event, got %+v", r)
	}
	// The peer's pings reset the idle deadline: keep it alive for longer
	// than IdleAfter, then let it lapse.
	for i := 0; i < 4; i++ {
		time.Sleep(150 * time.Millisecond)
		b, _ := json.Marshal(remoteAgentRequest{ID: int64(10 + i), Ping: true})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
		for {
			r := <-lines
			if r.Event == "ping" {
				continue
			}
			if r.ID != int64(10+i) || r.Code != 0 || r.Error != "" {
				t.Fatalf("ping with an id must be acknowledged under it, got %+v", r)
			}
			break
		}
	}
	select {
	case <-done:
		t.Fatal("agent exited although the peer kept pinging")
	default:
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit after the idle deadline")
	}
	_ = inW.Close()
}

// Finding 6: a stdout that stops taking lines ends the agent even while
// stdin stays open, and never blocks a reply or the watcher on the way.
func TestRemoteAgent_ExitsWhenStdoutDies(t *testing.T) {
	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	var writes atomic.Int32
	out := writerFunc(func(b []byte) (int, error) {
		if writes.Add(1) > 1 {
			return 0, errors.New("broken pipe")
		}
		return len(b), nil
	})
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, out, remoteAgentConfig{
			Run:       func(context.Context, []string) (string, string, int) { return "", "", 0 },
			PingEvery: 10 * time.Millisecond,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit after stdout failed")
	}
}

// Finding 6: the outbound queue is bounded and a peer that never reads
// cannot wedge the agent: the writer gives up and the agent exits.
func TestRemoteAgent_ExitsWhenPeerStopsReading(t *testing.T) {
	inR, inW := io.Pipe()
	defer func() { _ = inW.Close() }()
	blocked := make(chan struct{})
	out := writerFunc(func(b []byte) (int, error) {
		<-blocked // never drained
		return 0, errors.New("closed")
	})
	done := make(chan struct{})
	go func() {
		serveRemoteAgent(context.Background(), inR, out, remoteAgentConfig{
			Run:       func(context.Context, []string) (string, string, int) { return "", "", 0 },
			PingEvery: time.Millisecond,
		})
		close(done)
	}()
	// The loop must keep taking requests while the queue fills, and end
	// once it is full.
	b, _ := json.Marshal(remoteAgentRequest{ID: 1, Args: []string{"status"}})
	_, _ = inW.Write(append(b, '\n'))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not exit after its outbound queue filled")
	}
	close(blocked)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// Finding 10: a command reply carries the DB stamp after the command ran,
// so the local side can tell a listing older than its last action.
func TestRemoteAgent_ReplyCarriesStamp(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	if err := writeFileAtomic(db, "v1"); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, args []string) (string, string, int) {
		_ = writeFileAtomic(db, "v2")
		return "ok", "", 0
	}
	h := startAgent(t, remoteAgentConfig{Run: run, WatchPath: db})
	before := time.Now().Add(-time.Second).UnixNano()
	h.send(remoteAgentRequest{ID: 1, Args: []string{"rename", "s", "t"}})
	r := h.next("reply")
	if r.ID != 1 || r.Stdout != "ok" || r.Stamp < before {
		t.Fatalf("reply must carry the DB stamp after the command, got %+v", r)
	}
	st, _ := os.Stat(db)
	if r.Stamp != st.ModTime().UnixNano() {
		t.Fatalf("stamp %d must be the state file's mtime %d", r.Stamp, st.ModTime().UnixNano())
	}
	h.closeAndWait()
}

func TestRemoteAgentReadOnly(t *testing.T) {
	for args, want := range map[string]bool{
		"list --json":             true,
		"group list --json":       true,
		"costs summary --json":    true,
		"group create x":          false,
		"rename s t":              false,
		"remove s":                false,
		"session output s --pane": true,
		"session restart s":       false,
	} {
		if got := remoteAgentReadOnly(strings.Fields(args)); got != want {
			t.Errorf("remoteAgentReadOnly(%q) = %v, want %v", args, got, want)
		}
	}
}
