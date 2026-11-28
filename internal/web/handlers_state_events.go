package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
	"github.com/asheshgoplani/agent-deck/internal/voicestream"
)

// GET /events/state: the live state stream (session.status, voice.state,
// voice.result) as server-sent events with resume by cursor. Each bus
// frame is one SSE event with `id: <cursor>`, so a client that reconnects
// with Last-Event-ID (or ?after=<cursor>) gets every frame it missed, none
// twice. Auth is the /events/menu rule (owner token; ?token= allowed on
// streams); it reads only, so it is served in read-only mode too.

var stateEventsHeartbeatInterval = 15 * time.Second

// defaultStateKinds is what the stream carries without ?kind=.
var defaultStateKinds = []string{"session.status", "voice."}

// stateKindsAllowed bounds ?kind=: the stream is for status and voice, not
// a door to every kind on the bus (tmux output, ask dialogs).
var stateKindsAllowed = []string{"session.", "voice."}

// stateResetFrame tells a client its cursor cannot be resumed: re-read the
// snapshot (voice is the state now) and continue from Cursor.
type stateResetFrame struct {
	Reason string                 `json:"reason"`
	Cursor uint64                 `json:"cursor"`
	Voice  voicestream.StateEvent `json:"voice"`
}

// stateHello opens every stream. Voice is the state now; on a resume the
// replayed frames that follow are older than it.
type stateHello struct {
	Cursor uint64                 `json:"cursor"`
	Host   string                 `json:"host"`
	Voice  voicestream.StateEvent `json:"voice"`
}

func parseStateKinds(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultStateKinds, nil
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		ok := false
		for _, a := range stateKindsAllowed {
			if strings.HasPrefix(k, a) || k == strings.TrimSuffix(a, ".") {
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("kind %q is not a session or voice kind", k)
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return defaultStateKinds, nil
	}
	return out, nil
}

// stateKindMatch: "voice." matches the namespace, "session" matches
// session.*, anything else matches exactly or as a dotted prefix.
func stateKindMatch(kinds []string, kind string) bool {
	for _, k := range kinds {
		switch {
		case kind == k:
			return true
		case strings.HasSuffix(k, ".") && strings.HasPrefix(kind, k):
			return true
		case strings.HasPrefix(kind, k+"."):
			return true
		}
	}
	return false
}

func parseStateAfter(r *http.Request) (events.Cursor, bool, error) {
	// A browser EventSource reconnects with the URL it was opened with
	// (?after= from the first connect) plus Last-Event-ID: the larger one
	// is where the client really is, so nothing it has is replayed.
	var after events.Cursor
	resume := false
	for _, raw := range []string{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("after %q is not a cursor", raw)
		}
		resume = true
		if events.Cursor(n) > after {
			after = events.Cursor(n)
		}
	}
	return after, resume, nil
}

// stateBus is the profile's events bus, opened once as a follower.
func (s *Server) stateBus() (*events.Bus, error) {
	s.stateBusMu.Lock()
	defer s.stateBusMu.Unlock()
	if s.stateBusClosed {
		return nil, errors.New("server stopping")
	}
	if s.stateBusHandle != nil {
		return s.stateBusHandle, nil
	}
	b, err := events.OpenReader(s.stateProfile())
	if errors.Is(err, events.ErrNoBus) {
		// No writer has created the log yet: create it, then follow it.
		w := events.OpenProfile(s.stateProfile())
		_ = w.Close()
		b, err = events.OpenReader(s.stateProfile())
	}
	if err != nil {
		return nil, err
	}
	s.stateBusHandle = b
	return b, nil
}

func (s *Server) stateProfile() string {
	return session.GetEffectiveProfile(s.cfg.Profile)
}

