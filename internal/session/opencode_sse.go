package session

// SSE-based status tracking for OpenCode sessions (issue #1614).
//
// OpenCode's TUI, when launched with an explicit --port, binds a real HTTP
// server on 127.0.0.1 that streams session lifecycle events over SSE at
// GET /event. Among them, "session.status" events carry
// {sessionID, status:{type: "busy"|"retry"|"idle"}} and "session.idle"
// carries {sessionID}. A GET /session/status snapshot returns the busy
// sessions at connect time.
//
// OpenCodeSSEWatcher maintains one streaming connection per running OpenCode
// instance and derives a per-instance status:
//
//	any session busy/retry  -> "running"
//	all sessions idle       -> "waiting"
//
// The TUI feed loop (internal/ui/home.go backgroundStatusUpdate) pushes the
// derived status into the Instance, where UpdateStatus() consumes it on an
// SSE fast path ahead of the tmux content-sniffing fallback. When the stream
// drops or goes silent, the status ages out of its freshness window and
// control falls back to tmux polling — the same degradation model as the
// Claude hook fast path.
//
// OpenCode 2.x has no per-TUI server. One background service, whose URL
// `opencode service status` prints, streams every session's events at
// GET /api/event (basic auth), framed {type, data:{sessionID}}. A turn runs
// from session.execution.started to session.execution.succeeded, failed or
// interrupted. A 2.x target therefore names the instance's bound session, and
// the stream ignores every other session's events (issue #2511).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenCodeSSEStatus is the latest status derived from an instance's event stream.
type OpenCodeSSEStatus struct {
	Status    string    // "running" or "waiting"
	UpdatedAt time.Time // last time the stream confirmed this status
}

// SSETarget identifies one OpenCode instance's event server.
type SSETarget struct {
	InstanceID string
	Port       int

	// 2.x shared service (Port is 0): the bound OpenCode session whose events
	// count, and how to find the service. Set by Instance.OpenCodeSSETarget.
	sessionID string
	service   func() (openCodeService, error)
}

// openCodeService is where the 2.x background service listens.
type openCodeService struct {
	URL      string
	Password string // basic-auth password; empty sends no credentials
}

// openCodeEventSource is one resolved stream: the 1.x per-TUI server, or the
// 2.x shared service filtered to one session.
type openCodeEventSource struct {
	base         string
	port         int // 1.x per-TUI server port, for logs
	eventPath    string
	snapshotPath string
	password     string
	sessionID    string // non-empty: 2.x shared stream, count only this session
}

func openCodeTUIServerSource(port int) openCodeEventSource {
	return openCodeEventSource{
		base:         fmt.Sprintf("http://127.0.0.1:%d", port),
		port:         port,
		eventPath:    "/event",
		snapshotPath: "/session/status",
	}
}

func openCodeSharedServiceSource(svc openCodeService, sessionID string) openCodeEventSource {
	return openCodeEventSource{
		base:         strings.TrimRight(svc.URL, "/"),
		eventPath:    "/api/event",
		snapshotPath: "/api/session/active",
		password:     svc.Password,
		sessionID:    sessionID,
	}
}

// OpenCodeSSEWatcher manages SSE connections to OpenCode instances.
type OpenCodeSSEWatcher struct {
	mu       sync.Mutex
	conns    map[string]*openCodeSSEConn // instance ID -> active connection
	statuses map[string]*OpenCodeSSEStatus
	onChange func() // optional TUI refresh callback

	// client is overridable for tests.
	client *http.Client
}

type openCodeSSEConn struct {
	port      int
	sessionID string
	cancel    context.CancelFunc
}

// NewOpenCodeSSEWatcher creates a watcher. onChange (may be nil) is invoked
// whenever a derived status changes.
func NewOpenCodeSSEWatcher(onChange func()) *OpenCodeSSEWatcher {
	return &OpenCodeSSEWatcher{
		conns:    make(map[string]*openCodeSSEConn),
		statuses: make(map[string]*OpenCodeSSEStatus),
		onChange: onChange,
		client: &http.Client{
			// Streaming reads must not time out; bound only the dial.
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
		},
	}
}

