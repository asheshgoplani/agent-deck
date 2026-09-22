package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Options configure a Server.
type Options struct {
	// Registry is the command catalog; the CLI passes its own registry so
	// both surfaces run the same Defs.
	Registry *core.Registry
	// Bus is streamed to subscribers; nil disables subscribe.
	Bus *events.Bus
	// Profile is the profile this daemon serves. A call input with a
	// Profile field gets it when empty and is refused when it names another.
	Profile string
	// OwnerUID is the only uid allowed to connect.
	OwnerUID int
	// Version is reported in hello and status.
	Version string
	// Socket is reported in hello and status.
	Socket string
}

// Server speaks the daemon protocol (docs/daemon-protocol.md).
type Server struct {
	opts      Options
	startedAt time.Time
	calls     atomic.Uint64
	conns     atomic.Int64
	slots     chan struct{}

	stopOnce sync.Once
	stop     chan struct{}
}

const frameReadTimeout = 2 * time.Second
const maxClients = 64

// New returns a server for opts.
func New(opts Options) *Server {
	return &Server{opts: opts, startedAt: time.Now(), slots: make(chan struct{}, maxClients), stop: make(chan struct{})}
}

// Serve accepts connections on ln until ctx is cancelled or a client sends
// shutdown, then closes ln and every open connection and returns nil.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		cancel()
		_ = ln.Close()
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = newFrameConn(c).write(errorFrame("", CodeServerBusy, "too many clients"))
			_ = c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-s.slots }()
			s.handle(ctx, c)
		}()
	}
}

func (s *Server) status() *Status {
	return &Status{
		PID:         os.Getpid(),
		Profile:     s.opts.Profile,
		Socket:      s.opts.Socket,
		Version:     s.opts.Version,
		StartedAt:   s.startedAt.UTC().Format(time.RFC3339),
		Calls:       s.calls.Load(),
		Connections: s.conns.Load(),
	}
}

// checkPeer admits only a Unix socket peer running as OwnerUID. Other
// transports need their own authentication before they can be admitted.
func (s *Server) checkPeer(c net.Conn) error {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("only unix socket peers are accepted")
	}
	uid, err := peerUID(uc)
	if err != nil {
		return err
	}
	if uid != s.opts.OwnerUID {
		return errors.New("peer uid is not the daemon owner")
	}
	return nil
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopClose()

	fc := newFrameConn(c)
	if err := s.checkPeer(c); err != nil {
		s.fatal(c, fc, errorFrame("", CodePeerRejected, "%v", err))
		return
	}
	s.conns.Add(1)
	defer s.conns.Add(-1)

	token, err := newToken()
	if err != nil {
		return
	}
	if fc.write(Frame{Type: TypeHello, Token: token, Status: s.status()}) != nil {
		return
	}

	subscribed := false
	for {
		readTimeout := frameReadTimeout
		if subscribed {
			readTimeout = streamIdleTimeout
		}
		_ = c.SetReadDeadline(time.Now().Add(readTimeout))
		f, err := fc.readStrict()
		_ = c.SetReadDeadline(time.Time{})
		switch {
		case errors.Is(err, errFrameTooLarge):
			s.fatal(c, fc, errorFrame("", CodeFrameTooLarge, "frame exceeds %d bytes", MaxFrameBytes))
			return
		case errors.Is(err, errBadFrame):
			s.fatal(c, fc, errorFrame("", CodeBadFrame, "%v", err))
			return
		case os.IsTimeout(err):
			s.fatal(c, fc, errorFrame("", CodeReadTimeout, "client frame read timed out"))
			return
		case err != nil:
			return
		}
		if subtle.ConstantTimeCompare([]byte(f.Token), []byte(token)) != 1 {
			s.fatal(c, fc, errorFrame(f.ID, CodeAuthFailed, "missing or wrong token"))
			return
		}
		if strings.TrimSpace(f.ID) == "" {
			s.fatal(c, fc, errorFrame("", CodeBadFrame, "client frame id is required"))
			return
		}
		if f.V != ProtocolVersion {
			_ = fc.write(errorFrame(f.ID, CodeUnsupportedVersion, "protocol version %d is not supported (want %d)", f.V, ProtocolVersion))
			continue
		}

		var reply Frame
		switch f.Type {
		case TypeCall:
			callCtx, cancelCall := context.WithTimeout(ctx, 7*time.Second)
			result, res := s.call(callCtx, f)
			err := fc.write(result)
			// Deferred work (journal writes) runs once the answer is out,
			// the same order the CLI uses.
			res.Finish()
			cancelCall()
			if err != nil {
				return
			}
			continue
		case TypeCatalog:
			reply = Frame{Type: TypeCatalog, ID: f.ID, Commands: catalog(s.opts.Registry)}
		case TypeStatus:
			reply = Frame{Type: TypeStatus, ID: f.ID, Status: s.status()}
		case TypeShutdown:
			_ = fc.write(Frame{Type: TypeStatus, ID: f.ID, Status: s.status()})
			s.stopOnce.Do(func() { close(s.stop) })
			return
		case TypeSubscribe:
			if subscribed {
				reply = errorFrame(f.ID, CodeAlreadySubscribed, "this connection already has a subscription")
				break
			}
			sub, errFrame := s.subscribe(ctx, f)
			if sub == nil {
				reply = errFrame
				break
			}
			subscribed = true
			if fc.write(Frame{Type: TypeSubscribed, ID: f.ID, After: f.After}) != nil {
				return
			}
			go s.stream(fc, f.ID, sub)
			continue
		default:
			reply = errorFrame(f.ID, CodeUnknownType, "unknown frame type %q", f.Type)
		}
		if fc.write(reply) != nil {
			return
		}
	}
}

