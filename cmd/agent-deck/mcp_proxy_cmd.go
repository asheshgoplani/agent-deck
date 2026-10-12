package main

import (
	"context"
	"io"
	"net"
	"os"
	"time"
)

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
	parentPID := os.Getppid()
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

		mcpProxyConnection(ctx, conn, input)

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

func mcpProxyConnection(ctx context.Context, conn net.Conn, input <-chan []byte) {
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case chunk := <-input:
			for len(chunk) > 0 {
				n, err := conn.Write(chunk)
				// Never replay a partially written request on another connection.
				if err != nil || n == 0 {
					return
				}
				chunk = chunk[n:]
			}
		}
	}
}