// Sync reconciles active connections against the desired target set: it opens
// connections for new targets (or targets whose port changed after a restart)
// and tears down connections for instances no longer present.
func (w *OpenCodeSSEWatcher) Sync(targets []SSETarget) {
	w.mu.Lock()
	defer w.mu.Unlock()

	want := make(map[string]SSETarget, len(targets))
	for _, t := range targets {
		shared := t.sessionID != "" && t.service != nil
		if t.InstanceID == "" || (t.Port <= 0 && !shared) {
			continue
		}
		want[t.InstanceID] = t
	}

	for id, conn := range w.conns {
		if t, ok := want[id]; !ok || t.Port != conn.port || t.sessionID != conn.sessionID {
			conn.cancel()
			delete(w.conns, id)
			delete(w.statuses, id)
		}
	}

	for id, t := range want {
		if _, ok := w.conns[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		w.conns[id] = &openCodeSSEConn{port: t.Port, sessionID: t.sessionID, cancel: cancel}
		go w.run(ctx, id, t.eventSource())
	}
}

// eventSource returns how to (re)resolve this target's stream on each
// connect attempt. The 2.x service is looked up inside the stream goroutine,
// on the reconnect backoff, so the TUI tick never runs the CLI.
func (t SSETarget) eventSource() func() (openCodeEventSource, error) {
	if t.Port > 0 {
		src := openCodeTUIServerSource(t.Port)
		return func() (openCodeEventSource, error) { return src, nil }
	}
	return func() (openCodeEventSource, error) {
		svc, err := t.service()
		if err != nil {
			return openCodeEventSource{}, err
		}
		return openCodeSharedServiceSource(svc, t.sessionID), nil
	}
}

// GetStatus returns the latest derived status for an instance, or nil.
func (w *OpenCodeSSEWatcher) GetStatus(instanceID string) *OpenCodeSSEStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.statuses[instanceID]
	if !ok {
		return nil
	}
	cp := *s
	return &cp
}

// Stop tears down all connections.
func (w *OpenCodeSSEWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, conn := range w.conns {
		conn.cancel()
		delete(w.conns, id)
	}
}

// run maintains one instance's stream with reconnect + exponential backoff.
func (w *OpenCodeSSEWatcher) run(ctx context.Context, instanceID string, resolve func() (openCodeEventSource, error)) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		gotData := false
		if src, err := resolve(); err != nil {
			sessionLog.Debug("opencode_sse_service_unavailable",
				slog.String("instance_id", instanceID), slog.String("error", err.Error()))
		} else {
			gotData = w.stream(ctx, instanceID, src)
		}
		if ctx.Err() != nil {
			return
		}
		if gotData {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// openCodeEvent is the SSE payload shape (verified against the OpenCode
// server's OpenAPI doc: Event.session.status / Event.session.idle).
type openCodeEvent struct {
	Type       string `json:"type"`
	Properties struct {
		SessionID string `json:"sessionID"`
		Status    struct {
			Type string `json:"type"` // "idle" | "busy" | "retry"
		} `json:"status"`
	} `json:"properties"`
	// Data is the 2.x shared-service payload.
	Data struct {
		SessionID string `json:"sessionID"`
	} `json:"data"`
}

// stream connects to /event and consumes it until error or cancellation.
// Returns true if any data was received (resets the caller's backoff).
func (w *OpenCodeSSEWatcher) stream(ctx context.Context, instanceID string, src openCodeEventSource) bool {
	base := src.base

	// Track busy-ness per OpenCode session on this server: subagent/child
	// sessions share the process, and the instance is "running" while ANY of
	// them is busy. Seed from the /session/status snapshot so a stream opened
	// mid-turn starts correct instead of waiting for the next transition.
	busy := make(map[string]bool)
	seeded := w.seedSnapshot(ctx, src, busy)
	if seeded && len(busy) > 0 {
		w.setStatus(instanceID, "running")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+src.eventPath, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/event-stream")
	src.authorize(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	if src.sessionID != "" {
		sessionLog.Debug("opencode_sse_connected",
			slog.String("instance_id", instanceID), slog.String("service", base),
			slog.String("opencode_session", src.sessionID))
	} else {
		sessionLog.Debug("opencode_sse_connected",
			slog.String("instance_id", instanceID), slog.Int("port", src.port))
	}

	gotData := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		gotData = true
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			// Comment/heartbeat line: refresh freshness of the known status.
			w.touch(instanceID)
			continue
		}
		var ev openCodeEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			w.touch(instanceID)
			continue
		}
		if src.sessionID != "" {
			if !applySharedServiceEvent(ev, src.sessionID, busy) {
				w.touch(instanceID)
				continue
			}
			w.setStatus(instanceID, busyStatus(busy))
			continue
		}
		switch ev.Type {
		case "session.status":
			switch ev.Properties.Status.Type {
			case "busy", "retry":
				// retry = provider call being retried; still working, no
				// input needed, so it maps to running rather than waiting.
				busy[ev.Properties.SessionID] = true
			default: // "idle"
				delete(busy, ev.Properties.SessionID)
			}
		case "session.idle":
			delete(busy, ev.Properties.SessionID)
		default:
			// Unrelated event (heartbeat, tui.*, ...): liveness only.
			w.touch(instanceID)
			continue
		}
		if len(busy) > 0 {
			w.setStatus(instanceID, "running")
		} else {
			w.setStatus(instanceID, "waiting")
		}
	}
	return gotData
}

