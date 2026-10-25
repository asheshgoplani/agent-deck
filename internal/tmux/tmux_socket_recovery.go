package tmux

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

func shouldRecoverFromTmuxStartError(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "server exited unexpectedly") ||
		strings.Contains(lower, "lost server")
}

func recoverFromStaleDefaultSocketIfNeeded(startErrOutput string) (bool, error) {
	if !shouldRecoverFromTmuxStartError(startErrOutput) {
		return false, nil
	}

	// If tmux can already answer list-sessions, don't touch any socket file.
	// Bounded — see tmuxPollTimeout. This is the recovery path for a server
	// that already misbehaved, so it is exactly where a client is most likely
	// to hang; an unbounded probe here would block recovery indefinitely.
	if err := runBoundedRun(DefaultSocketName(), "list-sessions"); err == nil {
		return false, nil
	}

	for _, socketPath := range defaultTmuxSocketCandidates() {
		info, err := os.Stat(socketPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		if isSocketAcceptingConnections(socketPath) {
			continue
		}

		backupPath := fmt.Sprintf("%s.stale.%d", socketPath, time.Now().UnixNano())
		if err := os.Rename(socketPath, backupPath); err != nil {
			return false, fmt.Errorf("failed to quarantine stale tmux socket %s: %w", socketPath, err)
		}

		statusLog.Warn("tmux_stale_socket_recovered",
			slog.String("socket", socketPath),
			slog.String("backup", backupPath),
		)
		return true, nil
	}

	return false, nil
}

func defaultTmuxSocketCandidates() []string {
	uid := os.Getuid()
	if uid < 0 {
		return nil
	}

	add := func(out []string, seen map[string]struct{}, p string) []string {
		if p == "" {
			return out
		}
		if _, ok := seen[p]; ok {
			return out
		}
		seen[p] = struct{}{}
		return append(out, p)
	}

	seen := make(map[string]struct{})
	candidates := make([]string, 0, 5)
	if tmuxPath := tmuxSocketPathFromEnv(); tmuxPath != "" {
		candidates = add(candidates, seen, tmuxPath)
	}

	socketSuffix := filepath.Join(fmt.Sprintf("tmux-%d", uid), "default")
	if tmuxTmp := os.Getenv("TMUX_TMPDIR"); tmuxTmp != "" {
		candidates = add(candidates, seen, filepath.Join(tmuxTmp, socketSuffix))
	}
	candidates = add(candidates, seen, filepath.Join(os.TempDir(), socketSuffix))
	candidates = add(candidates, seen, filepath.Join("/tmp", socketSuffix))
	candidates = add(candidates, seen, filepath.Join("/private/tmp", socketSuffix))
	return candidates
}

func tmuxSocketPathFromEnv() string {
	raw := os.Getenv("TMUX")
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func isSocketAcceptingConnections(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// hasSessionProbeTimeout bounds a `tmux has-session` existence probe. A tmux
// server that is briefly busy (e.g. tearing down another session) can make the
// probe hang; rather than block a status poll — or, worse, mistake the stall
// for a dead session — we cap it and treat a timeout as indeterminate (assume
// the session still exists and let a later poll resolve it). Overridable in
// tests.
var hasSessionProbeTimeout = 2 * time.Second

// isTmuxProtocolMismatch reports whether tmux output signals a client/server
// protocol version mismatch. tmux prints "protocol version mismatch (client X,
// server Y)" when a client binary of one protocol version talks to a server
// started by a binary of a different version — common when the running server
// uses a newer tmux (e.g. one installed under a Nix profile) while the client's
// PATH resolves an older system tmux. Crucially, a mismatch response is proof
// that the SERVER is alive and reachable: it answered with its version. The
// failure is purely in the client binary, not in the session. Callers must
// therefore treat a mismatch as indeterminate (assume alive), never as an
// authoritative "session gone".
//
// An authoritative absence reply always wins over the mismatch marker. tmux
// echoes the requested target in "can't find session: <name>", so a session
// NAMED like the marker (e.g. "protocol version mismatch") would otherwise make
// the bare substring test below fire on a genuine "gone" reply and wrongly keep
// a dead session alive. A real mismatch means the client never reached the
// server, so it can't also carry an authoritative absence — the two are
// mutually exclusive, and giving absence precedence is safe.
//
// defaultProbeSocketProtocolMismatch, the only production caller, additionally
// keeps operator-controlled session names out of the bytes handed here (it
// classifies `list-sessions -F ""` stderr, which echoes no target). The
// short-circuit above is therefore belt-and-braces for that path — and load-
// bearing for any caller that ever classifies has-session output again.
func isTmuxProtocolMismatch(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "can't find session") {
		return false
	}
	return strings.Contains(lower, "protocol version mismatch")
}

// socketMismatchCacheTTL bounds how long one socket's protocol-mismatch verdict
// is reused. A mismatch is a property of the client/server BINARY PAIR — the
// tmux on PATH versus the tmux that started the server on this socket — not of
// an individual session, so it is stable for the lifetime of that pair.
//
// Keep the window short anyway. The direction that costs the user is a mismatch
// appearing while a "no mismatch" verdict is still warm: sessions on that socket
// keep reading gone for up to one TTL, exactly as they do today. Recovery in the
// other direction (the operator fixes PATH, or the server restarts) is benign —
// sessions merely stay "assume alive" for one more TTL. 5s matches
// socketSessionCacheTTL and the negative-cache reasoning in #1728.
const socketMismatchCacheTTL = 5 * time.Second

// socketMismatchVerdict is one socket's cached classification.
type socketMismatchVerdict struct {
	mismatched bool
	checkedAt  time.Time
}

var (
	socketMismatchMu    sync.Mutex
	socketMismatchCache = map[string]socketMismatchVerdict{}

	// socketMismatchSf collapses a burst of classifications for the same socket
	// into one, the way captureSf does for CapturePane: every session on a
	// refusing socket reads "gone" in the same status pass, and that burst must
	// cost ONE classification, not one per session.
	socketMismatchSf singleflight.Group

	// probeSocketProtocolMismatch is a package var so tests can drive the cache
	// without a live tmux server. Read/written under socketMismatchMu.
	probeSocketProtocolMismatch = defaultProbeSocketProtocolMismatch
)

// socketHasProtocolMismatch reports whether the tmux client agent-deck spawns
// can talk to the server on socketName at all, or is refused for speaking a
// different protocol version.
//
// This is the out-of-band half of the mismatch fix. The liveness probes
// (Session.Exists, tmuxSessionExistsOnSocket) stay on cmd.Run() with no stdio to
// capture, so nothing on the status hot path can block waiting for an EOF that a
// forked child of tmux holds open (see tmuxSubprocessWaitDelay in socket.go).
// The one exchange that has to READ tmux's error text to classify it happens
// here instead: at most once per socket per TTL, and only after a probe has
// already come back "gone".
func socketHasProtocolMismatch(socketName string) bool {
	socket := strings.TrimSpace(socketName)

	if verdict, fresh := cachedSocketMismatch(socket); fresh {
		return verdict
	}

	// Slow path: classify, deduplicating concurrent callers for this socket.
	result, _, _ := socketMismatchSf.Do(socket, func() (any, error) {
		// Double-check inside the singleflight: a caller that queued here just
		// before the winner stored its verdict must reuse it, not re-classify.
		if verdict, fresh := cachedSocketMismatch(socket); fresh {
			return verdict, nil
		}

		socketMismatchMu.Lock()
		probe := probeSocketProtocolMismatch
		socketMismatchMu.Unlock()

		mismatched := probe(socket)

		socketMismatchMu.Lock()
		socketMismatchCache[socket] = socketMismatchVerdict{mismatched: mismatched, checkedAt: time.Now()}
		socketMismatchMu.Unlock()

		return mismatched, nil
	})

	mismatched, _ := result.(bool)
	return mismatched
}

// cachedSocketMismatch returns a socket's verdict and whether it is still fresh.
func cachedSocketMismatch(socket string) (verdict, fresh bool) {
	socketMismatchMu.Lock()
	defer socketMismatchMu.Unlock()

	entry, ok := socketMismatchCache[socket]
	if !ok || time.Since(entry.checkedAt) >= socketMismatchCacheTTL {
		return false, false
	}
	return entry.mismatched, true
}

// defaultProbeSocketProtocolMismatch settles one client/server exchange on
// socketName. Every client command against a server of a different protocol
// version is refused with tmux's "protocol version mismatch (client X, server
// Y)" on stderr, so a single `list-sessions` decides it.
//
// Three deliberate choices:
//
//   - stderr is captured into a REAL FILE, never a pipe. os/exec hands an
//     *os.File to the child as its fd 2 unchanged — no os.Pipe, no copy
//     goroutine — so nothing here can wait on an EOF that a forked child of tmux
//     is holding open (socket.go's tmuxSubprocessWaitDelay). Moving the capture
//     off the liveness probes is what keeps that wait off the status hot path;
//     using a pipe HERE would put the same bounded-but-real 2s drain back, once
//     per socket per TTL, and `agent-deck status` under bridged stdio measurably
//     pays it.
//   - Only tmux's own stderr is classified, and `-F ""` makes tmux print an
//     empty line per session instead of session names. Session names are
//     operator-controlled text, so keeping them out of the classified bytes is
//     what makes this probe structurally immune to the echo problem
//     isTmuxProtocolMismatch has to guard against in has-session output.
//   - Only a FAILED exchange can be a mismatch: a server that answers is by
//     definition speaking our protocol. Anything else that fails (deadline,
//     exec failure, an unwritable temp dir) reads as "no mismatch" — the
//     conservative answer, since it leaves the caller's own verdict untouched.
func defaultProbeSocketProtocolMismatch(socketName string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), hasSessionProbeTimeout)
	defer cancel()

	sink, err := os.CreateTemp("", "agent-deck-tmux-mismatch-*")
	if err != nil {
		return false
	}
	defer func() {
		_ = sink.Close()
		_ = os.Remove(sink.Name())
	}()

	cmd := tmuxExecContext(ctx, socketName, "list-sessions", "-F", "")
	cmd.Stderr = sink // *os.File: passed through to the child, not proxied
	if err := commandRun(cmd); err == nil {
		return false
	}

	if _, err := sink.Seek(0, io.SeekStart); err != nil {
		return false
	}
	// tmux's refusal is one short line; cap the read so a pathological server
	// cannot make this allocate.
	stderr, err := io.ReadAll(io.LimitReader(sink, 4096))
	if err != nil {
		return false
	}
	return isTmuxProtocolMismatch(string(stderr))
}

// resetSocketMismatchCacheForTest clears every cached verdict.
func resetSocketMismatchCacheForTest() {
	socketMismatchMu.Lock()
	defer socketMismatchMu.Unlock()
	socketMismatchCache = map[string]socketMismatchVerdict{}
}

// setSocketMismatchProbeForTest swaps the classifier and returns a restore func.
func setSocketMismatchProbeForTest(probe func(string) bool) func() {
	socketMismatchMu.Lock()
	defer socketMismatchMu.Unlock()

	prev := probeSocketProtocolMismatch
	probeSocketProtocolMismatch = probe
	return func() {
		socketMismatchMu.Lock()
		defer socketMismatchMu.Unlock()
		probeSocketProtocolMismatch = prev
	}
}
