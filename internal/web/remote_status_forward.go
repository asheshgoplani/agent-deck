package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Remote sessions in the state stream (plan gap G8). For each remote named
// in Config.StreamRemotes the server follows that host's own session.status
// frames over ssh (`remote <name> events follow`, the Mac app's path, about
// 0.1 s) and republishes each on this host's bus with data.host = <name>, so
// GET /events/state (and `events follow` here) carries local and remote
// status in one cursor order.
//
// Only session.status crosses: a remote's voice.state is not forwarded (the
// stream host has its own, from the relay's mirror), and a frame that already
// carries a host (a remote's own forward) is dropped, so two hosts that
// stream each other never loop.

// RemoteStatusRunner is the part of session.SSHRunner the forwarder uses.
type RemoteStatusRunner interface {
	RunReadIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, unsupported []byte, follow bool, args ...string) error
	FetchSessions(ctx context.Context) ([]session.RemoteSessionInfo, *session.ListStats, error)
}

// RemoteStatusRunnerFor builds the runner for one remote name (nil: skip).
type RemoteStatusRunnerFor func(name string) RemoteStatusRunner

// remoteForwardBackoffMax caps the reconnect wait: remote status is current
// again about a second after a link returns.
const remoteForwardBackoffMax = time.Second

// KindSessionLink is the frame a stream host publishes about one remote's
// link: up (with the remote's current statuses, a resync after any gap) or
// down (until the next up, that remote's status may be stale).
const KindSessionLink = "session.link"

// SessionLinkEvent is the data of a session.link frame.
type SessionLinkEvent struct {
	Host     string              `json:"host"`
	Sessions []SessionLinkStatus `json:"sessions,omitempty"`
	Up       bool                `json:"up"`
}

// SessionLinkStatus is one remote session's status in a resync.
type SessionLinkStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

type remoteStatusForwarder struct {
	name    string
	runner  RemoteStatusRunner
	publish func(kind, sessionID string, data any)
	// since is the last remote cursor republished; 0 = read the remote's
	// current cursor (`events stats`) before following.
	since uint64
	// startTS is kept for older callers; liveness never compares clocks.
	startTS int64
	up      bool
}

// run follows until ctx ends, reconnecting within a second and resuming
// from the last cursor (no frame lost or doubled across a reconnect). Each
// connect publishes session.link up with the remote's statuses; each drop
// publishes session.link down.
func (f *remoteStatusForwarder) run(ctx context.Context) {
	log := logging.ForComponent(logging.CompWeb)
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		err := f.once(ctx)
		if f.up {
			f.up = false
			f.publish(KindSessionLink, "", SessionLinkEvent{Host: f.name, Up: false})
			backoff = 250 * time.Millisecond // it was up: retry fast
		}
		if ctx.Err() != nil {
			return
		}
		log.Warn("remote_status_follow_ended", slog.String("remote", f.name), slog.String("error", fmt.Sprint(err)), slog.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > remoteForwardBackoffMax {
			backoff = remoteForwardBackoffMax
		}
	}
}

// remoteCursor is the remote bus's newest cursor (`events stats --json`).
func (f *remoteStatusForwarder) remoteCursor(ctx context.Context) (uint64, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := f.runner.RunReadIO(cctx, nil, &out, io.Discard, []byte("unsupported\n"), false, "events", "stats", "--json", "--read-only"); err != nil {
		return 0, fmt.Errorf("reading the remote cursor: %w", err)
	}
	var st struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		return 0, fmt.Errorf("decoding the remote cursor: %w", err)
	}
	return st.Cursor, nil
}

// resync publishes session.link up with the remote's current statuses.
func (f *remoteStatusForwarder) resync(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, _, err := f.runner.FetchSessions(cctx)
	if err != nil {
		return fmt.Errorf("listing the remote's sessions: %w", err)
	}
	ev := SessionLinkEvent{Host: f.name, Up: true, Sessions: make([]SessionLinkStatus, 0, len(list))}
	for _, rs := range list {
		ev.Sessions = append(ev.Sessions, SessionLinkStatus{ID: rs.ID, Status: rs.Status, Title: rs.Title})
	}
	f.up = true
	f.publish(KindSessionLink, "", ev)
	return nil
}

// tooOldMarker is what a follow says for a cursor the remote no longer has.
const tooOldMarker = "older than the retained log"

