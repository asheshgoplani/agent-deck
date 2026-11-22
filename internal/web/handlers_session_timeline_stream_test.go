package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// sseEvent is one SSE block: an id-only block has no Event and no Data.
type sseEvent struct {
	ID, Event, Data string
}

// timelineStream is an open GET /timeline/stream against a real listener.
type timelineStream struct {
	resp   *http.Response
	events chan sseEvent
	cancel context.CancelFunc
}

func (f *timelineFixture) openStream(t *testing.T, ts *httptest.Server, rawQuery string, header http.Header) *timelineStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/sessions/"+f.inst.ID+"/timeline/stream?"+rawQuery, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Host = "localhost"
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	st := &timelineStream{resp: resp, events: make(chan sseEvent, 64), cancel: cancel}
	t.Cleanup(func() { st.close() })
	if resp.StatusCode != http.StatusOK {
		return st
	}
	go func() {
		defer close(st.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		seen := false
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if seen {
					st.events <- ev
				}
				ev, seen = sseEvent{}, false
			case strings.HasPrefix(line, ":"):
			case strings.HasPrefix(line, "id: "):
				ev.ID, seen = strings.TrimPrefix(line, "id: "), true
			case strings.HasPrefix(line, "event: "):
				ev.Event, seen = strings.TrimPrefix(line, "event: "), true
			case strings.HasPrefix(line, "data: "):
				ev.Data, seen = strings.TrimPrefix(line, "data: "), true
			}
		}
	}()
	return st
}

func (st *timelineStream) close() {
	st.cancel()
	_ = st.resp.Body.Close()
}

// next returns the next SSE block, failing after a deadline.
func (st *timelineStream) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-st.events:
		if !ok {
			t.Fatal("stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE event within 5s")
	}
	return sseEvent{}
}

// nextFrame skips id-only blocks and returns the next frame with its id.
func (st *timelineStream) nextFrame(t *testing.T) (query.RowFrame, sseEvent) {
	t.Helper()
	for {
		ev := st.next(t)
		if ev.Event == "" && ev.Data == "" {
			continue
		}
		var fr query.RowFrame
		if err := json.Unmarshal([]byte(ev.Data), &fr); err != nil {
			t.Fatalf("frame %q: %v", ev.Data, err)
		}
		if fr.Frame != ev.Event {
			t.Fatalf("event %q carries frame %q", ev.Event, fr.Frame)
		}
		return fr, ev
	}
}

func newStreamServer(t *testing.T, f *timelineFixture) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// The stream sends the rows appended after the cursor as row frames, each
// event named after its frame and carrying the frame's cursor as its id.
func TestSessionTimelineStream_RowFramesAfterCursor(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi"} {
		t.Run(harness, func(t *testing.T) {
			f := newTimelineFixture(t, harness, true)
			f.appendTurns(t, 0, 1)
			cursor := decodeTimeline(t, f.get(t, "")).ThroughCursor
			st := f.openStream(t, newStreamServer(t, f), "after="+url.QueryEscape(cursor), nil)
			if st.resp.StatusCode != http.StatusOK || !strings.HasPrefix(st.resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("status %d, content type %q", st.resp.StatusCode, st.resp.Header.Get("Content-Type"))
			}
			f.appendTurns(t, 1, 1)
			var bodies []string
			var last sseEvent
			for len(bodies) < 2 {
				fr, ev := st.nextFrame(t)
				if fr.Frame != "row" {
					t.Fatalf("frame = %+v", fr)
				}
				bodies = append(bodies, fr.Row.Body)
				last = ev
			}
			if want := []string{"user turn 1", "assistant turn 1"}; strings.Join(bodies, "|") != strings.Join(want, "|") {
				t.Fatalf("bodies = %q, want %q", bodies, want)
			}
			// The last event id resumes the snapshot route after both rows.
			if last.ID == "" {
				t.Fatal("last row event has no id")
			}
			if tl := decodeTimeline(t, f.get(t, "since="+url.QueryEscape(last.ID))); len(tl.Turns) != 0 {
				t.Fatalf("resume from stream id re-sent %q", rowTexts(tl.Turns))
			}
		})
	}
}

// after=end starts at the current end; a Last-Event-ID header (an
// EventSource reconnecting) wins over the after parameter.
func TestSessionTimelineStream_EndAndLastEventID(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 2)
	ts := newStreamServer(t, f)
	st := f.openStream(t, ts, "after=end", nil)
	if st.resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", st.resp.StatusCode)
	}
	f.appendTurns(t, 2, 1)
	if fr, _ := st.nextFrame(t); fr.Frame != "row" || fr.Row.Body != "user turn 2" {
		t.Fatalf("first frame after end = %+v", fr)
	}

	tl := decodeTimeline(t, f.get(t, "limit=2"))
	resumed := f.openStream(t, ts, "after=end", http.Header{"Last-Event-ID": {tl.ThroughCursor}})
	if fr, _ := resumed.nextFrame(t); fr.Frame != "row" || fr.Row.Body != "user turn 1" {
		t.Fatalf("Last-Event-ID resume = %+v", fr)
	}
}

