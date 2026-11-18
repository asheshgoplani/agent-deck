package voicestream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// DefaultInterval is how often the producer stats the voice files. Two
// stats per poll; a file is read only when it changed. 50 ms keeps a phase
// change on the bus well inside the 1 s screen target (plan 2.3).
const DefaultInterval = 50 * time.Millisecond

// Producer publishes one voice.state frame per change of what screens draw,
// one at start, and "off" (reason expired) when the voice goes away: a dead
// daemon pid, "on": false, or a missed heartbeat window
// (voicestate.Reader). It is driven by Run or, in tests, by Step.
type Producer struct {
	Reader *voicestate.Reader
	// Publish puts a frame on the bus and reports whether the bus took it.
	Publish func(kind, sessionID string, data any) bool
	// Origin names this machine in frames from its own current.json.
	Origin   string
	Interval time.Duration
	Now      func() time.Time

	// quiet: stop without the "stopped" frame (a receiver handing over to
	// a newer one, which publishes its own start frame at once).
	quiet atomic.Bool

	mu      sync.Mutex
	started bool
	lastKey string
	last    StateEvent
	// lastOrigin is the origin of the newest live state: an "off" carries
	// it, so a follower ends the state of the daemon that went away (an
	// expired mirror's off must not carry this host's own name).
	lastOrigin string
}

// NewProducer is a producer for dir that publishes on profile's bus.
func NewProducer(dir, profile string) *Producer {
	return &Producer{
		Reader:  voicestate.NewReader(dir),
		Publish: func(kind, sid string, data any) bool { return events.TryPublishProfile(profile, kind, sid, data) },
		Origin:  LocalOrigin(),
	}
}

func (p *Producer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Step polls once and publishes when needed. It returns the event and
// whether it was published.
func (p *Producer) Step() (StateEvent, bool) {
	now := p.now()
	s := p.Reader.Poll()
	p.mu.Lock()
	defer p.mu.Unlock()
	reason := ReasonChange
	switch {
	case !p.started:
		reason = ReasonStart
	case s == nil || !s.On:
		reason = ReasonExpired
	}
	ev := FromState(s, p.Origin, reason, now)
	if !ev.On {
		ev.Origin = p.lastOrigin // the daemon that went away; "" when none was seen
	}
	key := ev.key()
	if p.started && key == p.lastKey {
		return p.last, false
	}
	// A frame the bus did not take is not recorded: the next Step offers
	// it again, so a dropped "off" cannot leave "listening" as the newest
	// frame.
	if p.Publish != nil && !p.Publish(KindState, ev.SessionID(), ev) {
		return ev, false
	}
	p.started, p.lastKey, p.last = true, key, ev
	if ev.On {
		p.lastOrigin = ev.Origin
	}
	return ev, true
}

// Last is the newest event this producer published (Off before the first).
func (p *Producer) Last() StateEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return Off(ReasonStart, p.Origin, p.now())
	}
	return p.last
}

// reset makes the next Step publish a start frame (after a lock takeover:
// another producer may have published since).
func (p *Producer) reset() {
	p.mu.Lock()
	p.started, p.lastKey = false, ""
	p.mu.Unlock()
}

// StopQuietly makes the end of Run publish nothing.
func (p *Producer) StopQuietly() { p.quiet.Store(true) }

// stop publishes "off" (reason stopped) when the last frame said the voice
// is on: with no producer left, an absence of frames must never read as
// "still listening". A producer that takes the lock over republishes the
// live state at once (reset).
func (p *Producer) stop() {
	if p.quiet.Load() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || !p.last.On {
		return
	}
	ev := Off(ReasonStopped, p.lastOrigin, p.now())
	p.last, p.lastKey = ev, ev.key()
	if p.Publish != nil {
		// Last chance: offer it a few times if the queue is full.
		for i := 0; i < 20 && !p.Publish(KindState, "", ev); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Run steps every Interval until ctx ends, then publishes the stop frame
// (see stop). It returns when ctx is done; the caller joins it.
func (p *Producer) Run(ctx context.Context) {
	iv := p.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	p.Step()
	for {
		select {
		case <-ctx.Done():
			p.stop()
			return
		case <-t.C:
			p.Step()
		}
	}
}

// RunExclusive runs p only while this process holds the profile's voice
// producer lock, so a host with several long-lived processes (relay
// receiver, web server) publishes each change once. A process that does
// not hold it retries every lockRetry and takes over when the holder exits.
// Returns when ctx is done.
func RunExclusive(ctx context.Context, profile string, p *Producer) error {
	path, err := lockPath(profile)
	if err != nil {
		return err
	}
	for {
		release, err := tryLock(path)
		if err == nil {
			if ctx.Err() != nil {
				release() // stopped while waiting: publish nothing
				return nil
			}
			p.reset()
			p.Run(ctx)
			release()
			return nil
		}
		wait := lockRetry
		if !errors.Is(err, errLocked) {
			// Not "held" (a transient open or flock error): keep trying,
			// slower, rather than leave the host without a producer.
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

const lockRetry = 100 * time.Millisecond

var errLocked = errors.New("voice producer lock held")

func lockPath(profile string) (string, error) {
	dir, err := events.DirFor(profile)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating bus dir: %w", err)
	}
	return filepath.Join(dir, "voice-producer.lock"), nil
}

func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- the bus dir's own lock file
	if err != nil {
		return nil, fmt.Errorf("opening voice producer lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, fmt.Errorf("locking voice producer lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