func (f *remoteStatusForwarder) once(ctx context.Context) error {
	if f.since == 0 {
		c, err := f.remoteCursor(ctx)
		if err != nil {
			return err
		}
		f.since = c
	}
	// Statuses first, then the follow from the cursor: a change in between
	// arrives twice (snapshot, frame), never not at all.
	if err := f.resync(ctx); err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The forwarded follow ends when its stdin closes: keep one open for the
	// life of the connection.
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	outR, outW := io.Pipe()
	var stderr capWriter
	args := []string{"events", "follow", "--jsonl", "--kind", "session.status", "--since", strconv.FormatUint(f.since, 10)}
	done := make(chan error, 1)
	go func() {
		err := f.runner.RunReadIO(cctx, stdinR, outW, &stderr, []byte("unsupported\n"), true, args...)
		_ = outW.CloseWithError(io.EOF)
		done <- err
	}()
	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		f.forward(sc.Bytes())
	}
	cancel()
	_ = outR.Close()
	err := <-done
	if strings.Contains(stderr.String(), tooOldMarker) {
		f.since = 0 // re-read the remote's cursor; the resync covers the gap
	}
	return err
}

// capWriter keeps the first 4 KiB written (a remote's diagnostic).
type capWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if room := 4096 - len(w.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf = append(w.buf, p[:room]...)
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// forward republishes one remote frame line.
func (f *remoteStatusForwarder) forward(line []byte) {
	fr, err := events.ParseFrameLine(line)
	if err != nil || uint64(fr.Cursor) <= f.since {
		return
	}
	// Every frame read moves the resume point, whatever its kind.
	f.since = uint64(fr.Cursor)
	if fr.Kind != "session.status" {
		return
	}
	var data map[string]any
	if len(fr.Data) == 0 || json.Unmarshal(fr.Data, &data) != nil {
		return
	}
	if h, _ := data["host"].(string); h != "" {
		return // already a forward on that host: never loop
	}
	data["host"] = f.name
	data["remote_cursor"] = uint64(fr.Cursor)
	f.publish("session.status", fr.SessionID, data)
}

// SetRemoteStatusStream names the remotes Start will follow.
func (s *Server) SetRemoteStatusStream(names []string, runnerFor RemoteStatusRunnerFor) {
	s.remoteFwdNames, s.remoteFwdRunner = names, runnerFor
}

// startRemoteStatusForward starts one forwarder per configured name.
// Joined by stopRemoteStatusForward.
func (s *Server) startRemoteStatusForward(names []string, runnerFor RemoteStatusRunnerFor) {
	if len(names) == 0 || runnerFor == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.remoteFwdCancel = cancel
	profile := s.stateProfile()
	now := time.Now().UnixMilli()
	for _, name := range names {
		r := runnerFor(name)
		if r == nil {
			continue
		}
		f := &remoteStatusForwarder{name: name, runner: r, startTS: now,
			publish: func(kind, sid string, data any) {
				if link, ok := data.(SessionLinkEvent); ok {
					s.noteRemoteLink(link)
				}
				events.PublishProfile(profile, kind, sid, data)
			}}
		s.remoteFwdWG.Add(1)
		go func() {
			defer s.remoteFwdWG.Done()
			f.run(ctx)
		}()
	}
}

func (s *Server) stopRemoteStatusForward() {
	if s.remoteFwdCancel != nil {
		s.remoteFwdCancel()
	}
	s.remoteFwdWG.Wait()
}

// noteRemoteLink keeps the newest session.link of one remote for hello.
func (s *Server) noteRemoteLink(l SessionLinkEvent) {
	s.remoteLinksMu.Lock()
	defer s.remoteLinksMu.Unlock()
	if s.remoteLinksByHost == nil {
		s.remoteLinksByHost = map[string]SessionLinkEvent{}
	}
	s.remoteLinksByHost[l.Host] = l
}

// remoteLinks is the newest session.link per followed remote, by name.
func (s *Server) remoteLinks() []SessionLinkEvent {
	s.remoteLinksMu.Lock()
	defer s.remoteLinksMu.Unlock()
	if len(s.remoteLinksByHost) == 0 {
		return nil
	}
	out := make([]SessionLinkEvent, 0, len(s.remoteLinksByHost))
	for _, l := range s.remoteLinksByHost {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// remoteForwardFields is embedded in Server.
type remoteForwardFields struct {
	remoteLinksMu     sync.Mutex
	remoteLinksByHost map[string]SessionLinkEvent
	remoteFwdNames    []string
	remoteFwdRunner   RemoteStatusRunnerFor
	remoteFwdCancel   context.CancelFunc
	remoteFwdWG       sync.WaitGroup
}
