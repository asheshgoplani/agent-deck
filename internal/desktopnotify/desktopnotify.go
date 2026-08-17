// Package desktopnotify contains the platform-neutral protocol for Agent
// Deck's actionable desktop notifications. The macOS helper is deliberately a
// separate process: it owns the GUI notification APIs while Agent Deck keeps
// session discovery and focus routing in its normal user process.
package desktopnotify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Class string

const (
	Complete  Class = "complete"
	Attention Class = "attention"
	Error     Class = "error"
)

// Event is the stable, local-only wire payload between Agent Deck and its GUI
// helper. BinaryPath is captured by the sender so a click still routes through
// the same installed Agent Deck binary after the helper has outlived it.
type Event struct {
	Class      Class     `json:"class"`
	SessionID  string    `json:"session_id"`
	Title      string    `json:"title"`
	Profile    string    `json:"profile,omitempty"`
	Project    string    `json:"project,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	BinaryPath string    `json:"binary_path,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// SourceEvent is the daemon-facing event shape. It avoids importing session,
// keeping this package usable by the helper and on non-macOS platforms.
type SourceEvent struct {
	SessionID  string
	Title      string
	Profile    string
	Project    string
	Kind       string
	ToStatus   string
	DoneStatus string
	Summary    string
	Timestamp  time.Time
}

func Normalize(source SourceEvent) (Event, bool) {
	id := strings.TrimSpace(source.SessionID)
	if id == "" {
		return Event{}, false
	}
	var class Class
	switch {
	case strings.EqualFold(strings.TrimSpace(source.Kind), "finished") || strings.TrimSpace(source.DoneStatus) != "":
		class = Complete
	case strings.EqualFold(strings.TrimSpace(source.ToStatus), "waiting") || strings.EqualFold(strings.TrimSpace(source.ToStatus), "idle"):
		class = Attention
	case strings.EqualFold(strings.TrimSpace(source.ToStatus), "error"):
		class = Error
	default:
		return Event{}, false
	}
	return Event{Class: class, SessionID: id, Title: strings.TrimSpace(source.Title), Profile: strings.TrimSpace(source.Profile), Project: strings.TrimSpace(source.Project), Summary: strings.TrimSpace(source.Summary), Timestamp: source.Timestamp}, true
}

func FocusCommand(binaryPath string, event Event) []string {
	if binaryPath == "" {
		binaryPath = "agent-deck"
	}
	args := []string{binaryPath}
	if event.Profile != "" {
		args = append(args, "--profile", event.Profile)
	}
	return append(args, "session", "focus", event.SessionID, "--attach")
}

type storeData struct {
	Events  map[string]string `json:"events"`
	Pending map[string]string `json:"pending,omitempty"`
}

// Store persists the current notification identity per session. The first
// observation is a baseline: enabling/restarting never replays old state.
type Store struct {
	path string
	mu   sync.Mutex
	data storeData
}

// storeLocks covers Store instances in the same process; the sibling advisory
// lock file below extends that critical section to the daemon and GUI helper.
// Both processes read-modify-write the same JSON state, so a Store-local mutex
// alone cannot prevent one writer from replacing another writer's new key.
var storeLocks sync.Map // map[string]*sync.Mutex

type storeLock struct {
	inProc *sync.Mutex
	file   *os.File
}

func (l *storeLock) release() {
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	if l.inProc != nil {
		l.inProc.Unlock()
	}
}

func acquireStoreLock(path string) (*storeLock, error) {
	mIface, _ := storeLocks.LoadOrStore(path, &sync.Mutex{})
	m := mIface.(*sync.Mutex)
	m.Lock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		m.Unlock()
		return nil, fmt.Errorf("ensure notification state dir: %w", err)
	}
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("open notification state lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		m.Unlock()
		return nil, fmt.Errorf("flock notification state: %w", err)
	}
	return &storeLock{inProc: m, file: f}, nil
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: storeData{Events: map[string]string{}, Pending: map[string]string{}}}
	if err := s.reload(); err != nil {
		return nil, err
	}
	if !s.pruneExpired(time.Now()) {
		return s, nil
	}
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return nil, err
	}
	if s.pruneExpired(time.Now()) {
		if err := s.persist(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) reload() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = storeData{Events: map[string]string{}, Pending: map[string]string{}}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read notification state: %w", err)
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return fmt.Errorf("parse notification state: %w", err)
	}
	if s.data.Events == nil {
		s.data.Events = map[string]string{}
	}
	if s.data.Pending == nil {
		s.data.Pending = map[string]string{}
	}
	return nil
}

