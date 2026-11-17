package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
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
}

// RemoteStatusRunnerFor builds the runner for one remote name (nil: skip).
type RemoteStatusRunnerFor func(name string) RemoteStatusRunner

var remoteForwardBackoffMax = 30 * time.Second

type remoteStatusForwarder struct {
	name    string
	runner  RemoteStatusRunner
	publish func(kind, sessionID string, data any)
	// since is the last remote cursor republished (0: start from now).
	since   uint64
	startTS int64 // ms; frames older than the first connect are history, not news
}

// run follows until ctx ends, reconnecting with backoff and resuming from
// the last cursor (no frame lost or doubled across a reconnect).
func (f *remoteStatusForwarder) run(ctx context.Context) {
	log := logging.ForComponent(logging.CompWeb)
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		started := time.Now()
		err := f.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = 250 * time.Millisecond
		}
		log.Warn("remote_status_follow_ended", slog.String("remote", f.name), slog.String("error", fmt.Sprint(err)), slog.Duration("retry_in", backoff))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < remoteForwardBackoffMax {
			backoff *= 2
		}
	}
}

func (f *remoteStatusForwarder) once(ctx context.Context) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The forwarded follow ends when its stdin closes: keep one open for the
	// life of the connection.
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	outR, outW := io.Pipe()
	args := []string{"events", "follow", "--jsonl", "--kind", "session.status", "--since", strconv.FormatUint(f.since, 10)}
	done := make(chan error, 1)
	go func() {
		err := f.runner.RunReadIO(cctx, stdinR, outW, io.Discard, []byte("unsupported\n"), true, args...)
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
	return <-done
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
	if fr.TS < f.startTS {
		return // the remote's history, replayed on the first connect
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
			publish: func(kind, sid string, data any) { events.PublishProfile(profile, kind, sid, data) }}
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

// remoteForwardFields is embedded in Server.
type remoteForwardFields struct {
	remoteFwdNames  []string
	remoteFwdRunner RemoteStatusRunnerFor
	remoteFwdCancel context.CancelFunc
	remoteFwdWG     sync.WaitGroup
}
