package web

// Round 4 of #3174: a remote whose event log is recreated starts its cursor
// again from a low number. The resume cursor must follow it back down, or
// every new status is at or below the old resume cursor and is dropped.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// recreatedRemote: the first connection is at cursor 50 and forwards a
// frame at 51, then drops. The remote's log is then recreated: the second
// connection's cursor is 3, its log holds a frame at 3 (before the listing)
// and a new one at 4. Like the real follow, it sends only frames after
// --since.
type recreatedRemote struct {
	mu      sync.Mutex
	conns   int
	follows []string
	sent    chan struct{}
}

func (r *recreatedRemote) RunReadIO(ctx context.Context, _ io.Reader, stdout, _ io.Writer, _ []byte, _ bool, args ...string) error {
	r.mu.Lock()
	n := r.conns
	if len(args) >= 2 && args[0] == "events" && args[1] == "stats" {
		r.mu.Unlock()
		c := 50
		if n > 0 {
			c = 3 // the log was recreated
		}
		_, err := fmt.Fprintf(stdout, `{"enabled":true,"cursor":%d}`+"\n", c)
		return err
	}
	r.conns++
	r.follows = append(r.follows, strings.Join(args, " "))
	r.mu.Unlock()
	var since uint64
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--since" {
			since, _ = strconv.ParseUint(args[i+1], 10, 64)
		}
	}
	now := time.Now().UnixMilli()
	var lines []string
	if n == 0 {
		lines = []string{statusLine(51, now, "b1", "running", "")}
	} else {
		lines = []string{
			statusLine(3, now, "old", "running", ""), // before the fresh listing
			statusLine(4, now, "b1", "waiting", ""),  // new after the recreate
		}
	}
	for _, l := range lines {
		fr, err := events.ParseFrameLine([]byte(l))
		if err != nil || uint64(fr.Cursor) <= since {
			continue
		}
		if _, err := io.WriteString(stdout, l+"\n"); err != nil {
			return err
		}
	}
	if n == 0 {
		return errors.New("connection reset")
	}
	if n == 1 {
		close(r.sent)
	}
	<-ctx.Done()
	return nil
}

func (r *recreatedRemote) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	return []session.RemoteSessionInfo{{ID: "b1", Title: "beta", Status: "idle"}}, nil, nil
}

func TestRemoteStatusForwarder_LogRecreatedResumesFromNewCursor(t *testing.T) {
	r := &recreatedRemote{sent: make(chan struct{})}
	rec := &pubRec{}
	f := &remoteStatusForwarder{name: "box", runner: r, publish: rec.publish}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	select {
	case <-r.sent:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("the second follow never ran")
	}
	time.Sleep(300 * time.Millisecond) // the frames sent have been read
	cancel()
	<-done
	got := rec.wait(t, 0, 0)
	joined := strings.Join(got, "\n")
	r.mu.Lock()
	follows := append([]string(nil), r.follows...)
	r.mu.Unlock()
	if len(follows) < 2 || !strings.Contains(follows[1], "--since 3") {
		t.Fatalf("follows after the log was recreated: %v, want the second at --since 3 (the remote's new cursor)", follows)
	}
	var newStatus bool
	for _, g := range got {
		if strings.HasPrefix(g, "session.status old ") {
			t.Fatalf("a frame from before the fresh listing was republished:\n%s", joined)
		}
		if strings.HasPrefix(g, "session.status b1 ") && strings.Contains(g, `"remote_cursor":4`) {
			newStatus = true
		}
	}
	if !newStatus {
		t.Fatalf("the new status after the log was recreated (cursor 4) was dropped:\n%s", joined)
	}
	if f.since != 4 {
		t.Fatalf("resume cursor %d, want 4", f.since)
	}
}