func eventKey(event Event) string {
	return strings.Join([]string{string(event.Class), event.SessionID, event.Timestamp.UTC().Format(time.RFC3339Nano), event.Summary}, "\x00")
}

const dedupRetention = 30 * 24 * time.Hour

// pruneExpired removes identities outside the bounded deduplication window.
// Invalid legacy keys have no trustworthy age and are removed as well.
func (s *Store) pruneExpired(now time.Time) bool {
	cutoff := now.Add(-dedupRetention)
	pruned := false
	prune := func(events map[string]string) {
		for sessionID, key := range events {
			parts := strings.SplitN(key, "\x00", 4)
			if len(parts) < 3 {
				delete(events, sessionID)
				pruned = true
				continue
			}
			timestamp, err := time.Parse(time.RFC3339Nano, parts[2])
			if err != nil || timestamp.Before(cutoff) {
				delete(events, sessionID)
				pruned = true
			}
		}
	}
	prune(s.data.Events)
	prune(s.data.Pending)
	return pruned
}

// Baseline records a currently-observed session state without creating an
// alert. The transition daemon calls this during its first pass after enabling
// notifications, so historical states never become a notification backlog.
func (s *Store) Baseline(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return err
	}
	s.pruneExpired(time.Now())
	if _, pending := s.data.Pending[event.SessionID]; pending {
		return s.persist()
	}
	s.data.Events[event.SessionID] = eventKey(event)
	return s.persist()
}

// ShouldDeliver atomically records event and reports whether it differs from
// the established baseline/current event for its session. An unseen event is a
// genuine post-baseline transition and is therefore delivered.
func (s *Store) ShouldDeliver(event Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return false, err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return false, err
	}
	s.pruneExpired(time.Now())
	if pending, exists := s.data.Pending[event.SessionID]; exists && pending == eventKey(event) {
		return true, nil
	}
	key := eventKey(event)
	previous, exists := s.data.Events[event.SessionID]
	s.data.Events[event.SessionID] = key
	if err := s.persist(); err != nil {
		return false, err
	}
	return !exists || previous != key, nil
}

// NeedsDelivery reports whether event differs from the established state
// without recording it. The helper uses this before native presentation so a
// rejected notification remains eligible for retry.
func (s *Store) NeedsDelivery(event Event) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return false, err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return false, err
	}
	s.pruneExpired(time.Now())
	previous, exists := s.data.Events[event.SessionID]
	return !exists || previous != eventKey(event), nil
}

// MarkDelivered persists an event only after the presenter has accepted it.
func (s *Store) MarkDelivered(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return err
	}
	s.pruneExpired(time.Now())
	s.data.Events[event.SessionID] = eventKey(event)
	delete(s.data.Pending, event.SessionID)
	return s.persist()
}

// MarkPending preserves retry eligibility across daemon baseline seeding.
func (s *Store) MarkPending(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireStoreLock(s.path)
	if err != nil {
		return err
	}
	defer lock.release()
	if err := s.reload(); err != nil {
		return err
	}
	s.pruneExpired(time.Now())
	s.data.Pending[event.SessionID] = eventKey(event)
	return s.persist()
}

func (s *Store) persist() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".desktop-notifications-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

type Listener struct{ listener *net.UnixListener }

const socketReadDeadline = 250 * time.Millisecond

var errMalformedPayload = errors.New("malformed desktop notification payload")

func Listen(path string) (*Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket endpoint %q", path)
		}
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("desktop notification helper is already running")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return &Listener{listener: l}, nil
}

func (l *Listener) Close() error { return l.listener.Close() }

