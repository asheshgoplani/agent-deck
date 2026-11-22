package web

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The 32-stream cap counts streams whose peer is gone but whose socket
// still accepts writes (a live SSH tunnel whose far side dropped, or a
// stalled reader). They are never reclaimed, so after 32 such drops every
// new stream is refused, even with the heartbeat firing many times.
func TestReview2_CapHeldByDeadPeers(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	old := timelineStreamHeartbeat
	timelineStreamHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { timelineStreamHeartbeat = old })
	ts := newStreamServer(t, f)
	addr := strings.TrimPrefix(ts.URL, "http://")
	for i := 0; i < int(timelineStreamMax); i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		fmt.Fprintf(c, "GET /api/sessions/%s/timeline/stream?after=end HTTP/1.1\r\nHost: localhost\r\n\r\n", f.inst.ID)
		// The client process is gone; the tunnel endpoint keeps the socket.
	}
	time.Sleep(2 * time.Second) // ~100 heartbeats
	st := f.openStream(t, ts, "after=end", nil)
	if st.resp.StatusCode != http.StatusOK {
		t.Fatalf("after %d dead peers and ~100 heartbeats a live client gets %d: the cap never reclaims dead streams (timelineStreams=%d)", timelineStreamMax, st.resp.StatusCode, f.srv.timelineStreams.Load())
	}
}
