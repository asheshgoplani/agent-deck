package web

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// boundaryRemote: the cursor is read at 10; before the listing runs, the
// session changes twice (cursor 11: running, cursor 12: idle), so the
// listing already shows the newest status. The follow from 10 then
// replays both frames.
type boundaryRemote struct{}

func (boundaryRemote) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	return []session.RemoteSessionInfo{{ID: "x1", Title: "alpha", Status: "idle"}}, nil, nil
}

func (boundaryRemote) RunReadIO(ctx context.Context, _ io.Reader, stdout, _ io.Writer, _ []byte, _ bool, args ...string) error {
	if len(args) >= 2 && args[0] == "events" && args[1] == "stats" {
		_, err := io.WriteString(stdout, `{"cursor":10}`+"\n")
		return err
	}
	for _, l := range []string{statusLine(11, 1, "x1", "running", ""), statusLine(12, 2, "x1", "idle", "")} {
		if _, err := io.WriteString(stdout, l+"\n"); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return fmt.Errorf("closed")
}

// A change between the cursor read and the listing arrives twice (in the
// listing and as its frame), never not at all, and the last status a
// client sees is the listing's: every change the listing reflects has its
// own frame after the cursor, and the frames come in cursor order.
func TestRemoteStatusForwarder_ChangeBetweenCursorAndListing(t *testing.T) {
	var mu sync.Mutex
	var order []string
	f := &remoteStatusForwarder{name: "box", runner: boundaryRemote{}, publish: func(kind, sid string, data any) {
		mu.Lock()
		defer mu.Unlock()
		switch d := data.(type) {
		case SessionLinkEvent:
			for _, s := range d.Sessions {
				order = append(order, "snapshot:"+s.ID+":"+s.Status)
			}
		case forwardedStatus:
			order = append(order, fmt.Sprintf("frame:%s:%s:%d", sid, d.To, d.RemoteCursor))
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	f.run(ctx)
	mu.Lock()
	defer mu.Unlock()
	want := []string{"snapshot:x1:idle", "frame:x1:running:11", "frame:x1:idle:12"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("published %v, want %v", order, want)
	}
	if last := order[len(order)-1]; last != "frame:x1:idle:12" {
		t.Errorf("the last status is %q, not the listing's idle", last)
	}
}
