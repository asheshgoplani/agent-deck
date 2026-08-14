package desktopnotify

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
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

func TestConcurrentStoresRetainEverySessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	left, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}

	const perStore = 80
	start := make(chan struct{})
	var wg sync.WaitGroup
	write := func(store *Store, prefix string) {
		defer wg.Done()
		<-start
		for i := 0; i < perStore; i++ {
			event := Event{Class: Attention, SessionID: fmt.Sprintf("%s-%d", prefix, i), Timestamp: time.Unix(int64(i), 0)}
			if err := store.Baseline(event); err != nil {
				t.Errorf("baseline %s: %v", event.SessionID, err)
				return
			}
		}
	}
	wg.Add(2)
	go write(left, "left")
	go write(right, "right")
	close(start)
	wg.Wait()

	restarted, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(restarted.data.Events), 2*perStore; got != want {
		t.Fatalf("persisted event count = %d, want %d; concurrent store writes lost state", got, want)
	}
}

const (
	helperStoreWriterStateEnv   = "AGENT_DECK_DESKTOP_NOTIFY_WRITER_STATE"
	helperStoreWriterReadyEnv   = "AGENT_DECK_DESKTOP_NOTIFY_WRITER_READY"
	helperStoreWriterReleaseEnv = "AGENT_DECK_DESKTOP_NOTIFY_WRITER_RELEASE"
	helperStoreLockPathEnv      = "AGENT_DECK_DESKTOP_NOTIFY_LOCK_PATH"
	helperStoreLockReadyEnv     = "AGENT_DECK_DESKTOP_NOTIFY_LOCK_READY"
	helperStoreLockReleaseEnv   = "AGENT_DECK_DESKTOP_NOTIFY_LOCK_RELEASE"
)

// TestDesktopNotificationHelperStoreWriter is re-executed in a separate test
// process by TestHelperStoreSubprocessAndDaemonStoreRetainEveryState. It uses
// ShouldDeliver, the same Store write path used by the GUI helper, while the
// parent executes daemon-style Baseline writes against the same state file.
func TestDesktopNotificationHelperStoreWriter(t *testing.T) {
	statePath := os.Getenv(helperStoreWriterStateEnv)
	if statePath == "" {
		return
	}
	store, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(helperStoreWriterReadyEnv), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := waitForDesktopNotifyTestFile(os.Getenv(helperStoreWriterReleaseEnv)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		event := Event{Class: Error, SessionID: fmt.Sprintf("helper-%d", i), Timestamp: time.Unix(int64(i), 0)}
		if deliver, err := store.ShouldDeliver(event); err != nil || !deliver {
			t.Fatalf("helper write %s: deliver=%v err=%v", event.SessionID, deliver, err)
		}
	}
}

func TestHelperStoreSubprocessAndDaemonStoreRetainEveryState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	readyPath := filepath.Join(t.TempDir(), "writer-ready")
	releasePath := filepath.Join(t.TempDir(), "writer-release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopNotificationHelperStoreWriter$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		helperStoreWriterStateEnv+"="+statePath,
		helperStoreWriterReadyEnv+"="+readyPath,
		helperStoreWriterReleaseEnv+"="+releasePath,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper writer: %v", err)
	}
	if err := waitForDesktopNotifyTestFile(readyPath); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("wait for helper writer readiness: %v\n%s", err, output.String())
	}

	store, err := OpenStore(statePath)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	parentWriterReady := make(chan struct{})
	parentWriterRelease := make(chan struct{})
	parentWriterDone := make(chan error, 1)
	go func() {
		close(parentWriterReady)
		<-parentWriterRelease
		for i := 0; i < 120; i++ {
			event := Event{Class: Attention, SessionID: fmt.Sprintf("daemon-%d", i), Timestamp: time.Unix(int64(i), 0)}
			if err := store.Baseline(event); err != nil {
				parentWriterDone <- fmt.Errorf("daemon baseline %s: %w", event.SessionID, err)
				return
			}
		}
		parentWriterDone <- nil
	}()
	<-parentWriterReady
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("release concurrent writers: %v", err)
	}
	close(parentWriterRelease)
	if err := <-parentWriterDone; err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper writer failed: %v\n%s", err, output.String())
	}

	restarted, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		for _, event := range []Event{
			{Class: Error, SessionID: fmt.Sprintf("helper-%d", i), Timestamp: time.Unix(int64(i), 0)},
			{Class: Attention, SessionID: fmt.Sprintf("daemon-%d", i), Timestamp: time.Unix(int64(i), 0)},
		} {
			if got := restarted.data.Events[event.SessionID]; got != eventKey(event) {
				t.Fatalf("persisted state for %q = %q, want %q", event.SessionID, got, eventKey(event))
			}
		}
	}
}

// TestDesktopNotificationHelperStoreLockHolder is the separate helper process
// used to hold the advisory lock while the daemon-side Store attempts its
// read-modify-write transaction.
func TestDesktopNotificationHelperStoreLockHolder(t *testing.T) {
	lockPath := os.Getenv(helperStoreLockPathEnv)
	if lockPath == "" {
		return
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	readyPath := os.Getenv(helperStoreLockReadyEnv)
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	releasePath := os.Getenv(helperStoreLockReleaseEnv)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(releasePath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for lock release")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHelperStoreSubprocessBlocksDaemonStoreOnAdvisoryLock(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	readyPath := filepath.Join(t.TempDir(), "lock-ready")
	releasePath := filepath.Join(t.TempDir(), "lock-release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopNotificationHelperStoreLockHolder$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		helperStoreLockPathEnv+"="+statePath+".lock",
		helperStoreLockReadyEnv+"="+readyPath,
		helperStoreLockReleaseEnv+"="+releasePath,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper lock holder: %v", err)
	}
	if err := waitForDesktopNotifyTestFile(readyPath); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("wait for helper lock: %v\n%s", err, output.String())
	}

	store, err := OpenStore(statePath)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	entered := make(chan struct{})
	event := Event{Class: Attention, SessionID: "daemon-during-helper-lock", Timestamp: time.Now()}
	go func() {
		close(entered)
		done <- store.Baseline(event)
	}()
	<-entered
	select {
	case err := <-done:
		_ = cmd.Process.Kill()
		t.Fatalf("daemon write bypassed helper advisory lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper lock holder failed: %v\n%s", err, output.String())
	}
	restarted, err := OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.data.Events[event.SessionID]; got != eventKey(event) {
		t.Fatalf("daemon event persisted after lock release = %q, want %q", got, eventKey(event))
	}
}

func waitForDesktopNotifyTestFile(path string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(time.Millisecond)
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

func TestHelperDropsIncompletePayloadWithoutEOFAndContinuesServing(t *testing.T) {
	socketFile, err := os.CreateTemp("", "adn-incomplete-*")
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

	stalled, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	if _, err := stalled.Write([]byte(`{"class":"error"`)); err != nil {
		t.Fatal(err)
	}

	want := Event{Class: Attention, SessionID: "valid-after-incomplete", Timestamp: time.Now()}
	if err := Send(socket, want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-delivered:
		if got.Class != want.Class || got.SessionID != want.SessionID || !got.Timestamp.Equal(want.Timestamp) {
			t.Fatalf("delivered %#v, want %#v", got, want)
		}
	case err := <-serveDone:
		t.Fatalf("helper stopped after incomplete payload: %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("incomplete client blocked a later valid payload")
	}
}
