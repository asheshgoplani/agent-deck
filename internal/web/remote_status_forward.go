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
	// Cursor is the remote cursor read before the listing: the listing
	// covers every remote frame at or before it. Not sent; it orders the
	// forwarded frames merged into the stored snapshot.
	Cursor uint64 `json:"-"`
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
	// snapCursor is the remote cursor the last listing covers: a frame at
	// or before it (a replay after a reconnect) is older than that listing.
	snapCursor uint64
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
	c, _, err := f.remoteLog(ctx)
	return c, err
}

// remoteLog is the remote bus's newest cursor and whether that bus is
// enabled (a disabled bus reports cursor 0, which says nothing about its
// log). A reply without "enabled" counts as enabled.
func (f *remoteStatusForwarder) remoteLog(ctx context.Context) (uint64, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := f.runner.RunReadIO(cctx, nil, &out, io.Discard, []byte("unsupported\n"), false, "events", "stats", "--json", "--read-only"); err != nil {
		return 0, false, fmt.Errorf("reading the remote cursor: %w", err)
	}
	var st struct {
		Cursor  uint64 `json:"cursor"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		return 0, false, fmt.Errorf("decoding the remote cursor: %w", err)
	}
	return st.Cursor, st.Enabled == nil || *st.Enabled, nil
}

// resync publishes session.link up with the remote's current statuses. The
// remote cursor is read first, so the listing covers every frame up to it:
// a frame the follow replays at or before that cursor (one from while the
// link was down) is older than the listing and is not forwarded, so it can
// not bring back a session deleted meanwhile or undo a newer status.
func (f *remoteStatusForwarder) resync(ctx context.Context, cursor uint64) error {
	if cursor == 0 {
		c, err := f.remoteCursor(ctx)
		if err != nil {
			return err
		}
		cursor = c
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, _, err := f.runner.FetchSessions(cctx)
	if err != nil {
		return fmt.Errorf("listing the remote's sessions: %w", err)
	}
	f.snapCursor = cursor
	ev := SessionLinkEvent{Host: f.name, Up: true, Cursor: cursor, Sessions: make([]SessionLinkStatus, 0, len(list))}
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
	// The remote's cursor is read on every connect: it starts the first
	// follow and bounds the listing the resync takes.
	cur, enabled, err := f.remoteLog(ctx)
	if err != nil {
		return err
	}
	if f.since == 0 || (enabled && cur < f.since) {
		// First connect, or the remote's log moved backward (it was
		// recreated): resume from its current cursor. Frames above the
		// old resume cursor would otherwise all look already seen and be
		// dropped; the resync below covers everything up to cur.
		f.since = cur
	}
	// Cursor, then statuses, then the follow from the resume cursor: a
	// change after the cursor arrives twice (snapshot, frame), never not at
	// all; one at or before it is in the snapshot and is not forwarded.
	if err := f.resync(ctx, cur); err != nil {
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
	err = <-done
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
	if fr.Kind != "session.status" || uint64(fr.Cursor) <= f.snapCursor {
		return // a replay the session.link up already covers
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
				switch d := data.(type) {
				case SessionLinkEvent:
					s.noteRemoteLink(d)
				case map[string]any:
					if to, _ := d["to"].(string); kind == "session.status" && to != "" {
						rc, _ := d["remote_cursor"].(uint64)
						s.noteRemoteStatus(name, sid, to, rc)
					}
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

// noteRemoteStatus applies one forwarded status to the stored snapshot, so
// hello.remotes is the remote's current state, not the last link-up's. It
// is ordered by the remote cursor: a frame at or before the newest one the
// snapshot holds (the listing's cursor or a later frame already applied)
// is older than that state, so it neither overwrites a status nor adds back
// a session the listing left out.
func (s *Server) noteRemoteStatus(host, id, status string, cursor uint64) {
	s.remoteLinksMu.Lock()
	defer s.remoteLinksMu.Unlock()
	l, ok := s.remoteLinksByHost[host]
	if !ok || !l.Up || id == "" || cursor <= l.Cursor {
		return
	}
	l.Cursor = cursor
	sessions := make([]SessionLinkStatus, 0, len(l.Sessions)+1)
	found := false
	for _, st := range l.Sessions {
		if st.ID == id {
			st.Status, found = status, true
		}
		sessions = append(sessions, st)
	}
	if !found {
		sessions = append(sessions, SessionLinkStatus{ID: id, Status: status})
	}
	l.Sessions = sessions
	s.remoteLinksByHost[host] = l
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