// applySharedServiceEvent folds one 2.x event for the bound session into busy.
// It returns false for anything that is not a turn transition of that session:
// other sessions' events and unrelated types only prove the stream is alive.
func applySharedServiceEvent(ev openCodeEvent, sessionID string, busy map[string]bool) bool {
	if ev.Data.SessionID != sessionID {
		return false
	}
	switch ev.Type {
	case "session.execution.started", "session.retry.scheduled":
		busy[sessionID] = true
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		delete(busy, sessionID)
	default:
		return false
	}
	return true
}

func busyStatus(busy map[string]bool) string {
	if len(busy) > 0 {
		return "running"
	}
	return "waiting"
}

// authorize adds the 2.x service's basic auth. The service URL is checked to
// be loopback before a password is ever attached (resolveOpenCodeService).
func (src openCodeEventSource) authorize(req *http.Request) {
	if src.password != "" {
		req.SetBasicAuth("opencode", src.password)
	}
}

// seedSnapshot loads the busy-session snapshot into busy. Returns false if
// the snapshot could not be fetched or parsed.
func (w *OpenCodeSSEWatcher) seedSnapshot(ctx context.Context, src openCodeEventSource, busy map[string]bool) bool {
	snapCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(snapCtx, http.MethodGet, src.base+src.snapshotPath, nil)
	if err != nil {
		return false
	}
	src.authorize(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if src.sessionID != "" {
		// 2.x session.active: {"data":{"ses_...":{"type":"running"}}}, busy
		// sessions only.
		var active struct {
			Data map[string]struct {
				Type string `json:"type"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
			return false
		}
		if st, ok := active.Data[src.sessionID]; ok && st.Type == "running" {
			busy[src.sessionID] = true
		}
		return true
	}
	var snapshot map[string]struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return false
	}
	for sid, st := range snapshot {
		if st.Type == "busy" || st.Type == "retry" {
			busy[sid] = true
		}
	}
	return true
}

// setStatus records a derived status and fires onChange on transitions.
func (w *OpenCodeSSEWatcher) setStatus(instanceID, status string) {
	w.mu.Lock()
	prev := w.statuses[instanceID]
	changed := prev == nil || prev.Status != status
	w.statuses[instanceID] = &OpenCodeSSEStatus{Status: status, UpdatedAt: time.Now()}
	onChange := w.onChange
	w.mu.Unlock()
	if changed && onChange != nil {
		onChange()
	}
}

// touch refreshes the freshness timestamp of an existing status without
// changing it, so a quiet-but-connected stream keeps the fast path alive.
func (w *OpenCodeSSEWatcher) touch(instanceID string) {
	w.mu.Lock()
	if s, ok := w.statuses[instanceID]; ok {
		s.UpdatedAt = time.Now()
	}
	w.mu.Unlock()
}
