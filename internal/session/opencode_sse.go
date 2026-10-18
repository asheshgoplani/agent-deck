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
// the stream ignores every other session's events (issue #2511) except those
// of its sub-agents: a sub-agent runs in a child session whose parentID chain
// leads to the bound one, and while any of them is busy the instance is
// "running", the same rule the 1.x per-TUI stream applies.

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
	"sync/atomic"
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
	// parentOf names the session that spawned a session ("" for a root), so a
	// sub-agent's turn counts for its parent. Nil counts the bound session only.
	parentOf func(ctx context.Context, sessionID string) (string, error)
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
	sessionID    string // non-empty: 2.x shared stream, count only this session and its sub-agents
	parentOf     func(ctx context.Context, sessionID string) (string, error)
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

	// parents caches 2.x session -> parent session lookups. A link never
	// changes, so every stream on the shared service reuses an answer, and
	// streams asking about the same session at once share one CLI call.
	parents map[string]*openCodeParentLookup

	// writes counts status writes and touches: every handled stream line
	// ends in exactly one, so tests wait on it instead of sleeping.
	writes atomic.Int64

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
		parents:  make(map[string]*openCodeParentLookup),
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
		src := openCodeSharedServiceSource(svc, t.sessionID)
		src.parentOf = t.parentOf
		return src, nil
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
	tree := w.newSessionTree(ctx, src)
	seed := func() {
		seeded := w.seedSnapshot(ctx, src, tree, busy)
		if seeded && len(busy) > 0 {
			w.setStatus(ctx, instanceID, "running")
		} else if seeded && src.sessionID != "" {
			// A turn that ended while the stream was down must not leave the
			// bound session running; the shared snapshot lists busy sessions only.
			w.setStatus(ctx, instanceID, "waiting")
		}
	}
	// The 2.x seed may look up sub-agent parents, which takes CLI calls, so
	// it runs after subscribing: events that arrive meanwhile wait in the
	// stream instead of being lost between the snapshot and the subscription.
	if src.sessionID == "" {
		seed()
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
		seed()
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
	lastReconcile := time.Now()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		gotData = true
		// A counted sub-agent that is aborted, deleted or dies may never
		// send its turn ending, and the stream's own traffic keeps the status
		// fresh, so busy sessions are checked against session.active now and
		// then. It runs between events, so their order is kept.
		if src.sessionID != "" && len(busy) > 0 && time.Since(lastReconcile) >= openCodeReconcileInterval {
			w.reconcileShared(ctx, instanceID, src, busy)
			lastReconcile = time.Now()
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			// Comment/heartbeat line: refresh freshness of the known status.
			w.touch(ctx, instanceID)
			continue
		}
		var ev openCodeEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			w.touch(ctx, instanceID)
			continue
		}
		if src.sessionID != "" {
			walkCtx, cancel := context.WithTimeout(ctx, openCodeParentWalkBudget)
			applied := applySharedServiceEvent(walkCtx, ev, tree, busy)
			cancel()
			if !applied {
				w.touch(ctx, instanceID)
				continue
			}
			w.setStatus(ctx, instanceID, busyStatus(busy))
			if ev.Data.SessionID == src.sessionID && !busy[src.sessionID] && len(busy) > 0 {
				// The bound turn ended with sub-agents still counted: check
				// them now rather than leave the parent running on a stale one.
				w.reconcileShared(ctx, instanceID, src, busy)
				lastReconcile = time.Now()
			}
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
			w.touch(ctx, instanceID)
			continue
		}
		if len(busy) > 0 {
			w.setStatus(ctx, instanceID, "running")
		} else {
			w.setStatus(ctx, instanceID, "waiting")
		}
	}
	return gotData
}

// applySharedServiceEvent folds one 2.x event for the bound session or one of
// its sub-agents into busy. It returns false for anything that is not a turn
// transition of those sessions: other sessions' events and unrelated types
// only prove the stream is alive.
func applySharedServiceEvent(ctx context.Context, ev openCodeEvent, tree *openCodeSessionTree, busy map[string]bool) bool {
	sessionID := ev.Data.SessionID
	if sessionID == "" {
		return false
	}
	switch ev.Type {
	case "session.execution.started", "session.retry.scheduled":
		if !tree.counts(ctx, sessionID) {
			return false
		}
		busy[sessionID] = true
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		// Only a session already counted busy can end a counted turn, so an
		// ending never needs a parent lookup.
		if !busy[sessionID] && sessionID != tree.root {
			return false
		}
		delete(busy, sessionID)
	default:
		return false
	}
	return true
}

