package main

import (
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
		maxRetries        = 120 // ~60 seconds worst case
		reconnectPause    = 100 * time.Millisecond
	)

	retryDelay := initialRetryDelay
	retries := 0

	for {
		// #nosec G704 -- "unix" socket dial, not a network address; socketPath
		// is the agent-deck-managed MCP proxy socket path resolved from local config.
		conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
		if err != nil {
			retries++
			if retries >= maxRetries {
				os.Exit(1)
			}
			time.Sleep(retryDelay)
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

		// Bidirectional copy: stdin <-> socket
		done := make(chan struct{}, 2)

		go func() {
			_, _ = io.Copy(conn, os.Stdin) // Claude -> Socket
			done <- struct{}{}
		}()

		go func() {
			_, _ = io.Copy(os.Stdout, conn) // Socket -> Claude
			done <- struct{}{}
		}()

		<-done // Wait for either direction to fail
		conn.Close()

		// Brief pause before reconnecting
		time.Sleep(reconnectPause)
	}
}
