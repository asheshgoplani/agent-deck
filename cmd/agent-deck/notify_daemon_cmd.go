package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleNotifyDaemon runs the always-on transition notifier daemon.
func handleNotifyDaemon(args []string) {
	fs := flag.NewFlagSet("notify-daemon", flag.ExitOnError)
	once := fs.Bool("once", false, "Run one sync pass and exit")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck notify-daemon [--once]")
		fmt.Println()
		fmt.Println("Run status-driven transition notification daemon.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	daemon := session.NewTransitionDaemon()
	if *once {
		daemon.SyncOnce(context.Background())
		// Ensure async dispatches started during SyncOnce land on disk
		// before the process exits; otherwise logs/queue state written by
		// the watcher/sender goroutines would race with the CLI shutdown
		// and leave the operator staring at an empty log.
		daemon.Flush()
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := daemon.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "notify-daemon error: %v\n", err)
		os.Exit(1)
	}
}
