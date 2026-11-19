package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var (
	// timelineStreamPoll is how often the stream checks the transcript.
	timelineStreamPoll = 200 * time.Millisecond
	// timelineStreamHeartbeat is how often an idle stream sends an SSE
	// comment, so proxies keep the connection and a dead peer is noticed.
	timelineStreamHeartbeat = 15 * time.Second
	// timelineStreamStatusEvery bounds how often status=true reloads the
	// menu snapshot; the menu stream polls at the same rate.
	timelineStreamStatusEvery = 2 * time.Second
	// timelineStreamMax caps the timeline streams one server keeps open.
	// Each stream parses what is appended to its transcript on its own, so
	// cost grows with streams times bytes; past the cap a new stream is
	// refused with 429 until a client disconnects.
	timelineStreamMax int64 = 32
)

// handleSessionTimelineStream is GET /api/sessions/{id}/timeline/stream,
// the HTTP form of `agent-deck recall follow <id> --after <cursor|end>
// --jsonl [--status]`. Each SSE event is one follow frame: the event name
// is the frame (row, update, remove, status, resync_required) and data is
// the frame's JSON, exactly the line the CLI prints. It sits behind the
// same token check as the timeline route (handleSessionByAction).
//
// Parameters: after (a timeline through_cursor, next_cursor or frame
// cursor, or "end"; a Last-Event-ID header wins, so an EventSource that
// reconnects resumes), kinds and skip-empty-body as on the timeline route,
// and status=true for status frames. Every frame cursor is also the SSE
// event id. With a filter, the cursor of a frame the filter holds back is
// sent as an id-only event (no data): it is the stream's next_cursor. The
// stream ends after resync_required, or when the client disconnects.
//
// The first block is always an id-only event with the start cursor (the
// resolved "end", or after itself), so a client that drops before the
// first frame reconnects from it. An open stream is not in-flight work
// (isStreamPath), and at most timelineStreamMax are open per server.
func (s *Server) handleSessionTimelineStream(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if cfg, _ := session.LoadUserConfig(); cfg == nil || !cfg.Recall.GetEnabled() {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeRecallDisabled, "recall is off: set [recall] enabled = true in config.toml (docs/recall.md)")
		return
	}
	q := r.URL.Query()
	after := r.Header.Get("Last-Event-ID")
	if after == "" {
		after = q.Get("after")
	}
	if after == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "after is required: a timeline cursor or end")
		return
	}
	var opts query.RowsOptions
	if err := timelineFilter(q, &opts); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	withStatus := false
	if v := q.Get("status"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "status must be true or false")
			return
		}
		withStatus = b
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "stream unavailable")
		return
	}
	if s.timelineStreams.Add(1) > timelineStreamMax {
		s.timelineStreams.Add(-1)
		writeAPIError(w, http.StatusTooManyRequests, ErrCodeRateLimited, fmt.Sprintf("too many open timeline streams (at most %d); close one and retry", timelineStreamMax))
		return
	}
	defer s.timelineStreams.Add(-1)
	src, _, _, ok := s.timelineSource(w, sessionID, opts)
	if !ok {
		return
	}
	if after == "end" {
		// Resolve the end with the stream's own filter: a filtered read's
		// cursor lists the open rows it dropped (a tool call still waiting
		// for its result), so their later patches stay off the wire.
		tl, err := query.ReadRows(r.Context(), src, query.RowsOptions{Tail: 1, Kinds: opts.Kinds, SkipEmptyBody: opts.SkipEmptyBody})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to read the session transcript")
			return
		}
		after = tl.ThroughCursor
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// A failed write (peer gone, but the request context not yet
	// cancelled) ends the follow through ctx as well.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := &sseWriter{w: w, flusher: flusher, last: time.Now()}
	filter := query.NewFollowFilter(after, opts)
	if start := filter.Start(); start != "" {
		if err := out.idOnly(start); err != nil {
			return
		}
	}
	var status func() *query.LiveStatus
	if withStatus {
		status = s.timelineStatus(sessionID)
	}
	heartbeat := func() []query.RowFrame {
		if time.Since(out.last) >= timelineStreamHeartbeat {
			if err := out.comment("keepalive"); err != nil {
				cancel()
			}
		}
		return nil
	}
	_ = query.FollowRows(ctx, src, after, timelineStreamPoll, status, heartbeat, func(fr query.RowFrame) error {
		fr, send := filter.Apply(fr)
		if !send {
			if fr.Cursor == "" {
				return nil
			}
			return out.idOnly(fr.Cursor)
		}
		return out.frame(fr)
	})
}

// timelineStatus answers status frames from the menu snapshot the web UI
// shows (hook overlay included), reloaded at most every
// timelineStreamStatusEvery. Pane facts (verb, tokens, tool) are not read.
func (s *Server) timelineStatus(sessionID string) func() *query.LiveStatus {
	var last *query.LiveStatus
	var at time.Time
	return func() *query.LiveStatus {
		if last != nil && time.Since(at) < timelineStreamStatusEvery {
			return last
		}
		at = time.Now()
		snapshot, err := s.menuData.LoadMenuSnapshot()
		if err != nil || snapshot == nil {
			return last
		}
		refreshSnapshotHookStatuses(snapshot, s.hookStatusLoader)
		for _, item := range snapshot.Items {
			if item.Type == MenuItemTypeSession && item.Session != nil && item.Session.ID == sessionID {
				st := string(item.Session.Status)
				last = &query.LiveStatus{SessionID: sessionID, SessionStatus: st, Running: st == string(session.StatusRunning)}
				break
			}
		}
		return last
	}
}

type sseWriter struct {
	w       io.Writer
	flusher http.Flusher
	last    time.Time
}

func (o *sseWriter) write(format string, args ...any) error {
	if _, err := fmt.Fprintf(o.w, format, args...); err != nil {
		return err
	}
	o.flusher.Flush()
	o.last = time.Now()
	return nil
}

func (o *sseWriter) frame(fr query.RowFrame) error {
	data, err := json.Marshal(fr)
	if err != nil {
		return err
	}
	if fr.Cursor != "" {
		return o.write("id: %s\nevent: %s\ndata: %s\n\n", fr.Cursor, fr.Frame, data)
	}
	return o.write("event: %s\ndata: %s\n\n", fr.Frame, data)
}

// idOnly moves the client's resume point without an event: an EventSource
// sets lastEventId from a block with no data and dispatches nothing.
func (o *sseWriter) idOnly(cursor string) error { return o.write("id: %s\n\n", cursor) }

func (o *sseWriter) comment(text string) error { return o.write(": %s\n\n", text) }