// openCodeSessionMaxDepth bounds a parent-chain walk, so a corrupt or cyclic
// chain ends instead of looping.
const openCodeSessionMaxDepth = 8

// openCodeParentWalkBudget bounds the parent lookups one event (or the
// whole seed) may run, so a stuck CLI cannot stall the stream past the
// status freshness window.
var openCodeParentWalkBudget = 5 * time.Second

// openCodeReconcileInterval is how often a stream with busy sessions checks
// them against the service's session.active.
var openCodeReconcileInterval = 30 * time.Second

// openCodeParentCacheMax bounds the shared parent cache: past it, finished
// lookups are dropped and asked again when seen.
var openCodeParentCacheMax = 4096

// openCodeParentRetry is how long a failed parent lookup is not repeated, so
// a 2.x build without session.get, or a stuck service, does not get one CLI
// call per event.
var openCodeParentRetry = time.Minute

// openCodeParentLookup is one session's parent lookup, shared by every stream.
type openCodeParentLookup struct {
	done     chan struct{} // closed when the call below has finished
	parent   string        // "" for a root session
	err      error
	failedAt time.Time
	waiters  int // streams that joined this call, for tests
}

// openCodeSessionTree answers, for one stream, whether a session is the bound
// session or a sub-agent below it.
type openCodeSessionTree struct {
	w        *OpenCodeSSEWatcher
	stream   context.Context // the stream's own context, not a walk deadline
	root     string
	parentOf func(ctx context.Context, sessionID string) (string, error)
	memo     map[string]bool // definite answers only; a failed lookup is asked again later
}

func (w *OpenCodeSSEWatcher) newSessionTree(stream context.Context, src openCodeEventSource) *openCodeSessionTree {
	return &openCodeSessionTree{w: w, stream: stream, root: src.sessionID, parentOf: src.parentOf, memo: make(map[string]bool)}
}

// counts walks sessionID's parent chain up to the bound session.
func (t *openCodeSessionTree) counts(ctx context.Context, sessionID string) bool {
	var path []string
	result, definite := false, false
	cur := sessionID
	for depth := 0; depth < openCodeSessionMaxDepth && ctx.Err() == nil; depth++ {
		if cur == t.root {
			result, definite = true, true
			break
		}
		if known, ok := t.memo[cur]; ok {
			result, definite = known, true
			break
		}
		path = append(path, cur)
		parent, ok := t.parent(ctx, cur)
		if !ok {
			break
		}
		if parent == "" {
			definite = true // reached a root that is not the bound session
			break
		}
		cur = parent
	}
	if definite {
		for _, id := range path {
			t.memo[id] = result
		}
	}
	return result
}

