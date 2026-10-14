package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

// mcpProxyParentPIDEnv lets a launcher pass its own PID. Getppid alone cannot
// tell whether the launcher exited before the proxy started: by then the proxy
// is already adopted by PID 1 or a subreaper, which is indistinguishable from a
// legitimate PID-1 host (a container entrypoint). When set, a mismatch at
// startup means the launcher is gone and the proxy exits immediately.
const mcpProxyParentPIDEnv = "AGENT_DECK_MCP_PROXY_PARENT_PID"

// mcpProxyExpectedParent returns the parent PID to watch and whether the
// expected launcher has already exited.
func mcpProxyExpectedParent(current int, env string) (int, bool) {
	if expected, err := strconv.Atoi(env); err == nil && expected > 0 {
		return expected, expected != current
	}
	// Without a launcher contract only later parent changes are observable.
	return current, false
}

// runMCPProxy is a bidirectional proxy between stdin/stdout and a Unix socket.
// Unlike nc, it automatically reconnects when the socket drops.
// Used internally when generating .mcp.json for Claude sessions.
func runMCPProxy(socketPath string) {
	const (
		initialRetryDelay = 100 * time.Millisecond
		maxRetryDelay     = 5 * time.Second
		dialTimeout       = 2 * time.Second
		maxRetries        = 120
		reconnectPause    = 100 * time.Millisecond
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parentPID, parentGone := mcpProxyExpectedParent(os.Getppid(), os.Getenv(mcpProxyParentPIDEnv))
	if parentGone {
		return
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A full input queue must not hide EOF behind unread pipe data.
				if os.Getppid() != parentPID || mcpProxyStdinClosed() {
					cancel()
					return
				}
			}
		}
	}()

	// One reader survives socket generations, so an old connection cannot
	// consume the next request. Bound buffering while the backend is offline.
	input := make(chan []byte, 1)
	go func() {
		for {
			buffer := make([]byte, 32*1024)
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				select {
				case input <- buffer[:n]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	retryDelay := initialRetryDelay
	retries := 0
	dialer := net.Dialer{Timeout: dialTimeout}
	forwarder := &mcpProxyForwarder{}

	for ctx.Err() == nil {
		// #nosec G704 -- "unix" socket dial, not a network address; socketPath
		// is the agent-deck-managed MCP proxy socket path resolved from local config.
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			retries++
			if retries >= maxRetries {
				exitCLI(1)
			}
			if !mcpProxyPause(ctx, retryDelay) {
				return
			}
			if retryDelay < maxRetryDelay {
				retryDelay *= 2
				if retryDelay > maxRetryDelay {
					retryDelay = maxRetryDelay
				}
			}
			continue
		}

		// Connected, reset backoff
		retryDelay = initialRetryDelay
		retries = 0

		mcpProxyConnection(ctx, conn, input, forwarder)

		// Brief pause before reconnecting
		if !mcpProxyPause(ctx, reconnectPause) {
			return
		}
	}
}

func mcpProxyPause(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// mcpProxyForwarder writes newline-delimited requests across socket
// generations. Bytes that never reached a socket are carried to the next
// connection only from a request boundary; the remainder of a request whose
// prefix was already delivered is dropped through its newline, because the
// backend cannot reassemble a request split across connections.
type mcpProxyForwarder struct {
	pending    []byte // unwritten bytes to send on the next connection
	midRequest bool   // current connection received a request prefix
	discarding bool   // drop input through the next newline
}

// reset prepares state for a new connection.
func (f *mcpProxyForwarder) reset() {
	if f.midRequest {
		f.discarding = true
	}
	f.midRequest = false
}

// send writes chunk (after any carried bytes) and reports whether the
// connection is still usable.
func (f *mcpProxyForwarder) send(w io.Writer, chunk []byte) bool {
	data := chunk
	if len(f.pending) > 0 {
		data = append(f.pending, chunk...)
		f.pending = nil
	}
	if f.discarding {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return true
		}
		f.discarding = false
		data = data[i+1:]
	}
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			f.midRequest = data[n-1] != '\n'
			data = data[n:]
		}
		if err != nil || n == 0 {
			if len(data) > 0 {
				f.pending = append([]byte(nil), data...)
			}
			return false
		}
	}
	return true
}

func mcpProxyConnection(ctx context.Context, conn net.Conn, input <-chan []byte, f *mcpProxyForwarder) {
	f.reset()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, conn)
		_ = conn.Close()
		close(done)
	}()
	defer func() {
		stop()
		_ = conn.Close()
		// Drain this generation before reconnecting. Terminal client shutdown
		// must still return when stdout is blocked; main then exits the process.
		select {
		case <-done:
		case <-ctx.Done():
		}
	}()
	if len(f.pending) > 0 && !f.send(conn, nil) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case chunk := <-input:
			// A chunk dequeued as the connection closes is kept, not lost.
			if !f.send(conn, chunk) {
				return
			}
		}
	}
}
