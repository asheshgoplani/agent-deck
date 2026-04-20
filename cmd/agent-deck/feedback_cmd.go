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
	"github.com/asheshgoplani/agent-deck/internal/session"
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
// It delegates to handleFeedbackWithSender wired to real stdin/stdout so prompts print
// interactively as they are emitted (see #679 follow-up: a previous strings.Builder wrapper
// hid every prompt until after the function returned, leaving users typing at a blank cursor).
func handleFeedback(args []string) {
	if err := handleFeedbackWithSender(args, Version, feedback.NewSender(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// handleFeedbackWithSender is the testable core: it reads a rating and optional comment
// from in, records the state, and calls sender.Send(). Prompts are written to w as they
// are emitted (no buffering — see #679 follow-up).
// The sender parameter is injected so tests can provide a mock.
func handleFeedbackWithSender(args []string, version string, sender *feedback.Sender, in io.Reader, w io.Writer) error {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printFeedbackHelp(w)
			return nil
		}
	}

	reader := bufio.NewReader(in)

	// v1.7.38: if the user previously opted out (via state.json or an
	// edited config.toml [feedback].disabled=true), ask whether to
	// re-enable before showing the rating prompt. Default-N. This keeps
	// explicit `agent-deck feedback` usable while respecting the opt-out
	// for every other code path.
	st, _ := feedback.LoadState()
	cfg, _ := session.LoadUserConfig()
	if isFeedbackOptedOut(st, cfg) {
		fmt.Fprintln(w, "Feedback is currently disabled (opt-out saved from a previous run).")
		fmt.Fprint(w, "Enable feedback and continue? [y/N]: ")
		answer, _ := reader.ReadString('\n')
		if !isYesConfirmation(answer) {
			fmt.Fprintln(w, "Feedback stays disabled. Run 'agent-deck feedback' again any time.")
			return nil
		}
		// Re-enable in BOTH stores so state.json and config.toml stay in sync.
		if st != nil {
			st.FeedbackEnabled = true
			if saveErr := feedback.SaveState(st); saveErr != nil {
				fmt.Fprintf(os.Stderr, "feedback: save state: %v\n", saveErr)
			}
		}
		if cfg != nil && cfg.Feedback.Disabled {
			cfg.Feedback.Disabled = false
			if saveErr := session.SaveUserConfig(cfg); saveErr != nil {
				fmt.Fprintf(os.Stderr, "feedback: save config: %v\n", saveErr)
			}
		}
		fmt.Fprintln(w, "Feedback re-enabled.")
	}

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
		persistFeedbackOptOut(w, "Feedback disabled. Run 'agent-deck feedback' any time to re-enable.")
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
			// v1.7.38: declining at the disclosure step is a persistent
			// opt-out, not a one-shot "not posted". Previously the user
			// would re-see this prompt on every launch.
			persistFeedbackOptOut(w, "Not posted. Feedback prompts disabled — run 'agent-deck feedback' any time to re-enable.")
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

// isFeedbackOptedOut returns true when either the feedback-state.json
// has FeedbackEnabled=false or the user's config.toml has
// [feedback].disabled=true. Either one opts out (v1.7.38).
func isFeedbackOptedOut(st *feedback.State, cfg *session.UserConfig) bool {
	if st != nil && !st.FeedbackEnabled {
		return true
	}
	if cfg != nil && cfg.Feedback.Disabled {
		return true
	}
	return false
}

// persistFeedbackOptOut writes the opt-out to BOTH stores and prints the
// given message. Errors on either write are non-fatal — they log to stderr
// but do not abort the CLI flow. v1.7.38.
func persistFeedbackOptOut(w io.Writer, userMessage string) {
	st, _ := feedback.LoadState()
	if st == nil {
		st = &feedback.State{MaxShows: 3}
	}
	feedback.RecordOptOut(st)
	if saveErr := feedback.SaveState(st); saveErr != nil {
		fmt.Fprintf(os.Stderr, "feedback: save state: %v\n", saveErr)
	}

	cfg, _ := session.LoadUserConfig()
	if cfg != nil && !cfg.Feedback.Disabled {
		cfg.Feedback.Disabled = true
		if saveErr := session.SaveUserConfig(cfg); saveErr != nil {
			fmt.Fprintf(os.Stderr, "feedback: save config: %v\n", saveErr)
		}
	}

	fmt.Fprintln(w, userMessage)
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
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Prompt frequency (v1.7.41+):")
	fmt.Fprintln(w, "  The TUI auto-prompt appears after 7 launches or 3 days of use,")
	fmt.Fprintln(w, "  whichever comes later. If you dismiss it, we wait 14 days before")
	fmt.Fprintln(w, "  asking again. Maximum 3 prompts per version. Pressing `n` at any")
	fmt.Fprintln(w, "  step is a permanent opt-out. `agent-deck feedback` always works")
	fmt.Fprintln(w, "  on demand regardless of pacing.")
}