// parent returns sessionID's parent. A successful answer is kept for every
// stream; concurrent streams wait on the one call already running; a failure
// is not asked again for openCodeParentRetry.
func (t *openCodeSessionTree) parent(ctx context.Context, sessionID string) (string, bool) {
	if t.parentOf == nil {
		return "", false
	}
	w := t.w
	w.mu.Lock()
	lookup, ok := w.parents[sessionID]
	if ok {
		select {
		case <-lookup.done:
			if lookup.err == nil {
				w.mu.Unlock()
				return lookup.parent, true
			}
			if time.Since(lookup.failedAt) < openCodeParentRetry {
				w.mu.Unlock()
				return "", false
			}
			ok = false // retry a failure that is old enough
		default:
		}
	}
	if ok {
		lookup.waiters++
		w.mu.Unlock()
		select {
		case <-lookup.done:
		case <-ctx.Done():
			return "", false
		}
		w.mu.Lock()
		parent, err := lookup.parent, lookup.err
		dropped := w.parents[sessionID] != lookup
		w.mu.Unlock()
		if err != nil && dropped && ctx.Err() == nil && t.stream.Err() == nil {
			// The leading stream was cancelled mid-call; ask again.
			return t.parent(ctx, sessionID)
		}
		return parent, err == nil
	}
	if len(w.parents) >= openCodeParentCacheMax {
		for id, old := range w.parents {
			select {
			case <-old.done:
				delete(w.parents, id)
			default: // in flight: its streams are waiting on it
			}
		}
	}
	lookup = &openCodeParentLookup{done: make(chan struct{})}
	w.parents[sessionID] = lookup
	w.mu.Unlock()

	parent, err := t.parentOf(ctx, sessionID)
	w.mu.Lock()
	lookup.parent, lookup.err = parent, err
	if err != nil {
		lookup.failedAt = time.Now()
		if t.stream.Err() != nil && w.parents[sessionID] == lookup {
			// This stream was cancelled; that says nothing about the session.
			// A walk deadline does count as a failure, so a stuck CLI is not
			// asked again on every event.
			delete(w.parents, sessionID)
		}
	}
	close(lookup.done)
	w.mu.Unlock()
	if err != nil {
		sessionLog.Debug("opencode_sse_parent_lookup_failed",
			slog.String("opencode_session", sessionID), slog.String("error", err.Error()))
		return "", false
	}
	return parent, true
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
func (w *OpenCodeSSEWatcher) seedSnapshot(ctx context.Context, src openCodeEventSource, tree *openCodeSessionTree, busy map[string]bool) bool {
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
		active, ok := decodeSharedActive(resp)
		if !ok {
			return false
		}
		// A busy sub-agent of the bound session counts too. All the seed's
		// lookups share one budget.
		walkCtx, cancelWalk := context.WithTimeout(ctx, openCodeParentWalkBudget)
		defer cancelWalk()
		for sid := range active {
			if tree.counts(walkCtx, sid) {
				busy[sid] = true
			}
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

// decodeSharedActive reads 2.x session.active,
// {"data":{"ses_...":{"type":"running"}}}, which lists busy sessions only.
func decodeSharedActive(resp *http.Response) (map[string]bool, bool) {
	var active struct {
		Data map[string]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
		return nil, false
	}
	running := make(map[string]bool, len(active.Data))
	for sid, st := range active.Data {
		if st.Type == "running" {
			running[sid] = true
		}
	}
	return running, true
}

// reconcileShared drops busy sessions that session.active no longer lists:
// a turn whose ending event never came. A failed fetch changes nothing.
func (w *OpenCodeSSEWatcher) reconcileShared(ctx context.Context, instanceID string, src openCodeEventSource, busy map[string]bool) {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, src.base+src.snapshotPath, nil)
	if err != nil {
		return
	}
	src.authorize(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	active, ok := decodeSharedActive(resp)
	if !ok {
		return
	}
	dropped := false
	for sid := range busy {
		if !active[sid] {
			delete(busy, sid)
			dropped = true
		}
	}
	if dropped {
		sessionLog.Debug("opencode_sse_reconciled_stale_busy",
			slog.String("instance_id", instanceID), slog.String("opencode_session", src.sessionID))
		w.setStatus(ctx, instanceID, busyStatus(busy))
	}
}

// setStatus records a derived status and fires onChange on transitions.
// ctx is the connection's context: Sync cancels a replaced connection under
// w.mu without waiting for its stream, so a late write from it is dropped here.
func (w *OpenCodeSSEWatcher) setStatus(ctx context.Context, instanceID, status string) {
	w.mu.Lock()
	if ctx.Err() != nil {
		w.mu.Unlock()
		return
	}
	prev := w.statuses[instanceID]
	changed := prev == nil || prev.Status != status
	w.statuses[instanceID] = &OpenCodeSSEStatus{Status: status, UpdatedAt: time.Now()}
	onChange := w.onChange
	w.mu.Unlock()
	w.writes.Add(1)
	if changed && onChange != nil {
		onChange()
	}
}

// touch refreshes the freshness timestamp of an existing status without
// changing it, so a quiet-but-connected stream keeps the fast path alive.
func (w *OpenCodeSSEWatcher) touch(ctx context.Context, instanceID string) {
	w.mu.Lock()
	if s, ok := w.statuses[instanceID]; ok && ctx.Err() == nil {
		s.UpdatedAt = time.Now()
	}
	w.mu.Unlock()
	w.writes.Add(1)
}
