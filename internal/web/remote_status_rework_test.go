package web

// Rework round 1 of #3174 (review of 2fa65e68): G1-G4.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// scriptedRemote answers `events stats --json` with a cursor, plays one
// scripted follow connection per call, and lists the remote's sessions.
type scriptedRemote struct {
	mu        sync.Mutex
	cursor    uint64
	follows   [][]string // lines per follow connection
	followErr []string   // stderr per follow connection
	calls     []string
	attempts  []time.Time
	sessions  []session.RemoteSessionInfo
	failAll   bool
}

func (s *scriptedRemote) RunReadIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, _ []byte, follow bool, args ...string) error {
	s.mu.Lock()
	s.calls = append(s.calls, strings.Join(args, " "))
	s.attempts = append(s.attempts, time.Now())
	if s.failAll {
		s.mu.Unlock()
		return errors.New("exit status 255")
	}
	if len(args) >= 2 && args[0] == "events" && args[1] == "stats" {
		c := s.cursor
		s.mu.Unlock()
		_, err := fmt.Fprintf(stdout, `{"enabled":true,"cursor":%d}`+"\n", c)
		return err
	}
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "events follow") {
			n++
		}
	}
	var lines []string
	var errText string
	if n-1 < len(s.follows) {
		lines = s.follows[n-1]
	}
	if n-1 < len(s.followErr) {
		errText = s.followErr[n-1]
	}
	last := n > len(s.follows)
	s.mu.Unlock()
	for _, l := range lines {
		if _, err := io.WriteString(stdout, l+"\n"); err != nil {
			return err
		}
	}
	if errText != "" {
		_, _ = io.WriteString(stderr, errText)
		return errors.New("exit status 1")
	}
	if last {
		<-ctx.Done()
		return nil
	}
	return errors.New("connection reset")
}

func (s *scriptedRemote) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.RemoteSessionInfo(nil), s.sessions...), nil, nil
}

type pubRec struct {
	mu   sync.Mutex
	list []string
}

func (p *pubRec) publish(kind, sid string, data any) {
	raw, _ := json.Marshal(data)
	p.mu.Lock()
	p.list = append(p.list, kind+" "+sid+" "+string(raw))
	p.mu.Unlock()
}

func (p *pubRec) wait(t *testing.T, n int, d time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		p.mu.Lock()
		got := append([]string(nil), p.list...)
		p.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// G1: after a link reset (ssh exits at once) the forwarder retries at
// least every second, so remote status is current about a second after
// the link returns (it was 8-11 s with backoff growing to 30 s).
func TestRemoteStatusForwarder_RetriesWithinASecond(t *testing.T) {
	r := &scriptedRemote{failAll: true}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: func(string, string, any) {}, startTS: time.Now().UnixMilli()}
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	f.run(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attempts) < 4 {
		t.Fatalf("%d attempts in 4.5 s", len(r.attempts))
	}
	for i := 1; i < len(r.attempts); i++ {
		if gap := r.attempts[i].Sub(r.attempts[i-1]); gap > 1200*time.Millisecond {
			t.Fatalf("attempt %d came %s after the previous; want at most ~1 s", i, gap)
		}
	}
}

// G2 + G3: every (re)connect publishes session.link {host, up:true,
// sessions:[...]} with the remote's current statuses (so a change made
// while this server was down or the link was cut is not lost), and a link
// that ends publishes session.link {host, up:false}.
func TestRemoteStatusForwarder_LinkFramesResync(t *testing.T) {
	now := time.Now().UnixMilli()
	r := &scriptedRemote{cursor: 5,
		sessions: []session.RemoteSessionInfo{{ID: "b1", Title: "beta", Status: "waiting"}},
		follows:  [][]string{{statusLine(6, now+10, "b1", "running", "")}},
	}
	rec := &pubRec{}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: rec.publish, startTS: now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	got := rec.wait(t, 4, 3*time.Second)
	cancel()
	<-done
	joined := strings.Join(got, "\n")
	want := []string{
		`session.link  {"host":"box","sessions":[{"id":"b1","status":"waiting","title":"beta"}],"up":true}`,
		`session.status b1 `,
		`session.link  {"host":"box","up":false}`,
		`session.link  {"host":"box","sessions":[{"id":"b1","status":"waiting","title":"beta"}],"up":true}`,
	}
	if len(got) < len(want) {
		t.Fatalf("published %d frames, want at least %d:\n%s", len(got), len(want), joined)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) && got[i] != w {
			t.Fatalf("frame %d = %s\nwant prefix %s\nall:\n%s", i, got[i], w, joined)
		}
	}
}

// G4: the first follow starts at the remote's own cursor (events stats),
// not at a local-clock filter: a remote whose clock is behind still has its
// live frames forwarded.
func TestRemoteStatusForwarder_StartsAtTheRemoteCursor(t *testing.T) {
	behind := time.Now().Add(-10 * time.Minute).UnixMilli() // the remote's clock is 10 min behind
	r := &scriptedRemote{cursor: 41, follows: [][]string{{statusLine(42, behind, "b1", "waiting", "")}}}
	rec := &pubRec{}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: rec.publish, startTS: time.Now().UnixMilli()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	got := rec.wait(t, 2, 3*time.Second)
	cancel()
	<-done
	found := false
	for _, g := range got {
		if strings.HasPrefix(g, "session.status b1 ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a live frame from a remote whose clock is behind was dropped: %v", got)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	first := ""
	for _, c := range r.calls {
		if strings.HasPrefix(c, "events follow") {
			first = c
			break
		}
	}
	if !strings.Contains(first, "--since 41") {
		t.Fatalf("first follow %q, want --since 41 (the remote's cursor)", first)
	}
}

// G4: a resume cursor the remote no longer has ("cursor older than the
// retained log") restarts from the remote's current cursor with a resync,
// instead of failing for good.
func TestRemoteStatusForwarder_TooOldCursorResyncs(t *testing.T) {
	r := &scriptedRemote{cursor: 900,
		follows:   [][]string{{}, {}},
		followErr: []string{"events: cursor older than the retained log\n"},
	}
	rec := &pubRec{}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: rec.publish, since: 3, startTS: time.Now().UnixMilli()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	time.Sleep(2500 * time.Millisecond)
	cancel()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	var follows []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "events follow") {
			follows = append(follows, c)
		}
	}
	if len(follows) < 2 || !strings.Contains(follows[1], "--since 900") {
		t.Fatalf("after a too-old cursor the next follow is %v, want --since 900 (re-read from the remote)", follows)
	}
}
