package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Batched pane capture for one-shot status passes (`list --json`, the
// remote agent's change probe, `group list`, ...).
//
// A status pass used to start one `tmux capture-pane` process per live
// session: GetStatus and Substate both read CapturePane, whose 500ms cache
// only deduplicates within one session. On a host with many live sessions
// that is N fork+exec+server round trips per listing, and remote clients run
// that listing every few seconds — under memory pressure the forks are what
// make a listing slow enough for polls to pile up.
//
// PrimePaneCaptures captures every given pane with ONE tmux client per socket
// by chaining the commands tmux already accepts as a sequence:
//
//	capture-pane -t A -p -e ; display-message -p MARK-0 ; capture-pane -t B -p -e ; display-message -p MARK-1 ...
//
// Each capture's bytes are exactly what a lone `capture-pane -t X -p -e`
// prints, delimited by a per-call random marker line, and are stored as the
// session's CapturePane cache for capturePrimeTTL. A session whose frame the
// batch could not deliver is simply left unprimed and captured on its own as
// before, so the batch can only remove subprocesses, never change answers.

const (
	// captureBatchMax bounds the argv of one batched client.
	captureBatchMax = 64
	// capturePrimeTTL is how long a primed frame serves CapturePane. It
	// covers one status pass on a loaded host; the next pass re-primes.
	capturePrimeTTL = 3 * time.Second
	// captureBatchRetries bounds how often a batch is resumed after a pane
	// vanished mid-sequence (tmux aborts the rest of a sequence when one
	// command fails).
	captureBatchRetries = 2
)

// captureBatchRun runs one batched tmux client and returns its stdout. A
// package var so tests can count and shape batches without a tmux server.
var captureBatchRun = func(ctx context.Context, socketName string, args []string) ([]byte, error) {
	return commandOutput(tmuxExecContext(ctx, socketName, args...))
}

// PrimePaneCaptures fills the CapturePane cache of every session in sessions
// with one tmux invocation per socket (chunked at captureBatchMax). It returns
// how many sessions were primed. Fewer than two capturable sessions is not
// worth a batch and primes nothing.
func PrimePaneCaptures(sessions []*Session) int {
	pm := GetPipeManager()
	bySocket := make(map[string][]*Session)
	var order []string
	seen := make(map[*Session]bool, len(sessions))
	for _, s := range sessions {
		if s == nil || s.Name == "" || seen[s] {
			continue
		}
		seen[s] = true
		// A control-mode pipe captures without a subprocess already.
		if pm != nil && pm.IsConnected(s.Name) {
			continue
		}
		socket := strings.TrimSpace(s.SocketName)
		// A session the fresh default-socket inventory says is gone would
		// abort the sequence; leave it to its own (absent) handling.
		if socket == DefaultSocketName() {
			if exists, valid := sessionExistsFromCache(s.Name); valid && !exists {
				continue
			}
		}
		if _, ok := bySocket[socket]; !ok {
			order = append(order, socket)
		}
		bySocket[socket] = append(bySocket[socket], s)
	}
	total := 0
	for _, s := range bySocket {
		total += len(s)
	}
	if total < 2 {
		return 0
	}
	primed := 0
	for _, socket := range order {
		group := bySocket[socket]
		for len(group) > 0 {
			n := min(len(group), captureBatchMax)
			primed += primeCaptureChunk(socket, group[:n])
			group = group[n:]
		}
	}
	return primed
}

// primeCaptureChunk captures one chunk of sessions on one socket. When a
// pane disappears mid-sequence tmux stops there; the frames before it are
// kept, the failing pane is skipped and the rest is resumed, at most
// captureBatchRetries times.
func primeCaptureChunk(socket string, chunk []*Session) int {
	primed := 0
	for attempt := 0; len(chunk) > 0 && attempt <= captureBatchRetries; attempt++ {
		marker := captureBatchMarker()
		args := make([]string, 0, len(chunk)*9)
		for i, s := range chunk {
			if i > 0 {
				args = append(args, ";")
			}
			args = append(args, "capture-pane", "-t", s.Name, "-p", "-e", ";", "display-message", "-p", marker+strconv.Itoa(i))
		}
		timeout := min(tmuxPollTimeout+time.Duration(len(chunk))*50*time.Millisecond, 10*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		out, err := captureBatchRun(ctx, socket, args)
		timedOut := ctx.Err() == context.DeadlineExceeded
		cancel()
		frames := splitCaptureBatch(string(out), marker, len(chunk))
		now := time.Now()
		for i, frame := range frames {
			chunk[i].primeCapture(frame, now)
		}
		primed += len(frames)
		if err == nil || timedOut || len(frames) >= len(chunk) {
			return primed
		}
		// frames[len(frames)] is the pane that failed: skip it and resume.
		chunk = chunk[len(frames)+1:]
	}
	return primed
}

// splitCaptureBatch returns the frames of a batched capture in order, up to
// the first one whose closing marker is missing (that capture failed or the
// output was cut short).
func splitCaptureBatch(out, marker string, n int) []string {
	frames := make([]string, 0, n)
	pos := 0
	for i := 0; i < n; i++ {
		line := marker + strconv.Itoa(i) + "\n"
		idx := -1
		for search := pos; search <= len(out); {
			j := strings.Index(out[search:], line)
			if j < 0 {
				break
			}
			j += search
			if j == pos || out[j-1] == '\n' {
				idx = j
				break
			}
			search = j + 1
		}
		if idx < 0 {
			break
		}
		frames = append(frames, out[pos:idx])
		pos = idx + len(line)
	}
	return frames
}

// captureBatchMarker is a per-call random delimiter no pane content will
// contain by accident.
func captureBatchMarker() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "agentdeck-capture-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-"
	}
	return "agentdeck-capture-" + hex.EncodeToString(b[:]) + "-"
}

// primeCapture stores a batched frame as this session's CapturePane cache.
func (s *Session) primeCapture(content string, now time.Time) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheContent = content
	s.cacheTime = now
	s.cachePrimedUntil = now.Add(capturePrimeTTL)
}

// captureCacheFreshLocked reports whether the CapturePane cache may answer
// now: within the 500ms window, or within a batched frame's prime window.
// Caller holds cacheMu.
func (s *Session) captureCacheFreshLocked(now time.Time) bool {
	if s.cacheContent == "" {
		return false
	}
	return now.Sub(s.cacheTime) < 500*time.Millisecond || now.Before(s.cachePrimedUntil)
}