// fatal sends a last error frame, half-closes, and drains what the client
// is still sending so it can read the error before the connection drops.
func (s *Server) fatal(c net.Conn, fc *frameConn, f Frame) {
	_ = fc.write(f)
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, _ = io.Copy(io.Discard, io.LimitReader(c, 4*MaxFrameBytes))
}

// call runs one command through the registry and answers with its
// envelope. The caller sends the reply, then releases res.Finish.
func (s *Server) call(ctx context.Context, f Frame) (Frame, *core.Result) {
	res := s.run(ctx, f.Cmd, f.Input)
	env, err := json.Marshal(res.Envelope(f.ID))
	if err != nil {
		env, _ = json.Marshal((&core.Result{ID: f.Cmd, Err: core.Errorf(core.CodeInternal, "encode envelope: %v", err)}).Envelope(f.ID))
	}
	s.calls.Add(1)
	return Frame{Type: TypeResult, ID: f.ID, Envelope: env}, res
}

func (s *Server) run(ctx context.Context, id string, input json.RawMessage) *core.Result {
	def, ok := s.opts.Registry.Lookup(id)
	if !ok {
		return &core.Result{ID: id, Err: core.Errorf(core.CodeUnknownCommand, "unknown command %q", id)}
	}
	if def.Remote != core.RemoteAllow {
		return &core.Result{ID: id, Err: core.Errorf(CodeRemoteDenied, "command %s is local-only", id)}
	}
	in := reflect.New(def.InType())
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(in.Interface()); err != nil {
		return &core.Result{ID: id, Err: core.Errorf(core.CodeInvalidInput, "input for %s: %v", id, err)}
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &core.Result{ID: id, Err: core.Errorf(core.CodeInvalidInput, "input for %s: trailing JSON value", id)}
	}
	if err := s.bindProfile(in.Elem()); err != nil {
		return &core.Result{ID: id, Err: err}
	}
	return core.RunWithMutationLock(ctx, s.opts.Registry, id, s.opts.Profile, in.Interface())
}

// bindProfile fills an empty Profile field with the daemon's profile and
// refuses input aimed at another profile's store.
func (s *Server) bindProfile(in reflect.Value) error {
	f := in.FieldByName("Profile")
	if !f.IsValid() || f.Kind() != reflect.String {
		return nil
	}
	switch f.String() {
	case "":
		f.SetString(s.opts.Profile)
	case s.opts.Profile:
	default:
		return core.Errorf(core.CodeInvalidInput, "this daemon serves profile %q, not %q", s.opts.Profile, f.String())
	}
	return nil
}

func (s *Server) subscribe(ctx context.Context, f Frame) (*events.Subscription, Frame) {
	if !s.opts.Bus.Stats().Enabled {
		return nil, errorFrame(f.ID, CodeEventsUnavailable, "the event bus is disabled")
	}
	sub, err := s.opts.Bus.Subscribe(ctx, events.Cursor(f.After))
	if err != nil {
		return nil, errorFrame(f.ID, CodeEventsUnavailable, "%v", err)
	}
	return sub, Frame{}
}

// stream forwards bus frames, byte-identical to `events follow --json` lines,
// until the subscription ends.
func (s *Server) stream(fc *frameConn, id string, sub *events.Subscription) {
	for ev := range sub.Frames() {
		line, err := ev.CanonicalJSON()
		if err != nil {
			continue
		}
		if fc.write(Frame{Type: TypeEvent, ID: id, Event: line}) != nil {
			return
		}
		if conn, ok := fc.w.(net.Conn); ok {
			_ = conn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
		}
	}
	if err := sub.Err(); err != nil {
		code := CodeEventsUnavailable
		if errors.Is(err, events.ErrCursorTooOld) {
			code = CodeCursorTooOld
		}
		_ = fc.write(errorFrame(id, code, "%v", err))
	}
}

func catalog(reg *core.Registry) []CommandInfo {
	defs := reg.Defs()
	out := make([]CommandInfo, 0, len(defs))
	for _, d := range defs {
		out = append(out, CommandInfo{ID: d.ID, CLI: d.CLI, Summary: d.Summary, Class: string(d.Class), Remote: string(d.Remote)})
	}
	return out
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
