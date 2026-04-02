package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/ui"
)

// handleTabStrip launches the standalone tab strip Bubble Tea app.
func handleTabStrip(args []string) {
	fs := flag.NewFlagSet("tab-strip", flag.ExitOnError)

	homeDir, _ := os.UserHomeDir()
	defaultDB := filepath.Join(homeDir, ".agent-deck", "state.db")

	current := fs.String("current", "", "Initially selected session ID")
	dbPath := fs.String("db", defaultDB, "Path to SQLite database")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck tab-strip [--current=SESSION_ID] [--db=PATH]")
		fmt.Println()
		fmt.Println("Launch a standalone tab strip for use in a tmux split pane.")
		fmt.Println("Reads session data from SQLite and renders an animated vertical list.")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	app, err := ui.NewTabStripApp(*dbPath, *current)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tab-strip: %v\n", err)
		os.Exit(1)
	}
	defer app.Close()

	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tab-strip: %v\n", err)
		os.Exit(1)
	}
}
