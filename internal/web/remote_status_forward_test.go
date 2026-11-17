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
)

// fakeRemoteFollow plays one scripted connection per call and records the
// --since it was asked for.
type fakeRemoteFollow struct {
	mu     sync.Mutex
	calls  [][]string
	script [][]string // lines per connection
}

func (f *fakeRemoteFollow) RunReadIO(ctx context.Context, stdin io.Reader, stdout, _ io.Writer, _ []byte, follow bool, args ...string) error {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, append([]string(nil), args...))
	var lines []string
	if n < len(f.script) {
		lines = f.script[n]
	}
	f.mu.Unlock()
	if !follow {
		return fmt.Errorf("not a follow")
	}
	if stdin == nil {
		return fmt.Errorf("follow without an open stdin ends at once on the remote")
	}
	for _, l := range lines {
		if _, err := io.WriteString(stdout, l+"\n"); err != nil {
			return err
		}
	}
	if n >= len(f.script) {
		<-ctx.Done() // the last connection stays up
	}
	return fmt.Errorf("connection dropped")
}

func statusLine(cursor int, ts int64, sid, to, host string) string {
	data := fmt.Sprintf(`{"from":"idle","to":%q,"changed_at":"x","host":%q}`, to, host)
	return fmt.Sprintf(`{"cursor":%d,"data":%s,"event_id":"e%d","kind":"session.status","session_id":%q,"ts":%d}`, cursor, data, cursor, sid, ts)
}

// A remote's session.status frames are republished with host = the
// remote's name; its history before the start, frames that already carry a
// host (a forward) and other kinds are not; a dropped connection resumes
// from the last cursor, so nothing is doubled.
func TestRemoteStatusForwarder_RepublishesWithHost(t *testing.T) {
	start := time.Now().UnixMilli()
	fake := &fakeRemoteFollow{script: [][]string{
		{
			statusLine(1, start-60_000, "old", "running", ""), // history
			statusLine(2, start+10, "b1", "running", ""),
			statusLine(3, start+20, "b2", "waiting", "elsewhere"), // a forward on the remote: no loop
			`{"cursor":4,"data":{"phase":"listening"},"event_id":"e4","kind":"voice.state","session_id":"","ts":` + fmt.Sprint(start+30) + `}`,
		},
		{
			statusLine(2, start+10, "b1", "running", ""), // replay after the drop: skipped
			statusLine(5, start+40, "b1", "waiting", ""),
		},
	}}
	var mu sync.Mutex
	var got []string
	f := &remoteStatusForwarder{name: "box", runner: fake, startTS: start,
		publish: func(kind, sid string, data any) {
			raw, _ := json.Marshal(data)
			mu.Lock()
			got = append(got, kind+" "+sid+" "+string(raw))
			mu.Unlock()
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("published %d frames, want 2:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, want := range []string{`session.status b1 `, `session.status b1 `} {
		if !strings.HasPrefix(got[i], want) || !strings.Contains(got[i], `"host":"box"`) || !strings.Contains(got[i], `"remote_cursor":`) {
			t.Errorf("frame %d = %s", i, got[i])
		}
	}
	if !strings.Contains(got[0], `"to":"running"`) || !strings.Contains(got[1], `"to":"waiting"`) {
		t.Errorf("order: %v", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) < 2 {
		t.Fatalf("no reconnect: %v", fake.calls)
	}
	if s := strings.Join(fake.calls[1], " "); !strings.Contains(s, "--since 4") || !strings.Contains(s, "--kind session.status") {
		t.Errorf("reconnect args %q, want --since 4 (last cursor seen) and --kind session.status", s)
	}
}
