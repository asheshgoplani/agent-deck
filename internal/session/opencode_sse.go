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
}

// OpenCodeSSEWatcher manages SSE connections to OpenCode instances.
type OpenCodeSSEWatcher struct {
	mu       sync.Mutex
	conns    map[string]*openCodeSSEConn // instance ID -> active connection
	statuses map[string]*OpenCodeSSEStatus
	onChange func() // optional TUI refresh callback

	// client is overridable for tests.
	client *http.Client

	// Comms Ledger producer (docs/comms.md): per instance, the generation of
	// the pending idle debounce (a busy event invalidates it) and the id of
	// the last message spooled, so a repeated idle flip spools nothing new.
	idleGen     map[string]uint64
	lastSpooled map[string]string
	// idleDebounce is how long the server must stay idle before the final
	// message is fetched; OpenCode reports false idles mid-work.
	idleDebounce time.Duration
	// spool is overridable for tests; defaults to WriteCommsSpool.
	spool func(CommsSpoolEntry) error
}

type openCodeSSEConn struct {
	port   int
	cancel context.CancelFunc
}

// NewOpenCodeSSEWatcher creates a watcher. onChange (may be nil) is invoked
// whenever a derived status changes.
func NewOpenCodeSSEWatcher(onChange func()) *OpenCodeSSEWatcher {
	return &OpenCodeSSEWatcher{
		conns:        make(map[string]*openCodeSSEConn),
		statuses:     make(map[string]*OpenCodeSSEStatus),
		onChange:     onChange,
		idleGen:      make(map[string]uint64),
		lastSpooled:  make(map[string]string),
		idleDebounce: 2 * time.Second,
		spool:        WriteCommsSpool,
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

	want := make(map[string]int, len(targets))
	for _, t := range targets {
		if t.InstanceID == "" || t.Port <= 0 {
			continue
		}
		want[t.InstanceID] = t.Port
	}

	for id, conn := range w.conns {
		if port, ok := want[id]; !ok || port != conn.port {
			conn.cancel()
			delete(w.conns, id)
			delete(w.statuses, id)
		}
	}

	for id, port := range want {
		if _, ok := w.conns[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		w.conns[id] = &openCodeSSEConn{port: port, cancel: cancel}
		go w.run(ctx, id, port)
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
func (w *OpenCodeSSEWatcher) run(ctx context.Context, instanceID string, port int) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for ctx.Err() == nil {
		gotData := w.stream(ctx, instanceID, port)
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
}

// stream connects to /event and consumes it until error or cancellation.
// Returns true if any data was received (resets the caller's backoff).
func (w *OpenCodeSSEWatcher) stream(ctx context.Context, instanceID string, port int) bool {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Track busy-ness per OpenCode session on this server: subagent/child
	// sessions share the process, and the instance is "running" while ANY of
	// them is busy. Seed from the /session/status snapshot so a stream opened
	// mid-turn starts correct instead of waiting for the next transition.
	busy := make(map[string]bool)
	seeded := w.seedSnapshot(ctx, base, busy)
	if seeded && len(busy) > 0 {
		w.setStatus(instanceID, "running")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/event", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	sessionLog.Debug("opencode_sse_connected",
		slog.String("instance_id", instanceID), slog.Int("port", port))

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
			w.cancelIdleSpool(instanceID)
		} else {
			w.setStatus(instanceID, "waiting")
			w.scheduleIdleSpool(ctx, instanceID, base, ev.Properties.SessionID)
		}
	}
	return gotData
}

// cancelIdleSpool invalidates a pending idle debounce: the server went busy
// again before the window elapsed (a false idle mid-work).
func (w *OpenCodeSSEWatcher) cancelIdleSpool(instanceID string) {
	w.mu.Lock()
	w.idleGen[instanceID]++
	w.mu.Unlock()
}

// scheduleIdleSpool arms the idle debounce when the ledger is on. After the
// window, if no busy event arrived, the root session's newest assistant
// message is fetched over the HTTP API OpenCode already serves and spooled
// as a turn_end edge. OpenCode's idle event does not carry the text, and
// its child (subagent) sessions emit idle too, so the root is resolved
// through the session's parentID chain.
func (w *OpenCodeSSEWatcher) scheduleIdleSpool(ctx context.Context, instanceID, base, sessionID string) {
	if sessionID == "" || !CommsLedgerEnabled() {
		return
	}
	w.mu.Lock()
	w.idleGen[instanceID]++
	gen := w.idleGen[instanceID]
	wait := w.idleDebounce
	w.mu.Unlock()
	time.AfterFunc(wait, func() {
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		current := w.idleGen[instanceID]
		w.mu.Unlock()
		if current != gen {
			return
		}
		w.spoolIdleTurn(ctx, instanceID, base, sessionID)
	})
}

// openCodeMessage is the subset of GET /session/:id/message the producer
// reads: the role, the message id and its text parts.
type openCodeMessage struct {
	Info struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"info"`
	Parts []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Synthetic bool   `json:"synthetic"`
		Ignored   bool   `json:"ignored"`
	} `json:"parts"`
}

func (m openCodeMessage) text() string {
	var parts []string
	for _, p := range m.Parts {
		if p.Type == "text" && !p.Synthetic && !p.Ignored && strings.TrimSpace(p.Text) != "" {
			parts = append(parts, strings.TrimSpace(p.Text))
		}
	}
	return strings.Join(parts, "\n")
}

// spoolIdleTurn resolves the root session and spools its newest assistant
// text once per message id.
func (w *OpenCodeSSEWatcher) spoolIdleTurn(ctx context.Context, instanceID, base, sessionID string) {
	root := w.openCodeRootSession(ctx, base, sessionID)
	var messages []openCodeMessage
	if err := w.getJSON(ctx, base+"/session/"+root+"/message", &messages); err != nil || len(messages) == 0 {
		return
	}
	var assistant, prompt, messageID string
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		switch m.Info.Role {
		case "assistant":
			if assistant == "" {
				if t := m.text(); t != "" {
					assistant, messageID = t, m.Info.ID
				}
			}
		case "user":
			if assistant != "" {
				prompt = m.text()
			}
		}
		if assistant != "" && prompt != "" {
			break
		}
	}
	if assistant == "" {
		return
	}
	spooledKey := root + "/" + messageID
	w.mu.Lock()
	dup := w.lastSpooled[instanceID] == spooledKey
	if !dup {
		w.lastSpooled[instanceID] = spooledKey
	}
	spool := w.spool
	w.mu.Unlock()
	if dup || spool == nil {
		return
	}
	if err := spool(CommsSpoolEntry{
		Harness: "opencode", Event: "session.status/idle", Edge: CommsEdgeTurnEnd, Instance: instanceID,
		SessionID: root, TurnID: messageID, Text: assistant, Prompt: prompt, TSignal: time.Now().UnixMilli(),
	}); err != nil {
		sessionLog.Warn("comms_spool_write_failed", slog.String("instance", instanceID), slog.String("error", err.Error()))
	}
}

// openCodeRootSession follows parentID up to the root (bounded), so a
// subagent's idle resolves to the session the human talks to.
func (w *OpenCodeSSEWatcher) openCodeRootSession(ctx context.Context, base, sessionID string) string {
	id := sessionID
	for hop := 0; hop < 4; hop++ {
		var s struct {
			ParentID string `json:"parentID"`
		}
		if err := w.getJSON(ctx, base+"/session/"+id, &s); err != nil || s.ParentID == "" {
			return id
		}
		id = s.ParentID
	}
	return id
}

func (w *OpenCodeSSEWatcher) getJSON(ctx context.Context, url string, out any) error {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode: %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// seedSnapshot loads the /session/status snapshot into busy. Returns false if
// the snapshot could not be fetched or parsed.
func (w *OpenCodeSSEWatcher) seedSnapshot(ctx context.Context, base string, busy map[string]bool) bool {
	snapCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(snapCtx, http.MethodGet, base+"/session/status", nil)
	if err != nil {
		return false
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
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
