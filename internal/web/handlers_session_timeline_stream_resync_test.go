package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// testRowsCursor builds a v2 rows cursor by hand, for cursors the server
// would never hand out (another file, an offset past the end, a wrong
// anchor).
func testRowsCursor(t *testing.T, path string, off int64, anchor string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"v": 2, "p": path, "o": off, "a": anchor})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// A request whose resume cursor the stream cannot use is answered with
// resync_required without taking a slot: at the cap it does not evict the
// open stream, which keeps receiving rows afterwards. This holds for the
// after parameter and for a Last-Event-ID header, and for every reason the
// cursor check gives.
func TestSessionTimelineStream_BadCursorKeepsStreams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reason    string
		request   func(f *timelineFixture) (rawQuery string, header http.Header)
		wantStart bool // the bad cursor still decodes, so it is sent as the start id
	}{
		{"after invalid", "invalid_cursor", func(f *timelineFixture) (string, http.Header) {
			return "after=not-a-cursor", nil
		}, false},
		{"Last-Event-ID invalid", "invalid_cursor", func(f *timelineFixture) (string, http.Header) {
			return "after=end", http.Header{"Last-Event-Id": {"not-a-cursor"}}
		}, false},
		{"after other file", "source_moved", func(f *timelineFixture) (string, http.Header) {
			return "after=" + testRowsCursor(t, f.path+".other", 0, ""), nil
		}, true},
		{"Last-Event-ID past the end", "source_shortened", func(f *timelineFixture) (string, http.Header) {
			return "after=end", http.Header{"Last-Event-Id": {testRowsCursor(t, f.path, 1<<40, "")}}
		}, true},
		{"after rewritten", "source_rewritten", func(f *timelineFixture) (string, http.Header) {
			return "after=" + testRowsCursor(t, f.path, 1, "not-the-anchor"), nil
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := timelineStreamMax
			timelineStreamMax = 1
			t.Cleanup(func() { timelineStreamMax = old })
			f := newTimelineFixture(t, "claude", true)
			f.appendTurns(t, 0, 1)
			ts := newStreamServer(t, f)
			live := f.openStream(t, ts, "after=end", nil)
			if live.resp.StatusCode != http.StatusOK {
				t.Fatalf("live stream status %d", live.resp.StatusCode)
			}
			if start := live.next(t); start.ID == "" {
				t.Fatalf("first block = %+v, want the start id", start)
			}

			rawQuery, header := tc.request(f)
			bad := f.openStream(t, ts, rawQuery, header)
			if bad.resp.StatusCode != http.StatusOK {
				t.Fatalf("bad cursor: status %d, want 200 with resync_required", bad.resp.StatusCode)
			}
			first := bad.next(t)
			if tc.wantStart {
				if first.ID == "" || first.Event != "" {
					t.Fatalf("bad cursor: first block = %+v, want the start id", first)
				}
				first = bad.next(t)
			}
			var fr query.RowFrame
			if err := json.Unmarshal([]byte(first.Data), &fr); err != nil || first.Event != "resync_required" || fr.Frame != "resync_required" || fr.Reason != tc.reason {
				t.Fatalf("bad cursor: block = %+v, want resync_required %s", first, tc.reason)
			}
			select {
			case _, ok := <-bad.events:
				if ok {
					t.Fatal("bad cursor: stream continued after resync_required")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("bad cursor: stream did not end after resync_required")
			}

			f.appendTurns(t, 1, 1)
			select {
			case ev, ok := <-live.events:
				if !ok {
					t.Fatalf("a resync_required answer (%s) evicted the open stream", tc.reason)
				}
				var fr query.RowFrame
				if err := json.Unmarshal([]byte(ev.Data), &fr); err != nil || fr.Frame != "row" || fr.Row == nil || fr.Row.Body != "user turn 1" {
					t.Fatalf("next event on the open stream = %+v", ev)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("open stream got no row within 5s after the bad-cursor request")
			}
			if n := f.srv.timelineStreams.Load(); n != 1 {
				t.Fatalf("open streams = %d, want 1", n)
			}
		})
	}
}
