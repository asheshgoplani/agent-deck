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
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printFeedbackHelp(w)
			return nil
		}
	}

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

		// Persist the rating BEFORE the disclosure prompt so a user who
		// declines to post is not re-prompted for the same version on
		// the next invocation (issue #679 — saving must not depend on
		// whether the user consents to public posting).
		st, _ := feedback.LoadState()
		feedback.RecordRating(st, version, rating)
		if saveErr := feedback.SaveState(st); saveErr != nil {
			fmt.Fprintf(os.Stderr, "feedback: save state: %v\n", saveErr)
		}

		fmt.Fprint(w, "Comment (optional, press Enter to skip): ")
		commentLine, commentErr := reader.ReadString('\n')
		if commentErr != nil && commentErr != io.EOF {
			commentLine = ""
		}
		comment := strings.TrimSpace(commentLine)

		// Build the EXACT body that will be posted. The preview below
		// displays this same variable verbatim, and the gh mutation
		// uses it unchanged — there is no "prettier preview" that
		// could drift from what actually hits GitHub.
		body := feedback.FormatComment(version, rating, runtime.GOOS, runtime.GOARCH, comment)

		renderFeedbackDisclosure(w, body, ghUserLogin())

		fmt.Fprint(w, "Post this? [y/N]: ")
		confirmLine, _ := reader.ReadString('\n')
		if !isYesConfirmation(confirmLine) {
			fmt.Fprintln(w, "Not posted.")
			return nil
		}

		// Confirmed — post directly via gh. Bypasses sender.Send() so
		// the clipboard/browser fallback path can NEVER fire from the
		// CLI (issue #679: no silent side-effects after 'y').
		const ghQuery = `mutation($id:ID!,$body:String!){addDiscussionComment(input:{discussionId:$id,body:$body}){comment{id}}}`
		ghErr := sender.GhCmd(
			"api", "graphql",
			"-f", "query="+ghQuery,
			"-f", "id="+feedback.DiscussionNodeID,
			"-f", "body="+body,
		)
		if ghErr != nil {
			fmt.Fprintln(w, "Error: could not post via gh. Feedback was NOT sent.")
			fmt.Fprintln(w, "Make sure `gh auth status` shows you are logged in.")
			return fmt.Errorf("feedback: gh post failed: %w", ghErr)
		}
		fmt.Fprintln(w, "Posted to Discussion #600.")
		return nil

	default:
		fmt.Fprintln(os.Stderr, "Invalid input. Enter 1-5, n, or q.")
		os.Exit(1)
		return nil // unreachable
	}
}

// renderFeedbackDisclosure prints the #679 disclosure block: where the
// comment will appear, how it is posted, which GitHub account it will
// carry, and the exact body that will be posted (indented four spaces
// so it stands apart from prose). login is the authenticated GitHub
// login from gh; when empty, the "As:" line falls back to a generic
// string with no @ prefix.
func renderFeedbackDisclosure(w io.Writer, body, login string) {
	asLine := "  As:     your GitHub account   (visible to anyone viewing the discussion)"
	if login != "" {
		asLine = fmt.Sprintf("  As:     @%s   (your own GitHub account — visible to anyone viewing the discussion)", login)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "This feedback will be posted PUBLICLY on GitHub.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Where:  https://github.com/asheshgoplani/agent-deck/discussions/600")
	fmt.Fprintln(w, "  How:    via the `gh` GitHub CLI (already installed and authenticated on this machine)")
	fmt.Fprintln(w, asLine)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Anyone browsing that discussion page will see your GitHub username")
	fmt.Fprintln(w, "next to the post. If you would rather keep this private, answer N.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Exact content that will be posted (no more, no less):")
	fmt.Fprintln(w, "────────────────────────────────────────────────────────")
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintln(w, "    "+line)
	}
	fmt.Fprintln(w, "────────────────────────────────────────────────────────")
	fmt.Fprintln(w)
}

// isYesConfirmation returns true only when the trimmed, lower-cased
// line is exactly "y" or "yes". Anything else — including empty input
// — is treated as a decline (default-N).
func isYesConfirmation(line string) bool {
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}

// printFeedbackHelp documents the `agent-deck feedback` flow, with
// the posting-is-public / default-N / no-silent-fallback guarantees
// explicit (issue #679).
func printFeedbackHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck feedback")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Rate agent-deck and optionally leave a comment.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "How it works:")
	fmt.Fprintln(w, "  1. You are asked for a rating (1-5, n to never ask again, q to quit).")
	fmt.Fprintln(w, "  2. On a valid rating you may add a short comment.")
	fmt.Fprintln(w, "  3. BEFORE anything is sent, the CLI shows a disclosure block with:")
	fmt.Fprintln(w, "       - the public URL (https://github.com/asheshgoplani/agent-deck/discussions/600),")
	fmt.Fprintln(w, "       - that it posts via the `gh` CLI under your GitHub account,")
	fmt.Fprintln(w, "       - your GitHub username (as seen by `gh api user -q .login`),")
	fmt.Fprintln(w, "       - the exact body that will be posted.")
	fmt.Fprintln(w, "  4. You confirm with `y`. Default is N — pressing Enter declines.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Failure mode:")
	fmt.Fprintln(w, "  If `gh` fails (not installed, not authenticated, network error), the")
	fmt.Fprintln(w, "  CLI prints an error and exits non-zero. There is NO silent clipboard")
	fmt.Fprintln(w, "  or browser fallback on this path — nothing is sent without consent.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A private/anonymous feedback channel is being designed for a future")
	fmt.Fprintln(w, "release — track in https://github.com/asheshgoplani/agent-deck/issues/679.")
}