func (s *Server) handleStateEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeStreamRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	kinds, err := parseStateKinds(r.URL.Query().Get("kind"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_KIND", err.Error())
		return
	}
	after, resume, err := parseStateAfter(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_CURSOR", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unavailable")
		return
	}
	bus, err := s.stateBus()
	if err != nil {
		logging.ForComponent(logging.CompWeb).Error("state_stream_bus_unavailable", slog.String("error", err.Error()))
		writeAPIError(w, http.StatusServiceUnavailable, "BUS_UNAVAILABLE", "event bus unavailable")
		return
	}
	end, _, err := bus.Ends()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "BUS_UNAVAILABLE", "event bus unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	hello := stateHello{Cursor: uint64(end), Voice: s.currentVoiceEvent()}
	if err := writeSSEEvent(w, flusher, "hello", hello); err != nil {
		return
	}
	if !resume {
		after = end
	} else if after > end {
		// A cursor this log never reached (another host's or profile's, or
		// a log that was re-created): waiting for it would hide every frame
		// until the log catches up. Start over from now instead.
		if err := writeSSEEvent(w, flusher, "reset", stateResetFrame{Reason: "cursor-ahead", Cursor: uint64(end), Voice: s.currentVoiceEvent()}); err != nil {
			return
		}
		after = end
	}

	ctx := r.Context()
	if s.baseCtx != nil {
		var cancel context.CancelFunc
		ctx, cancel = mergeDone(ctx, s.baseCtx)
		defer cancel()
	}
	heartbeat := time.NewTicker(stateEventsHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		sctx, cancelSub := context.WithCancel(ctx)
		sub, err := bus.Subscribe(sctx, after)
		if err != nil {
			cancelSub()
			return
		}
		next, done := s.pumpStateFrames(ctx, w, flusher, sub, kinds, heartbeat, &after)
		cancelSub()
		for range sub.Frames() {
			// drain so the subscription goroutine can exit
		}
		if done {
			return
		}
		if next == stateNeedsReset {
			end, _, err := bus.Ends()
			if err != nil {
				return
			}
			if err := writeSSEEvent(w, flusher, "reset", stateResetFrame{Reason: "cursor-too-old", Cursor: uint64(end), Voice: s.currentVoiceEvent()}); err != nil {
				return
			}
			after = end
			continue
		}
		return
	}
}

const (
	stateEnd = iota
	stateNeedsReset
)

// pumpStateFrames writes frames until the client goes, the subscription
// ends, or the cursor is too old (stateResetFrame).
func (s *Server) pumpStateFrames(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sub *events.Subscription,
	kinds []string, heartbeat *time.Ticker, after *events.Cursor) (int, bool) {
	frames := sub.Frames()
	for {
		select {
		case <-ctx.Done():
			return stateEnd, true
		case <-heartbeat.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return stateEnd, true
			}
		case f, ok := <-frames:
			if !ok {
				if errors.Is(sub.Err(), events.ErrCursorTooOld) {
					return stateNeedsReset, false
				}
				return stateEnd, true
			}
			*after = f.Cursor
			if !stateKindMatch(kinds, f.Kind) {
				continue
			}
			raw, err := f.CanonicalJSON()
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", f.Cursor, f.Kind, raw); err != nil {
				return stateEnd, true
			}
			flusher.Flush()
		}
	}
}

// currentVoiceEvent is the voice state now, read from this host's files.
func (s *Server) currentVoiceEvent() voicestream.StateEvent {
	st := voicestate.NewReader(voicestate.DefaultDir()).Poll()
	return voicestream.FromState(st, voicestream.LocalOrigin(), voicestream.ReasonStart, time.Now())
}

// startVoiceProducer publishes voice.state for this profile while the
// server runs (one producer per host: a running relay or receiver keeps
// the lock and this one waits). Joined by stopVoiceProducer.
func (s *Server) startVoiceProducer() {
	ctx, cancel := context.WithCancel(context.Background())
	s.voiceProdCancel = cancel
	s.voiceProdWG.Add(1)
	go func() {
		defer s.voiceProdWG.Done()
		p := voicestream.NewProducer(voicestate.DefaultDir(), s.stateProfile())
		if err := voicestream.RunExclusive(ctx, s.stateProfile(), p); err != nil {
			logging.ForComponent(logging.CompWeb).Warn("voice_producer_disabled", slog.String("error", err.Error()))
		}
	}()
}

// StopVoiceProducer stops the voice producer and publishes its "off"; the
// process's signal path calls it before the bus is closed. Safe to call
// more than once.
func (s *Server) StopVoiceProducer() { s.stopVoiceProducer() }

func (s *Server) stopVoiceProducer() {
	if s.voiceProdCancel != nil {
		s.voiceProdCancel()
	}
	s.voiceProdWG.Wait()
	s.stateBusMu.Lock()
	s.stateBusClosed = true
	if s.stateBusHandle != nil {
		_ = s.stateBusHandle.Close()
		s.stateBusHandle = nil
	}
	s.stateBusMu.Unlock()
}

// mergeDone is a context that ends when either a or b ends.
func mergeDone(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	stop := context.AfterFunc(b, cancel)
	return ctx, func() { stop(); cancel() }
}

// stateServerFields is embedded in Server.
type stateServerFields struct {
	stateBusMu      sync.Mutex
	stateBusHandle  *events.Bus
	stateBusClosed  bool
	voiceProdCancel context.CancelFunc
	voiceProdWG     sync.WaitGroup
}
