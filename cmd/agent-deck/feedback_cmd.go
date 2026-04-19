package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
)

// ghUserLogin returns the authenticated GitHub account login (e.g.
// "octocat") as seen by the local gh CLI. Used by the feedback flow
// (issue #679) to tell the user which account will carry the post
// before they confirm. Empty string when gh is unauthenticated or
// unavailable — callers render a generic fallback in that case.
//
// Overridable for tests.
var ghUserLogin = func() string {
	out, err := exec.Command("gh", "api", "user", "-q", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// handleFeedback is the public dispatch entry point for the "agent-deck feedback" subcommand.
// It delegates to handleFeedbackWithSender with the real stdin and a real Sender.
func handleFeedback(args []string) {
	var stdout strings.Builder
	if err := handleFeedbackWithSender(args, Version, feedback.NewSender(), &stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Print buffered output to real stdout
	fmt.Print(stdout.String())
}

// handleFeedbackWithSender is the testable core: it reads a rating and optional comment
// from os.Stdin, records the state, and calls sender.Send().
// The sender parameter is injected so tests can provide a mock.
// Output is written to w (use &strings.Builder for tests, os.Stdout for production).
func handleFeedbackWithSender(args []string, version string, sender *feedback.Sender, w io.Writer) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprint(w, "Rating (1-5, n=never-again, q=quit): ")

	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("feedback: read rating: %w", err)
	}
	input := strings.TrimSpace(line)

	switch input {
	case "q":
		fmt.Fprintln(w, "Cancelled.")
		return nil

	case "n":
		st, _ := feedback.LoadState()
		feedback.RecordOptOut(st)
		if saveErr := feedback.SaveState(st); saveErr != nil {
			// Non-fatal: log to stderr but don't abort
			fmt.Fprintf(os.Stderr, "feedback: save state: %v\n", saveErr)
		}
		fmt.Fprintln(w, "Feedback disabled. You can always re-open via 'agent-deck feedback'.")
		return nil

	case "1", "2", "3", "4", "5":
		rating := int(input[0] - '0')

		// Load and update state
		st, _ := feedback.LoadState()
		feedback.RecordRating(st, version, rating)
		if saveErr := feedback.SaveState(st); saveErr != nil {
			fmt.Fprintf(os.Stderr, "feedback: save state: %v\n", saveErr)
		}

		// Prompt for optional comment
		fmt.Fprint(w, "Comment (optional, press Enter to skip): ")
		commentLine, commentErr := reader.ReadString('\n')
		if commentErr != nil && commentErr != io.EOF {
			// Treat as empty comment on read error
			commentLine = ""
		}
		comment := strings.TrimSpace(commentLine)

		// Send via three-tier fallback (always returns nil)
		_ = sender.Send(version, rating, runtime.GOOS, runtime.GOARCH, comment)
		fmt.Fprintln(w, "Sent! Thanks for the feedback.")
		return nil

	default:
		fmt.Fprintln(os.Stderr, "Invalid input. Enter 1-5, n, or q.")
		os.Exit(1)
		return nil // unreachable
	}
}
