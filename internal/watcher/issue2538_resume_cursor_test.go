package watcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2538: the ntfy and slack adapters kept their resume position (the
// last ntfy message ID) only in memory, so the first subscription after a
// restart carried no `since` and every message published while agent-deck was
// down was silently lost. These tests stop one engine, publish to the topic
// while nothing is subscribed, start a fresh engine on the same state, and
// require the missed messages to land in watcher_events exactly once.

// fakeNtfyServer is a minimal ntfy server with a message cache. It honors the
// three `since` forms the adapters can send: a message ID (replay everything
// after it; like ntfy, an ID no longer in the cache replays the whole cache),
// a unix timestamp (replay messages at or after it) and "all". Without `since` it streams only
// messages published after the subscription opened, as ntfy does.
type fakeNtfyServer struct {
	t      *testing.T
	topic  string
	mu     sync.Mutex
	cache  []ntfyMessage
	nextID int
	reqs   []string // raw query of every subscribe request, in order
	srv    *httptest.Server
}

func newFakeNtfyServer(t *testing.T, topic string) *fakeNtfyServer {
	t.Helper()
	f := &fakeNtfyServer{t: t, topic: topic}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNtfyServer) publish(message string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("m%04d", f.nextID)
	f.cache = append(f.cache, ntfyMessage{
		ID: id, Time: time.Now().Unix(), Event: "message", Topic: f.topic, Message: message,
	})
	return id
}

func (f *fakeNtfyServer) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func (f *fakeNtfyServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		return
	}
	if r.URL.Path != "/"+f.topic+"/json" {
		http.NotFound(w, r)
		return
	}
	since := r.URL.Query().Get("since")

	f.mu.Lock()
	f.reqs = append(f.reqs, r.URL.RawQuery)
	next := len(f.cache)
	switch {
	case since == "":
	case since == "all":
		next = 0
	default:
		if ts, err := strconv.ParseInt(since, 10, 64); err == nil {
			next = len(f.cache)
			for i, m := range f.cache {
				if m.Time >= ts {
					next = i
					break
				}
			}
		} else {
			next = 0
			for i, m := range f.cache {
				if m.ID == since {
					next = i + 1
					break
				}
			}
		}
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	open, _ := json.Marshal(ntfyMessage{ID: "open", Time: time.Now().Unix(), Event: "open", Topic: f.topic})
	fmt.Fprintf(w, "%s\n", open)
	if flusher != nil {
		flusher.Flush()
	}

	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		f.mu.Lock()
		pending := append([]ntfyMessage(nil), f.cache[next:]...)
		next = len(f.cache)
		f.mu.Unlock()
		for _, m := range pending {
			data, _ := json.Marshal(m)
			if _, err := fmt.Fprintf(w, "%s\n", data); err != nil {
				return
			}
		}
		if len(pending) > 0 && flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// issue2538Case describes one adapter type under test.
type issue2538Case struct {
	typ        string
	newAdapter func() WatcherAdapter
	// message builds the ntfy message body published for the n-th message.
	message func(n int) string
	// subject is the watcher_events.subject the adapter derives from message(n).
	subject func(n int) string
}

func issue2538Cases() []issue2538Case {
	return []issue2538Case{
		{
			typ: "ntfy",
			newAdapter: func() WatcherAdapter {
				return &NtfyAdapter{initialBackoff: 10 * time.Millisecond, maxBackoff: 50 * time.Millisecond}
			},
			message: func(n int) string { return fmt.Sprintf("ntfy message %d", n) },
			subject: func(n int) string { return fmt.Sprintf("ntfy message %d", n) },
		},
		{
			typ: "slack",
			newAdapter: func() WatcherAdapter {
				return &SlackAdapter{initialBackoff: 10 * time.Millisecond, maxBackoff: 50 * time.Millisecond}
			},
			message: func(n int) string {
				p, _ := json.Marshal(slackV2Payload{
					Type: "message", V: 2, Channel: "D2538", User: "U1",
					TS: fmt.Sprintf("1700000000.%06d", n), TextPreview: fmt.Sprintf("slack dm %d", n),
				})
				return string(p)
			},
			subject: func(n int) string { return fmt.Sprintf("slack dm %d", n) },
		},
	}
}

// newIssue2538Engine builds an engine on an existing DB, as a restarted
// agent-deck process would: same statedb, same watcher dir, fresh adapter.
func newIssue2538Engine(t *testing.T, db *statedb.StateDB, tc issue2538Case, watcherID, name, server, topic string) *Engine {
	t.Helper()
	eng := NewEngine(EngineConfig{
		DB:                  db,
		Router:              NewRouter(nil),
		MaxEventsPerWatcher: 1000,
		TriageSpawner:       &fakeSpawner{},
		TriageDir:           t.TempDir(),
	})
	eng.RegisterAdapter(watcherID, tc.newAdapter(), AdapterConfig{
		Type:     tc.typ,
		Name:     name,
		Settings: map[string]string{"topic": topic, "server": server},
	}, 60)
	if err := eng.Start(); err != nil {
		t.Fatalf("engine Start: %v", err)
	}
	return eng
}

// drainRouted keeps the engine's exported channel empty so a test never
// depends on its capacity.
func drainRouted(eng *Engine) {
	go func() {
		for range eng.EventCh() {
		}
	}()
}

func countEventsWithSubject(t *testing.T, db *statedb.StateDB, watcherID, subject string) int {
	t.Helper()
	var n int
	if err := db.DB().QueryRow(
		`SELECT COUNT(*) FROM watcher_events WHERE watcher_id = ? AND subject = ?`, watcherID, subject,
	).Scan(&n); err != nil {
		t.Fatalf("count by subject: %v", err)
	}
	return n
}

func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestIssue2538_RestartReceivesMessagesPublishedWhileDown is the reporter's
// scenario end to end: handle one message, stop, publish while down, start
// again, and require the missed message exactly once.
func TestIssue2538_RestartReceivesMessagesPublishedWhileDown(t *testing.T) {
	for _, tc := range issue2538Cases() {
		t.Run(tc.typ, func(t *testing.T) {
			db := newTestDB(t)
			watcherID := "w-2538-" + tc.typ
			name := fmt.Sprintf("i2538-%s-%d", tc.typ, time.Now().UnixNano())
			saveTestWatcher(t, db, watcherID, name, tc.typ)
			topic := "t2538" + tc.typ
			srv := newFakeNtfyServer(t, topic)

			// First process: subscribe, handle message 1, shut down.
			engA := newIssue2538Engine(t, db, tc, watcherID, name, srv.srv.URL, topic)
			drainRouted(engA)
			if !waitFor(5*time.Second, func() bool { return len(srv.requests()) >= 1 }) {
				engA.Stop()
				t.Fatal("harness: first engine never subscribed")
			}
			srv.publish(tc.message(1))
			if !waitFor(5*time.Second, func() bool { return countEventsWithSubject(t, db, watcherID, tc.subject(1)) == 1 }) {
				engA.Stop()
				t.Fatal("harness: live message 1 never reached watcher_events")
			}
			engA.Stop()

			// agent-deck is down: message 2 is published to the topic.
			srv.publish(tc.message(2))
			reqsBefore := len(srv.requests())

			// Second process: same DB and state dir, brand new adapter.
			engB := newIssue2538Engine(t, db, tc, watcherID, name, srv.srv.URL, topic)
			drainRouted(engB)
			defer engB.Stop()

			// Harness sanity: a live message after the restart still arrives.
			if !waitFor(5*time.Second, func() bool { return len(srv.requests()) > reqsBefore }) {
				t.Fatal("harness: restarted engine never subscribed")
			}
			srv.publish(tc.message(3))
			if !waitFor(5*time.Second, func() bool { return countEventsWithSubject(t, db, watcherID, tc.subject(3)) == 1 }) {
				t.Fatal("harness: live message 3 after restart never reached watcher_events")
			}

			firstAfterRestart := srv.requests()[reqsBefore]
			if got := countEventsWithSubject(t, db, watcherID, tc.subject(2)); got != 1 {
				t.Fatalf("message published while agent-deck was down reached watcher_events %d times, want 1 "+
					"(first subscribe after restart sent query %q, so the resume position was not persisted)",
					got, firstAfterRestart)
			}
			if got := countEventsWithSubject(t, db, watcherID, tc.subject(1)); got != 1 {
				t.Fatalf("message handled before the restart has %d watcher_events rows after replay, want 1", got)
			}
			if got := countWatcherEvents(t, db, watcherID); got != 3 {
				t.Fatalf("watcher_events rows = %d, want 3", got)
			}
		})
	}
}

// TestIssue2538_DowntimeBacklogLargerThanChannelsIsNotDropped publishes more
// messages while down than the adapter and engine channels hold (64 each).
// The replay must deliver every one of them: a message the engine has not
// persisted must never be skipped by the resume position.
func TestIssue2538_DowntimeBacklogLargerThanChannelsIsNotDropped(t *testing.T) {
	const backlog = 200
	for _, tc := range issue2538Cases() {
		t.Run(tc.typ, func(t *testing.T) {
			db := newTestDB(t)
			watcherID := "w-2538-backlog-" + tc.typ
			name := fmt.Sprintf("i2538b-%s-%d", tc.typ, time.Now().UnixNano())
			saveTestWatcher(t, db, watcherID, name, tc.typ)
			topic := "t2538b" + tc.typ
			srv := newFakeNtfyServer(t, topic)

			engA := newIssue2538Engine(t, db, tc, watcherID, name, srv.srv.URL, topic)
			drainRouted(engA)
			if !waitFor(5*time.Second, func() bool { return len(srv.requests()) >= 1 }) {
				engA.Stop()
				t.Fatal("harness: first engine never subscribed")
			}
			srv.publish(tc.message(0))
			if !waitFor(5*time.Second, func() bool { return countWatcherEvents(t, db, watcherID) == 1 }) {
				engA.Stop()
				t.Fatal("harness: live message never reached watcher_events")
			}
			engA.Stop()

			for i := 1; i <= backlog; i++ {
				srv.publish(tc.message(i))
			}

			engB := newIssue2538Engine(t, db, tc, watcherID, name, srv.srv.URL, topic)
			drainRouted(engB)
			defer engB.Stop()

			waitFor(10*time.Second, func() bool { return countWatcherEvents(t, db, watcherID) == backlog+1 })
			if got := countWatcherEvents(t, db, watcherID); got != backlog+1 {
				t.Fatalf("after restart watcher_events has %d rows, want %d (%d messages published while down were lost)",
					got, backlog+1, backlog+1-got)
			}
		})
	}
}

// TestIssue2538_ResumeSinceIsBounded covers how the persisted state turns
// into the first subscription's `since`: nothing for a fresh watcher, the
// message ID when it is recent, and a timestamp no older than the replay
// bound otherwise.
func TestIssue2538_ResumeSinceIsBounded(t *testing.T) {
	clock := newFakeClock()
	now := clock.Now()
	eng := NewEngine(EngineConfig{
		DB:            newTestDB(t),
		Clock:         clock,
		TriageSpawner: &fakeSpawner{},
		TriageDir:     t.TempDir(),
	})
	unix := func(ts time.Time) string { return strconv.FormatInt(ts.Unix(), 10) }

	cases := []struct {
		label    string
		state    *WatcherState // nil: no state.json (fresh watcher)
		settings map[string]string
		want     string
	}{
		{label: "fresh watcher streams only new messages", want: ""},
		{
			label: "recent cursor resumes after that message",
			state: &WatcherState{DedupCursor: "abc", DedupCursorTime: now.Add(-2 * time.Hour)},
			want:  "abc",
		},
		{
			label: "cursor older than the bound resumes from the bound",
			state: &WatcherState{DedupCursor: "abc", DedupCursorTime: now.Add(-72 * time.Hour)},
			want:  unix(now.Add(-DefaultResumeMaxAge)),
		},
		{
			label: "state from before #2538 resumes from the last event time",
			state: &WatcherState{LastEventTS: now.Add(-3 * time.Hour)},
			want:  unix(now.Add(-3 * time.Hour)),
		},
		{
			label: "state from before #2538 is still bounded",
			state: &WatcherState{LastEventTS: now.Add(-30 * 24 * time.Hour)},
			want:  unix(now.Add(-DefaultResumeMaxAge)),
		},
		{
			label:    "resume_max_age overrides the bound",
			state:    &WatcherState{DedupCursor: "abc", DedupCursorTime: now.Add(-2 * time.Hour)},
			settings: map[string]string{"resume_max_age": "1h"},
			want:     unix(now.Add(-time.Hour)),
		},
		{
			label:    "resume_max_age 0 turns replay off",
			state:    &WatcherState{DedupCursor: "abc", DedupCursorTime: now.Add(-time.Minute)},
			settings: map[string]string{"resume_max_age": "0"},
			want:     "",
		},
	}
	for i, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			name := fmt.Sprintf("i2538r-%d-%d", i, time.Now().UnixNano())
			if tc.state != nil {
				if err := SaveState(name, tc.state); err != nil {
					t.Fatalf("SaveState: %v", err)
				}
			}
			got := eng.resumeSince(AdapterConfig{Type: "ntfy", Name: name, Settings: tc.settings})
			if got != tc.want {
				t.Fatalf("resumeSince = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIssue2538_CursorPersistedAfterStore checks state.json carries the ID of
// the last stored message, including a replayed duplicate.
func TestIssue2538_CursorPersistedAfterStore(t *testing.T) {
	tc := issue2538Cases()[0]
	db := newTestDB(t)
	watcherID := "w-2538-cursor"
	name := fmt.Sprintf("i2538c-%d", time.Now().UnixNano())
	saveTestWatcher(t, db, watcherID, name, tc.typ)
	topic := "t2538c"
	srv := newFakeNtfyServer(t, topic)

	eng := newIssue2538Engine(t, db, tc, watcherID, name, srv.srv.URL, topic)
	drainRouted(eng)
	defer eng.Stop()
	if !waitFor(5*time.Second, func() bool { return len(srv.requests()) >= 1 }) {
		t.Fatal("harness: engine never subscribed")
	}
	id := srv.publish(tc.message(1))
	cursor := func() string {
		st, err := LoadState(name)
		if err != nil || st == nil {
			return ""
		}
		return st.DedupCursor
	}
	if !waitFor(5*time.Second, func() bool { return cursor() == id }) {
		t.Fatalf("state.json dedup_cursor = %q after storing %s, want %q", cursor(), id, id)
	}
}