func (l *Listener) Receive() (Event, error) {
	conn, err := l.listener.AcceptUnix()
	if err != nil {
		return Event{}, err
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(socketReadDeadline)); err != nil {
		return Event{}, fmt.Errorf("set desktop notification read deadline: %w", err)
	}
	var event Event
	if err := json.NewDecoder(conn).Decode(&event); err != nil {
		return Event{}, fmt.Errorf("%w: %v", errMalformedPayload, err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil { // clear after decoding for future protocol extensions
		return Event{}, fmt.Errorf("clear desktop notification read deadline: %w", err)
	}
	return event, nil
}

func Send(path string, event Event) error {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer conn.Close()
	return json.NewEncoder(conn).Encode(event)
}

func EndpointHealthy(path string) bool {
	conn, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err != nil {
		return false
	}
	return conn.Close() == nil
}

// Helper receives local events and delegates rendering to the platform-native
// presenter. Keeping the presenter injectable makes the socket/state logic
// testable on every supported Go platform.
type Helper struct {
	Listener *Listener
	Store    *Store
	Present  func(Event) error
	// RetryDelay is injectable for tests. Zero uses a conservative retry delay
	// so a temporarily unresolved macOS authorization does not lose its event.
	RetryDelay time.Duration
}

var errPresentationRejected = errors.New("desktop notification presentation rejected")

func (h Helper) presentEvent(event Event) error {
	deliver, err := h.Store.NeedsDelivery(event)
	if err != nil {
		return err
	}
	if !deliver {
		return nil
	}
	if err := h.Present(event); err != nil {
		return fmt.Errorf("%w: %v", errPresentationRejected, err)
	}
	return h.Store.MarkDelivered(event)
}

func (h Helper) retryDelay() time.Duration {
	if h.RetryDelay > 0 {
		return h.RetryDelay
	}
	return time.Second
}

func (h Helper) ServeOne() error {
	if h.Listener == nil || h.Store == nil || h.Present == nil {
		return errors.New("desktop notification helper is not configured")
	}
	event, err := h.Listener.Receive()
	if err != nil {
		return err
	}
	return h.presentEvent(event)
}

func (h Helper) Serve() error {
	if h.Listener == nil || h.Store == nil || h.Present == nil {
		return errors.New("desktop notification helper is not configured")
	}
	type retry struct {
		event  Event
		key    string
		cancel chan struct{}
	}
	type retryState struct {
		sync.Mutex
		pending  map[string]*retry
		fatalErr error
		stopped  bool
	}
	state := retryState{pending: make(map[string]*retry)}
	var presenterMu sync.Mutex
	defer func() {
		state.Lock()
		state.stopped = true
		for _, work := range state.pending {
			close(work.cancel)
		}
		state.pending = make(map[string]*retry)
		state.Unlock()
	}()

	isCurrent := func(work *retry) bool {
		state.Lock()
		defer state.Unlock()
		return !state.stopped && state.pending[work.event.SessionID] == work
	}
	recordFatal := func(work *retry, err error) {
		state.Lock()
		shouldStop := !state.stopped && state.pending[work.event.SessionID] == work && state.fatalErr == nil
		if shouldStop {
			state.fatalErr = err
		}
		state.Unlock()
		if shouldStop {
			_ = h.Listener.Close()
		}
	}

	startRetry := func(event Event) {
		key := eventKey(event)
		state.Lock()
		previous := state.pending[event.SessionID]
		if previous != nil && previous.key == key {
			state.Unlock()
			return
		}
		if previous != nil {
			close(previous.cancel)
		}
		work := &retry{event: event, key: key, cancel: make(chan struct{})}
		state.pending[event.SessionID] = work
		state.Unlock()
		if err := h.Store.MarkPending(event); err != nil {
			recordFatal(work, err)
			return
		}

		go func() {
			defer func() {
				state.Lock()
				if state.pending[event.SessionID] == work {
					delete(state.pending, event.SessionID)
				}
				state.Unlock()
			}()
			for {
				if !isCurrent(work) {
					return
				}
				deliver, err := h.Store.NeedsDelivery(event)
				if err != nil {
					recordFatal(work, err)
					return
				}
				if !deliver || !isCurrent(work) {
					return
				}

				presenterMu.Lock()
				if !isCurrent(work) {
					presenterMu.Unlock()
					return
				}
				err = h.Present(event)
				presenterMu.Unlock()
				if !isCurrent(work) {
					return
				}
				if err == nil {
					state.Lock()
					if !state.stopped && state.pending[event.SessionID] == work {
						err = h.Store.MarkDelivered(event)
					}
					state.Unlock()
					if err != nil {
						recordFatal(work, err)
					}
					return
				}
				timer := time.NewTimer(h.retryDelay())
				select {
				case <-work.cancel:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				case <-timer.C:
				}
			}
		}()
	}

	for {
		event, err := h.Listener.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, errMalformedPayload) {
				continue
			}
			state.Lock()
			fatalErr := state.fatalErr
			state.Unlock()
			if fatalErr != nil {
				return fatalErr
			}
			return err
		}
		startRetry(event)
	}
}
