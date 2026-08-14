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
	Events map[string]string `json:"events"`
}

// Store persists the current notification identity per session. The first
// observation is a baseline: enabling/restarting never replays old state.
type Store struct {
	path string
	mu   sync.Mutex
	data storeData
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: storeData{Events: map[string]string{}}}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) reload() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = storeData{Events: map[string]string{}}
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
	return nil
}

func eventKey(event Event) string {
	return strings.Join([]string{string(event.Class), event.SessionID, event.Timestamp.UTC().Format(time.RFC3339Nano), event.Summary}, "\x00")
}

// Baseline records a currently-observed session state without creating an
// alert. The transition daemon calls this during its first pass after enabling
// notifications, so historical states never become a notification backlog.
func (s *Store) Baseline(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reload(); err != nil {
		return err
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
	if err := s.reload(); err != nil {
		return false, err
	}
	key := eventKey(event)
	previous, exists := s.data.Events[event.SessionID]
	s.data.Events[event.SessionID] = key
	if err := s.persist(); err != nil {
		return false, err
	}
	return !exists || previous != key, nil
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
	var event Event
	if err := json.NewDecoder(conn).Decode(&event); err != nil {
		return Event{}, fmt.Errorf("%w: %v", errMalformedPayload, err)
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
}

func (h Helper) ServeOne() error {
	if h.Listener == nil || h.Store == nil || h.Present == nil {
		return errors.New("desktop notification helper is not configured")
	}
	event, err := h.Listener.Receive()
	if err != nil {
		return err
	}
	deliver, err := h.Store.ShouldDeliver(event)
	if err != nil {
		return err
	}
	if !deliver {
		return nil
	}
	return h.Present(event)
}

func (h Helper) Serve() error {
	for {
		if err := h.ServeOne(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, errMalformedPayload) {
			return err
		}
	}
}
