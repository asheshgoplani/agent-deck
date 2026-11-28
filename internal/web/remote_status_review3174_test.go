package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// failingFollow: the cursor read and the listing work, every follow fails
// at once (a remote whose bus is broken, or too old for a flag).
type failingFollow struct {
	mu      sync.Mutex
	follows [][]string
}

func (f *failingFollow) FetchSessions(context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error) {
	return nil, nil, nil
}

func (f *failingFollow) RunReadIO(_ context.Context, _ io.Reader, stdout, _ io.Writer, _ []byte, follow bool, args ...string) error {
	if len(args) >= 2 && args[0] == "events" && args[1] == "stats" {
		_, err := io.WriteString(stdout, `{"cursor":1}`+"\n")
		return err
	}
	f.mu.Lock()
	f.follows = append(f.follows, append([]string(nil), args...))
	f.mu.Unlock()
	return fmt.Errorf("remote follow failed")
}

// 3174 review F1: a follow that fails right after a good resync backs off
// (doubling to the cap) instead of flapping up and down every 250 ms.
func TestRemoteStatusForwarder_FailingFollowBacksOff(t *testing.T) {
	fake := &failingFollow{}
	var mu sync.Mutex
	ups := 0
	f := &remoteStatusForwarder{name: "box", runner: fake,
		publish: func(kind, _ string, data any) {
			if l, ok := data.(SessionLinkEvent); ok && kind == KindSessionLink && l.Up {
				mu.Lock()
				ups++
				mu.Unlock()
			}
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f.run(ctx)
	mu.Lock()
	defer mu.Unlock()
	// 0 s, 0.25, 0.75, 1.75 with doubling: four rounds; 250 ms each: eight
	if ups > 5 {
		t.Errorf("%d link-up rounds in 2 s with a follow that fails at once; want backoff (at most 5)", ups)
	}
}

// 3174 review F2: the long-running remote follow is a read-only observer:
// no send-worker recovery, no writer on the remote.
func TestRemoteStatusForwarder_FollowIsReadOnly(t *testing.T) {
	fake := &failingFollow{}
	f := &remoteStatusForwarder{name: "box", runner: fake, publish: func(string, string, any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	f.run(ctx)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.follows) == 0 {
		t.Fatal("no follow")
	}
	if s := strings.Join(fake.follows[0], " "); !strings.Contains(s, "--read-only") {
		t.Errorf("follow args %q lack --read-only", s)
	}
}

// 3174 review F3: only the session.status fields cross; anything else a
// remote puts in the frame (message text, a token) is dropped.
func TestRemoteStatusForwarder_ForwardsOnlyStatusFields(t *testing.T) {
	var got any
	f := &remoteStatusForwarder{name: "box", publish: func(_, _ string, data any) { got = data }}
	f.forward([]byte(`{"cursor":7,"data":{"from":"idle","to":"waiting","tool":"claude","tmux_session":"agentdeck_x","substate":"menu","changed_at":"2026-11-28T10:00:00Z","host":"","message":"SECRET text","token":"abc"},"event_id":"e7","kind":"session.status","session_id":"b1","ts":1}`))
	if got == nil {
		t.Fatal("nothing forwarded")
	}
	raw, _ := json.Marshal(got)
	for _, leak := range []string{"SECRET", "message", "token", "abc"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("forwarded frame carries %q: %s", leak, raw)
		}
	}
	for _, want := range []string{`"from":"idle"`, `"to":"waiting"`, `"tool":"claude"`, `"tmux_session":"agentdeck_x"`, `"substate":"menu"`, `"changed_at":"2026-11-28T10:00:00Z"`, `"host":"box"`, `"remote_cursor":7`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("forwarded frame lacks %s: %s", want, raw)
		}
	}
}

// Codex 3174 #3: a named remote with no runner (the config reload failed
// or the remote is gone) is said as a link down, not silently skipped.
func TestRemoteStatusForward_NoRunnerIsLinkDown(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "_r3174_norunner"})
	srv.startRemoteStatusForward([]string{"gone"}, func(string) RemoteStatusRunner { return nil })
	defer srv.stopRemoteStatusForward()
	links := srv.remoteLinks()
	if len(links) != 1 || links[0].Host != "gone" || links[0].Up {
		t.Errorf("links %+v, want one link down for gone", links)
	}
}
