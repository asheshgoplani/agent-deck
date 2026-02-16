package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// handleWeb starts the web UI server entrypoint.
// WP-00 scope: command wiring + flags + minimal blocking server lifecycle.
func handleWeb(profile string, args []string) {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	listenAddr := fs.String("listen", "127.0.0.1:8420", "Listen address for web server")
	readOnly := fs.Bool("read-only", false, "Run in read-only mode (input disabled)")
	token := fs.String("token", "", "Bearer token for API/WS access")
	openBrowser := fs.Bool("open", false, "Open browser automatically (placeholder)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck web [options]")
		fmt.Println()
		fmt.Println("Start the web UI server for Agent Deck.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck web")
		fmt.Println("  agent-deck -p work web --listen 127.0.0.1:9000")
		fmt.Println("  agent-deck web --read-only")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected arguments: %v\n", fs.Args())
		fs.Usage()
		os.Exit(1)
	}

	mode := "read-write"
	if *readOnly {
		mode = "read-only"
	}

	effectiveProfile := session.GetEffectiveProfile(profile)
	fmt.Printf("Starting Agent Deck web mode on http://%s\n", *listenAddr)
	fmt.Printf("Profile: %s\n", effectiveProfile)
	fmt.Printf("Mode: %s\n", mode)
	if *token != "" {
		fmt.Println("Auth: bearer token enabled")
		fmt.Println("Auth hint: open the UI with ?token=<your-token> or pass Authorization: Bearer <token> on API requests")
	}
	if *openBrowser {
		fmt.Println("Note: --open is not implemented yet")
	}
	fmt.Println("Press Ctrl+C to stop.")

	server := web.NewServer(web.Config{
		ListenAddr: *listenAddr,
		Profile:    effectiveProfile,
		ReadOnly:   *readOnly,
		Token:      *token,
	})

	errCh := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "Error: web server failed: %v\n", err)
		os.Exit(1)
	case <-sigCh:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: web server shutdown error: %v\n", err)
	}
}
