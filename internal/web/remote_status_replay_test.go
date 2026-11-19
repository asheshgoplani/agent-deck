package web

// Round 3 of #3174: after a reconnect, the fresh listing is the remote's
// state; a replayed frame from before that listing must not undo it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// awayRemote: the first connection lists b1 and gone at cursor 7 and drops.
// While this host is away, gone changes (cursor 8) and is then deleted, so
// the second connection lists only b1 at cursor 9 and its follow (--since 7)
// replays the cursor 8 frame for gone.
type awayRemote struct {
	mu       sync.Mutex
	follows  int
	replayed chan struct{}
}

func (a *awayRemote) RunReadIO(ctx context.Context, _ io.Reader, stdout, _ io.Writer, _ []byte, _ bool, args ...string) error {
	a.mu.Lock()
	n := a.follows
	if len(args) >= 2 && args[0] == "events" && args[1] == "stats" {
		a.mu.Unlock()
		c := 7
		if n > 0 {
			c = 9
		}
		_, err := fmt.Fprintf(stdout, `{"enabled":true,"cursor":%d}`+"\n", c)
		return err
	}
	a.follows++
	a.mu.Unlock()
	if n == 0 {
		return errors.New("connection reset") // the link drops
	}
	if n == 1 {
		_, _ = io.WriteString(stdout, statusLine(8, time.Now().UnixMilli(), "gone", "waiting", "")+"\n")
		close(a.replayed)
	}
	<-ctx.Done()
	return nil
}

func (a *awayRemote) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.follows == 0 {
		return []session.RemoteSessionInfo{{ID: "b1", Title: "beta", Status: "idle"}, {ID: "gone", Title: "old", Status: "running"}}, nil, nil
	}
	return []session.RemoteSessionInfo{{ID: "b1", Title: "beta", Status: "idle"}}, nil, nil
}

// The reviewer's case: a session deleted while the link was down stays out
// of hello.remotes after the reconnect, even though the follow replays an
// older status frame for it.
func TestStateEvents_HelloRemotesSessionDeletedWhileDisconnected(t *testing.T) {
	t.Setenv("CLAUDE_VOICE_STATE_DIR", t.TempDir())
	r := &awayRemote{replayed: make(chan struct{})}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "_rr3_deleted"})
	srv.SetRemoteStatusStream([]string{"box"}, func(string) RemoteStatusRunner { return r })
	srv.startRemoteStatusForward(srv.remoteFwdNames, srv.remoteFwdRunner)
	defer srv.stopRemoteStatusForward()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.stopVoiceProducer()
	select {
	case <-r.replayed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second follow never replayed")
	}
	time.Sleep(300 * time.Millisecond) // the replayed frame has been read
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evs := make(chan sseEvent, 16)
	sseClient(t, ctx, ts.URL+"/events/state", "", evs)
	select {
	case ev := <-evs:
		if ev.event != "hello" {
			t.Fatalf("first event %q", ev.event)
		}
		if strings.Contains(string(ev.data), `"gone"`) {
			t.Fatalf("a session deleted while disconnected came back in hello.remotes from a replayed older frame: %s", ev.data)
		}
		if !strings.Contains(string(ev.data), `"remotes":[{"host":"box","sessions":[{"id":"b1","status":"idle","title":"beta"}],"up":true}]`) {
			t.Fatalf("hello.remotes is not the fresh listing: %s", ev.data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no hello")
	}
}

// The same replay on the stream itself: a frame the fresh listing already
// covers is not republished after the session.link up, so a client applying
// frames in order does not bring the deleted session back either.
func TestRemoteStatusForwarder_ReplayBeforeSnapshotNotRepublished(t *testing.T) {
	r := &awayRemote{replayed: make(chan struct{})}
	rec := &pubRec{}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: rec.publish}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	select {
	case <-r.replayed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second follow never replayed")
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	got := rec.wait(t, 0, 0)
	for _, g := range got {
		if strings.HasPrefix(g, "session.status gone ") {
			t.Fatalf("a frame from before the fresh listing was republished after it:\n%s", strings.Join(got, "\n"))
		}
	}
	if f.since != 8 {
		t.Fatalf("resume cursor %d, want 8 (the replayed frame still moves it)", f.since)
	}
}

// Every merge into the stored snapshot is ordered by the remote cursor: a
// frame at or before the listing, or older than one already applied, does
// not overwrite newer state or add a session back.
func TestNoteRemoteStatus_OlderFramesDoNotOverwrite(t *testing.T) {
	s := &Server{}
	s.noteRemoteLink(SessionLinkEvent{Host: "box", Up: true, Cursor: 10, Sessions: []SessionLinkStatus{{ID: "b1", Status: "idle", Title: "beta"}}})
	s.noteRemoteStatus("box", "gone", "running", 9) // before the listing: gone was deleted since
	s.noteRemoteStatus("box", "b1", "running", 10)  // the listing already covers it
	s.noteRemoteStatus("box", "b1", "waiting", 12)
	s.noteRemoteStatus("box", "b1", "running", 11) // out of order: older than 12
	s.noteRemoteStatus("box", "new", "running", 13)
	got := s.remoteLinks()
	if len(got) != 1 {
		t.Fatalf("links %+v", got)
	}
	want := []SessionLinkStatus{{ID: "b1", Status: "waiting", Title: "beta"}, {ID: "new", Status: "running"}}
	if fmt.Sprint(got[0].Sessions) != fmt.Sprint(want) {
		t.Fatalf("sessions %+v, want %+v", got[0].Sessions, want)
	}
}