// kinds and skip-empty-body filter the stream like the snapshot route,
// and the cursor of every held-back frame still reaches the client as an
// id-only event, so resuming never re-reads the filtered window.
func TestSessionTimelineStream_KindsFilter(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	cursor := decodeTimeline(t, f.get(t, "")).ThroughCursor
	st := f.openStream(t, newStreamServer(t, f), "kinds=user&skip-empty-body&after="+url.QueryEscape(cursor), nil)
	if st.resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", st.resp.StatusCode)
	}
	f.appendTurns(t, 1, 2)
	var users []string
	lastID := ""
	for {
		ev := st.next(t)
		if ev.ID != "" {
			lastID = ev.ID
		}
		if ev.Event == "" {
			if ev.Data != "" {
				t.Fatalf("data without event: %+v", ev)
			}
			if lastID == decodeTimeline(t, f.get(t, "")).ThroughCursor && len(users) == 2 {
				break
			}
			continue
		}
		var fr query.RowFrame
		if err := json.Unmarshal([]byte(ev.Data), &fr); err != nil {
			t.Fatal(err)
		}
		if fr.Frame != "row" || fr.Row.Kind != "user" {
			t.Fatalf("filter let through %+v", fr)
		}
		users = append(users, fr.Row.Body)
	}
	if strings.Join(users, "|") != "user turn 1|user turn 2" {
		t.Fatalf("users = %q", users)
	}
	tl := decodeTimeline(t, f.get(t, "kinds=user&since="+url.QueryEscape(lastID)))
	if len(tl.Turns) != 0 {
		t.Fatalf("resume from the held-back cursor re-sent %q", rowTexts(tl.Turns))
	}
}

// The snapshot route takes the CLI's filter flags under the same names and
// answers next_cursor; resuming from it walks forward.
func TestSessionTimeline_KindsFilter(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 3)
	tl := decodeTimeline(t, f.get(t, "kinds=assistant&limit=2"))
	if got := strings.Join(rowTexts(tl.Turns), "|"); got != "assistant turn 0|assistant turn 1" {
		t.Fatalf("kinds=assistant&limit=2 = %q", got)
	}
	if tl.NextCursor == "" || tl.NextCursor != tl.ThroughCursor {
		t.Fatalf("next_cursor = %q, through_cursor = %q", tl.NextCursor, tl.ThroughCursor)
	}
	rest := decodeTimeline(t, f.get(t, "kinds=assistant&since="+url.QueryEscape(tl.NextCursor)))
	if got := strings.Join(rowTexts(rest.Turns), "|"); got != "assistant turn 2" {
		t.Fatalf("resumed page = %q", got)
	}
	if all := decodeTimeline(t, f.get(t, "skip-empty-body=true")); len(all.Turns) != 6 || all.NextCursor == "" {
		t.Fatalf("skip-empty-body=true: %d rows, next_cursor %q", len(all.Turns), all.NextCursor)
	}
	for _, q := range []string{"kinds=", "kinds=user,nope", "skip-empty-body=maybe"} {
		wantAPIError(t, f.get(t, q), http.StatusBadRequest, "INVALID_REQUEST")
		qs := q + "&after=end"
		rr := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/sessions/"+f.inst.ID+"/timeline/stream?"+qs, nil))
		wantAPIError(t, rr, http.StatusBadRequest, "INVALID_REQUEST")
	}
}

