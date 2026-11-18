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

// Relay polls the daemon's state file and hands every change (and a
// refresh every RelayRefresh) to each sink as a mirror named from that
// sink's host.
type Relay struct {
	Reader   *voicestate.Reader
	Self     string // this machine's name as the stream hosts know it
	Sinks    []Sink
	Interval time.Duration
	Refresh  time.Duration
}

// Run polls until ctx ends.
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
	lastKey, lastSent := "", time.Time{}
	for {
		now := time.Now()
		st := r.Reader.Poll()
		key := mirrorKey(st)
		if key != lastKey || now.Sub(lastSent) >= refresh {
			for _, s := range r.Sinks {
				s.Send(voicestate.MirrorFor(st, s.Dest, r.Self, now.UnixMilli()))
			}
			lastKey, lastSent = key, now
		}
		select {
		case <-ctx.Done():
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
// this long treats its link as gone. (Rework shim: unused.)
var MirrorInIdle = 3 * voicestate.RelayRefresh

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
	for open := true; open; {
		select {
		case <-ctx.Done():
			return nil
		case raw, ok := <-lines:
			if !ok {
				open = false
				break
			}
			var m voicestate.Mirror
			if err := json.Unmarshal(raw, &m); err != nil || m.Schema != voicestate.MirrorSchema {
				fmt.Fprintln(log, "voice mirror-in: ignoring a line that is not a mirror")
				continue
			}
			if err := voicestate.WriteMirror(dir, m); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
	}
	path := filepath.Join(dir, voicestate.SourceMirror)
	deadline := time.Now().Add(MirrorInLinger)
	for time.Now().Before(deadline) {
		if !prod.Last().On {
			return nil
		}
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(lastWrite.Add(time.Second)) {
			// A new receiver owns the file now and publishes its own start
			// frame: no "stopped" in between.
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
