package voicestream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// RelayRefresh re-sends an unchanged state so the receiving host's
// mirror.json stays inside its window.
const RelayRefresh = voicestate.RelayRefresh

// Sink takes the mirror for one stream host. It must not block for long;
// the ssh link (cmd/agent-deck) keeps only the newest one.
type Sink struct {
	Dest string // the remote's name, as this host's config names it
	Send func(voicestate.Mirror)
	// Ping keeps the link alive while there is nothing to send.
	Ping func()
}

// Relay polls the daemon's own state file (current.json only, never a
// mirror another host's relay wrote here) and hands each sink a mirror
// named from that sink's host:
//   - every change, and a refresh every Refresh while the voice is on;
//   - one "off" at start when there is no live daemon, and on every
//     on -> off change; while off it only pings, so an idle relay (a second
//     machine with no daemon) never overwrites another daemon's mirror;
//   - "off" when Run stops (the caller flushes it to every link).
type Relay struct {
	Reader   *voicestate.Reader
	Self     string // this machine's name as the stream hosts know it
	Sinks    []Sink
	Interval time.Duration
	Refresh  time.Duration
}

// Run polls until ctx ends, then sends "off" to every sink.
func (r *Relay) Run(ctx context.Context) {
	iv, refresh := r.Interval, r.Refresh
	if iv <= 0 {
		iv = DefaultInterval
	}
	if refresh <= 0 {
		refresh = RelayRefresh
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	lastKey, lastSent, started := "", time.Time{}, false
	send := func(st *voicestate.State, now time.Time) {
		for _, s := range r.Sinks {
			s.Send(voicestate.MirrorFor(st, s.Dest, r.Self, now.UnixMilli()))
		}
	}
	for {
		now := time.Now()
		st := r.Reader.PollCurrent()
		live := st != nil && st.On
		key := mirrorKey(st)
		switch {
		case !started || key != lastKey:
			send(st, now)
			lastKey, lastSent, started = key, now, true
		case now.Sub(lastSent) >= refresh:
			if live {
				send(st, now)
			} else {
				for _, s := range r.Sinks {
					if s.Ping != nil {
						s.Ping()
					}
				}
			}
			lastSent = now
		}
		select {
		case <-ctx.Done():
			if live {
				send(nil, time.Now())
			}
			return
		case <-t.C:
		}
	}
}

// mirrorKey changes whenever a mirror would change except its timestamp.
func mirrorKey(st *voicestate.State) string {
	raw, _ := json.Marshal(voicestate.MirrorFor(st, "\x00", "", 0))
	return string(raw)
}

// MirrorInIdle: a receiver that hears nothing (no mirror, no ping) for
// this long treats its link as gone (a dead route never sends FIN) and
// stops reading; its mirror then expires on RelayMirrorWindow.
var MirrorInIdle = 3 * voicestate.RelayRefresh

// PingLine is the relay's keepalive line while it has nothing to send.
const PingLine = `{"ping":1}`

// MirrorInLinger bounds how long a receiver whose relay went away keeps
// publishing: until its mirror expired (it has then said "off") or another
// receiver took the file over.
const MirrorInLinger = 2 * time.Minute

// MirrorIn is the receiving end on a stream host (`voice mirror-in`): it
// writes each mirror line from in to dir/mirror.json and publishes the
// result on profile's bus through an exclusive producer. After in ends it
// lingers (see MirrorInLinger) so the host says "off" when the mirror
// expires. log gets one line per problem, never the payload.
func MirrorIn(ctx context.Context, profile, dir string, in io.Reader, log io.Writer) error {
	pctx, cancelProd := context.WithCancel(ctx)
	prod := NewProducer(dir, profile)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := RunExclusive(pctx, profile, prod); err != nil {
			fmt.Fprintln(log, "voice mirror-in: producer:", err)
		}
	}()
	defer func() {
		cancelProd()
		wg.Wait()
	}()
	return mirrorInLoop(ctx, dir, in, log, prod)
}

// mirrorInLoop's line reader is cancelled on every return but not waited
// for: a Read blocked on stdin cannot be interrupted, so that goroutine ends
// with the process (the CLI exits right after MirrorIn returns).
func mirrorInLoop(ctx context.Context, dir string, in io.Reader, log io.Writer, prod *Producer) error {
	started := time.Now()
	var lastWrite time.Time
	lines := make(chan []byte)
	rctx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-rctx.Done():
				return
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintln(log, "voice mirror-in: input ended:", err)
		}
	}()
	idle := time.NewTimer(MirrorInIdle)
	defer idle.Stop()
	for open := true; open; {
		select {
		case <-ctx.Done():
			return nil
		case <-idle.C:
			fmt.Fprintln(log, "voice mirror-in: link silent, stopping")
			open = false
		case raw, ok := <-lines:
			if !ok {
				open = false
				break
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(MirrorInIdle)
			if string(raw) == PingLine {
				continue
			}
			var m voicestate.Mirror
			if err := json.Unmarshal(raw, &m); err != nil || m.Schema != voicestate.MirrorSchema {
				fmt.Fprintln(log, "voice mirror-in: ignoring a line that is not a mirror")
				continue
			}
			if owner, live := liveMirrorOwner(dir, time.Now()); live && owner != m.From {
				// Another daemon's relay holds the live mirror here: its
				// state stands until it goes off or expires (an idle second
				// relay must not blink it, two daemons must not alternate).
				continue
			}
			if err := voicestate.WriteMirror(dir, m); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
	}
	stopReader()
	// The relay went away (cut, stopped or silent). Keep the producer up
	// until the mirror has expired (it then publishes "off") or another
	// receiver writes the file, so a host is never left showing a phase.
	since := lastWrite
	if since.IsZero() {
		since = started
	}
	path := filepath.Join(dir, voicestate.SourceMirror)
	deadline := time.Now().Add(MirrorInLinger)
	for time.Now().Before(deadline) {
		if !prod.Last().On {
			return nil
		}
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(since.Add(time.Second)) {
			// A new receiver writes the file now and publishes its own start
			// frame: hand over with no "stopped" in between.
			prod.StopQuietly()
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil
}

// liveMirrorOwner is the sender ("from") of a live, on mirror in dir.
func liveMirrorOwner(dir string, now time.Time) (string, bool) {
	p := filepath.Join(dir, voicestate.SourceMirror)
	fi, err := os.Stat(p)
	if err != nil || now.Sub(fi.ModTime()) > voicestate.RelayMirrorWindow {
		return "", false
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- fixed name under the voice state dir
	if err != nil {
		return "", false
	}
	var m struct {
		On   bool   `json:"on"`
		From string `json:"from"`
	}
	if json.Unmarshal(raw, &m) != nil || !m.On {
		return "", false
	}
	return m.From, true
}
