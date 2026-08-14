package desktopnotify

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeEvent(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input SourceEvent
		want  Event
		ok    bool
	}{
		{
			name:  "completion",
			input: SourceEvent{SessionID: "worker-1", Title: "Worker", Profile: "work", Project: "/src/app", Kind: "finished", DoneStatus: "ok", Timestamp: now},
			want:  Event{Class: Complete, SessionID: "worker-1", Title: "Worker", Profile: "work", Project: "/src/app", Timestamp: now}, ok: true,
		},
		{
			name:  "waiting needs attention",
			input: SourceEvent{SessionID: "worker-1", Title: "Worker", ToStatus: "waiting", Timestamp: now},
			want:  Event{Class: Attention, SessionID: "worker-1", Title: "Worker", Timestamp: now}, ok: true,
		},
		{
			name:  "error",
			input: SourceEvent{SessionID: "worker-1", Title: "Worker", ToStatus: "error", Timestamp: now},
			want:  Event{Class: Error, SessionID: "worker-1", Title: "Worker", Timestamp: now}, ok: true,
		},
		{name: "running ignored", input: SourceEvent{SessionID: "worker-1", ToStatus: "running"}, ok: false},
		{name: "missing session ignored", input: SourceEvent{ToStatus: "error"}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Normalize(tt.input)
			if ok != tt.ok || (ok && !reflect.DeepEqual(got, tt.want)) {
				t.Fatalf("Normalize(%+v) = (%+v, %v), want (%+v, %v)", tt.input, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestStoreBaselineAndDeduplicatesPersistently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	event := Event{Class: Attention, SessionID: "worker-1", Title: "Worker", Timestamp: now}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Baseline(event); err != nil {
		t.Fatal(err)
	}
	if deliver, err := store.ShouldDeliver(event); err != nil || deliver {
		t.Fatal("baseline event must not alert")
	}
	if deliver, err := store.ShouldDeliver(event); err != nil || deliver {
		t.Fatal("same event must be deduplicated")
	}
	changed := event
	changed.Timestamp = now.Add(time.Second)
	if deliver, err := store.ShouldDeliver(changed); err != nil || !deliver {
		t.Fatal("a new event must deliver after the baseline")
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if deliver, err := store.ShouldDeliver(changed); err != nil || deliver {
		t.Fatal("persisted state must deduplicate after restart")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file was not persisted: %v", err)
	}
}

func TestStoreDeliversFirstNewEventAfterDaemonBaseline(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Class: Complete, SessionID: "worker-1", Timestamp: time.Now()}
	if deliver, err := store.ShouldDeliver(event); err != nil || !deliver {
		t.Fatal("a post-baseline event must be delivered")
	}
}

func TestPersistedBaselineSuppressesRestartBacklog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	event := Event{Class: Complete, SessionID: "completed-before-restart", Timestamp: time.Now()}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Baseline(event); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if deliver, err := restarted.ShouldDeliver(event); err != nil || deliver {
		t.Fatalf("restarted store delivered baseline event: deliver=%v err=%v", deliver, err)
	}
}

func TestFocusCommandIncludesStableBinaryAndProfile(t *testing.T) {
	got := FocusCommand("/Applications/Agent Deck.app/Contents/MacOS/agent-deck", Event{SessionID: "session-1", Profile: "work"})
	want := []string{"/Applications/Agent Deck.app/Contents/MacOS/agent-deck", "--profile", "work", "session", "focus", "session-1", "--attach"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FocusCommand() = %#v, want %#v", got, want)
	}
}

func TestClientSendsEventToPrivateSocket(t *testing.T) {
	tmp, err := os.CreateTemp("", "adn-socket-*")
	if err != nil {
		t.Fatal(err)
	}
	socket := tmp.Name()
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(socket)
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	want := Event{Class: Error, SessionID: "session-1", Title: "Worker"}
	done := make(chan Event, 1)
	go func() { got, _ := listener.Receive(); done <- got }()
	if err := Send(socket, want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("received %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for socket event")
	}
}

func TestHelperDeliversOnlyDistinctEvents(t *testing.T) {
	socketFile, err := os.CreateTemp("", "adn-helper-*")
	if err != nil {
		t.Fatal(err)
	}
	socket := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(socket)
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var delivered []Event
	helper := Helper{Listener: listener, Store: store, Present: func(event Event) error { delivered = append(delivered, event); return nil }}
	event := Event{Class: Error, SessionID: "session-1", Timestamp: time.Now()}
	for i := 0; i < 2; i++ {
		done := make(chan error, 1)
		go func() { done <- helper.ServeOne() }()
		if err := Send(socket, event); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered %d events, want one", len(delivered))
	}
}

func TestHelperDropsMalformedPayloadAndContinuesServing(t *testing.T) {
	socketFile, err := os.CreateTemp("", "adn-malformed-*")
	if err != nil {
		t.Fatal(err)
	}
	socket := socketFile.Name()
	if err := socketFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(socket)
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan Event, 1)
	helper := Helper{Listener: listener, Store: store, Present: func(event Event) error { delivered <- event; return nil }}
	serveDone := make(chan error, 1)
	go func() { serveDone <- helper.Serve() }()

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("not-json\n")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	want := Event{Class: Attention, SessionID: "valid-after-malformed", Timestamp: time.Now()}
	if err := Send(socket, want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got.Class != want.Class || got.SessionID != want.SessionID || !got.Timestamp.Equal(want.Timestamp) {
			t.Fatalf("delivered %#v, want %#v", got, want)
		}
	case err := <-serveDone:
		t.Fatalf("helper stopped after malformed payload: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for valid payload after malformed input")
	}
}