// The stream refuses what the timeline route refuses.
func TestSessionTimelineStream_Errors(t *testing.T) {
	getID := func(f *timelineFixture, id, method, rawQuery string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		f.srv.Handler().ServeHTTP(rr, newLocalRequest(method, "/api/sessions/"+id+"/timeline/stream?"+rawQuery, nil))
		return rr
	}
	get := func(f *timelineFixture, method, rawQuery string) *httptest.ResponseRecorder {
		return getID(f, f.inst.ID, method, rawQuery)
	}
	off := newTimelineFixture(t, "claude", false)
	wantAPIError(t, get(off, http.MethodGet, "after=end"), http.StatusServiceUnavailable, "RECALL_DISABLED")

	f := newTimelineFixture(t, "claude", true)
	wantAPIError(t, get(f, http.MethodGet, ""), http.StatusBadRequest, "INVALID_REQUEST")
	wantAPIError(t, get(f, http.MethodPost, "after=end"), http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	wantAPIError(t, getID(f, "missing", http.MethodGet, "after=end"), http.StatusNotFound, "NOT_FOUND")
}

// A cursor the stream cannot resume ends it with resync_required, the
// CLI's frame, instead of an HTTP error mid-stream.
func TestSessionTimelineStream_ResyncRequired(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	st := f.openStream(t, newStreamServer(t, f), "after=not-a-cursor", nil)
	if st.resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", st.resp.StatusCode)
	}
	if fr, _ := st.nextFrame(t); fr.Frame != "resync_required" || fr.Reason != "invalid_cursor" {
		t.Fatalf("frame = %+v", fr)
	}
	select {
	case _, ok := <-st.events:
		if ok {
			t.Fatal("stream continued after resync_required")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end after resync_required")
	}
}

// status=true adds status frames with the session's status.
func TestSessionTimelineStream_Status(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	st := f.openStream(t, newStreamServer(t, f), "status=true&after=end", nil)
	fr, _ := st.nextFrame(t)
	if fr.Frame != "status" || fr.LiveStatus == nil || fr.SessionID != f.inst.ID || fr.SessionStatus == "" {
		t.Fatalf("frame = %+v", fr)
	}
}

// The stream sits behind the timeline route's token check: no token, a
// wrong token, or the token in the query string (which only the
// EventSource-style routes accept) is refused before any frame.
func TestSessionTimelineStream_RequiresToken(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	f.srv.cfg.Token = "secret"
	ts := newStreamServer(t, f)
	for _, tc := range []struct {
		name, auth, query string
		status            int
	}{
		{"missing", "", "", http.StatusUnauthorized},
		{"wrong", "Bearer nope", "", http.StatusUnauthorized},
		{"query token", "", "&token=secret", http.StatusUnauthorized},
		{"right", "Bearer secret", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.auth != "" {
				h.Set("Authorization", tc.auth)
			}
			st := f.openStream(t, ts, "after=end"+tc.query, h)
			if st.resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", st.resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusOK {
				return
			}
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.NewDecoder(st.resp.Body).Decode(&body); err != nil || body.Error.Code != "UNAUTHORIZED" {
				t.Fatalf("body code = %q, err %v", body.Error.Code, err)
			}
		})
	}
}

// When the client disconnects the handler returns and leaves no goroutine
// behind.
func TestSessionTimelineStream_StopsOnDisconnect(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	baseline := goleak.IgnoreCurrent()
	returned := make(chan struct{}, 1)
	handler := f.srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/timeline/stream") {
			returned <- struct{}{}
		}
	}))
	st := f.openStream(t, ts, "status=true&after=end", nil)
	if fr, _ := st.nextFrame(t); fr.Frame != "status" {
		t.Fatalf("frame = %+v", fr)
	}
	select {
	case <-returned:
		t.Fatal("stream handler returned while the client was connected")
	default:
	}
	st.close()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stream handler still running 5s after the client disconnected")
	}
	ts.Close()
	ts.Client().CloseIdleConnections()
	goleak.VerifyNone(t, baseline)
}

// An open timeline stream, like /events/*, does not count as in-flight
// work: a headless web server with a follower open still self-restarts
// into a new build (review F-1).
func TestSessionTimelineStream_DoesNotBlockIdle(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	st := f.openStream(t, newStreamServer(t, f), "status=true&after=end", nil)
	if fr, _ := st.nextFrame(t); fr.Frame != "status" {
		t.Fatalf("frame = %+v", fr)
	}
	if !f.srv.Idle() {
		t.Fatalf("open /timeline/stream counts as in-flight (InFlightRequests=%d)", f.srv.InFlightRequests())
	}
}

// after=end sends its resolved start cursor as an id before any frame, so
// a client that drops before the first frame reconnects with it and gets
// the rows written in between instead of skipping them (review F-2).
func TestSessionTimelineStream_AfterEndSendsStartCursor(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	ts := newStreamServer(t, f)
	st := f.openStream(t, ts, "after=end", nil)
	var start sseEvent
	select {
	case start = <-st.events:
	case <-time.After(2 * time.Second):
		t.Fatal("after=end: no id before the first frame")
	}
	if start.ID == "" || start.Event != "" || start.Data != "" {
		t.Fatalf("first block = %+v, want an id-only block", start)
	}
	st.close()
	f.appendTurns(t, 1, 1) // written while the client is away
	resumed := f.openStream(t, ts, "after=end", http.Header{"Last-Event-ID": {start.ID}})
	if fr, _ := resumed.nextFrame(t); fr.Frame != "row" || fr.Row.Body != "user turn 1" {
		t.Fatalf("resume from the start id = %+v, want the row written while away", fr)
	}
}

