package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// openCodeCommsServer is sseTestServer plus the two HTTP reads the Comms
// Ledger producer makes: GET /session/:id (parentID) and
// GET /session/:id/message (the transcript).
func openCodeCommsServer(t *testing.T, events <-chan string, sessions map[string]string, messages map[string]string) int {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/session/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "{}")
	})
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path[len("/session/"):]
		if id, ok := cutSuffix(path, "/message"); ok {
			if body, ok := messages[id]; ok {
				fmt.Fprint(w, body)
				return
			}
			http.NotFound(w, r)
			return
		}
		if parent, ok := sessions[path]; ok {
			fmt.Fprintf(w, `{"id":%q,"parentID":%q}`, path, parent)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
		flusher.Flush()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", ev)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

func cutSuffix(s, suffix string) (string, bool) {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}

const openCodeRootMessages = `[
 {"info":{"id":"m1","role":"user","sessionID":"ses_root"},"parts":[{"type":"text","text":"[agent-deck from:parent-1] build it"}]},
 {"info":{"id":"m2","role":"assistant","sessionID":"ses_root"},"parts":[{"type":"text","text":"On it."},{"type":"tool","text":"ignored"}]},
 {"info":{"id":"m3","role":"assistant","sessionID":"ses_root"},"parts":[{"type":"text","text":"Built and tested.","synthetic":false},{"type":"text","text":"hidden","synthetic":true}]}
]`

func TestOpenCodeSSEWatcher_IdleSpoolsRootSessionText(t *testing.T) {
	t.Cleanup(SetCommsLedgerForTest(true))
	events := make(chan string, 8)
	port := openCodeCommsServer(t, events,
		map[string]string{"ses_root": "", "ses_child": "ses_root"},
		map[string]string{"ses_root": openCodeRootMessages})

	var mu sync.Mutex
	var spooled []CommsSpoolEntry
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.idleDebounce = 50 * time.Millisecond
	w.spool = func(e CommsSpoolEntry) error {
		mu.Lock()
		spooled = append(spooled, e)
		mu.Unlock()
		return nil
	}
	w.Sync([]SSETarget{{InstanceID: "inst-oc", Port: port}})

	// A false idle: busy again within the debounce spools nothing.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-oc", "running")
	events <- `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"idle"}}}`
	waitForStatus(t, w, "inst-oc", "waiting")
	events <- `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-oc", "running")
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	n := len(spooled)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("false idle spooled %d entries", n)
	}

	// A real idle on the CHILD session resolves to the root and spools its
	// newest assistant text with the prompt that started the turn.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"idle"}}}`
	waitForStatus(t, w, "inst-oc", "waiting")
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n = len(spooled)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spooled) != 1 {
		t.Fatalf("spooled %d entries", len(spooled))
	}
	e := spooled[0]
	if e.Harness != "opencode" || e.Edge != CommsEdgeTurnEnd || e.Instance != "inst-oc" || e.SessionID != "ses_root" ||
		e.TurnID != "m3" || e.Text != "Built and tested." || e.Prompt != "[agent-deck from:parent-1] build it" {
		t.Fatalf("spooled entry: %+v", e)
	}
	mu.Unlock()

	// The same message observed through another idle flip is not re-spooled.
	events <- `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-oc", "running")
	events <- `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"idle"}}}`
	waitForStatus(t, w, "inst-oc", "waiting")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if len(spooled) != 1 {
		t.Fatalf("same message re-spooled: %d entries", len(spooled))
	}
}

func TestOpenCodeSSEWatcher_IdleSpoolsNothingWithLedgerOff(t *testing.T) {
	t.Cleanup(SetCommsLedgerForTest(false))
	events := make(chan string, 4)
	port := openCodeCommsServer(t, events, map[string]string{"ses_root": ""}, map[string]string{"ses_root": openCodeRootMessages})
	w := NewOpenCodeSSEWatcher(nil)
	defer w.Stop()
	w.idleDebounce = 20 * time.Millisecond
	var called atomic.Bool
	w.spool = func(CommsSpoolEntry) error { called.Store(true); return nil }
	w.Sync([]SSETarget{{InstanceID: "inst-oc", Port: port}})
	events <- `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"busy"}}}`
	waitForStatus(t, w, "inst-oc", "running")
	events <- `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"idle"}}}`
	waitForStatus(t, w, "inst-oc", "waiting")
	time.Sleep(100 * time.Millisecond)
	if called.Load() {
		t.Fatal("spooled with the ledger off")
	}
}