const (
	streamToolCall   = `{"type":"assistant","uuid":"a-tool","sessionId":"conversation-1","message":{"id":"m-tool","role":"assistant","content":[{"type":"tool_use","id":"toolu_r","name":"Bash","input":{"command":"make"}}]},"timestamp":"2026-11-18T10:00:00Z"}` + "\n"
	streamToolResult = `{"type":"user","uuid":"r-tool","sessionId":"conversation-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_r","content":"BIG RESULT"}]},"timestamp":"2026-11-18T10:00:01Z"}` + "\n"
)

func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// A filtered after=end stream starts from a filtered read's cursor, so the
// result of a tool call already open at the end stays off the wire for a
// client that filtered tool rows out (review F-3).
func TestSessionTimelineStream_AfterEndFilterKeepsOpenCallResultOff(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	appendRaw(t, f.path, streamToolCall)
	// openStream returns once the response headers arrive, which is after
	// the handler resolved "end", so the call below is open at the start.
	st := f.openStream(t, newStreamServer(t, f), "kinds=user&after=end", nil)
	appendRaw(t, f.path, streamToolResult)
	f.appendTurns(t, 1, 1)
	for {
		fr, ev := st.nextFrame(t)
		if fr.Frame == "update" || strings.Contains(ev.Data, "BIG RESULT") {
			t.Fatalf("kinds=user stream sent the open call's result: %s", ev.Data)
		}
		if fr.Frame == "row" {
			return
		}
	}
}

// Timeline streams are capped per server; past the cap a new one evicts the
// oldest open stream instead of being refused, so slots held by dead peers
// are reclaimed. The evicted client resumes with Last-Event-ID (review F-4).
func TestSessionTimelineStream_CapEvictsOldest(t *testing.T) {
	f := newTimelineFixture(t, "claude", true)
	ts := newStreamServer(t, f)
	var open []*timelineStream
	for i := 0; i < int(timelineStreamMax); i++ {
		st := f.openStream(t, ts, "after=end", nil)
		if st.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: status %d, want 200 within the cap", i+1, st.resp.StatusCode)
		}
		open = append(open, st)
	}
	extra := f.openStream(t, ts, "after=end", nil)
	if extra.resp.StatusCode != http.StatusOK {
		t.Fatalf("stream %d: status %d, want 200 (evicts the oldest)", timelineStreamMax+1, extra.resp.StatusCode)
	}
	// The oldest stream is ended by the server: its event channel closes.
	deadline := time.After(5 * time.Second)
	for closed := false; !closed; {
		select {
		case _, ok := <-open[0].events:
			closed = !ok
		case <-deadline:
			t.Fatal("oldest stream still open 5s after the cap was exceeded")
		}
	}
	if n := f.srv.timelineStreams.Load(); n > timelineStreamMax {
		t.Fatalf("open streams = %d, want at most %d", n, timelineStreamMax)
	}
	// A newer stream is untouched.
	select {
	case _, ok := <-open[1].events:
		if !ok {
			t.Fatal("second-oldest stream was evicted too")
		}
	case <-time.After(100 * time.Millisecond):
	}
	extra.close()
}

// A request that fails validation (here an unknown session) is answered
// before it takes a stream slot, so at the cap it does not evict a live
// stream on its way to a 404.
func TestSessionTimelineStream_RejectedRequestKeepsStreams(t *testing.T) {
	old := timelineStreamMax
	timelineStreamMax = 1
	t.Cleanup(func() { timelineStreamMax = old })
	f := newTimelineFixture(t, "claude", true)
	f.appendTurns(t, 0, 1)
	ts := newStreamServer(t, f)
	st := f.openStream(t, ts, "after=end", nil)
	if st.resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", st.resp.StatusCode)
	}
	if start := st.next(t); start.ID == "" {
		t.Fatalf("first block = %+v, want the start id", start)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sessions/missing/timeline/stream?after=end", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session: status %d, want 404", resp.StatusCode)
	}

	f.appendTurns(t, 1, 1)
	select {
	case ev, ok := <-st.events:
		if !ok {
			t.Fatal("a 404 for an unknown session evicted the open stream")
		}
		var fr query.RowFrame
		if err := json.Unmarshal([]byte(ev.Data), &fr); err != nil || fr.Frame != "row" || fr.Row.Body != "user turn 1" {
			t.Fatalf("next event after the 404 = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open stream got no row within 5s after the 404")
	}
	if n := f.srv.timelineStreams.Load(); n != 1 {
		t.Fatalf("open streams = %d, want 1", n)
	}
}
