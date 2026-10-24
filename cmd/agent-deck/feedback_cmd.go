package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/feedback"
	"github.com/asheshgoplani/agent-deck/internal/issuereport"
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
	// `agent-deck feedback bug|idea` files a structured GitHub issue
	// (ASK-109); plain `agent-deck feedback` keeps the rating flow below.
	if len(args) > 0 && (args[0] == string(issuereport.KindBug) || args[0] == string(issuereport.KindIdea)) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		code := runFeedbackReport(ctx, issuereport.Kind(args[0]), args[1:], Version, os.Stdin, os.Stdout)
		stop()
		if code != 0 {
			exitCLI(code)
		}
		return
	}
	if err := handleFeedbackWithSender(args, Version, feedback.NewSender(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitCLI(1)
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
		exitCLI(1)
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
//
// The opt-out is scoped to the running release series via the package-level
// Version, so a future release-series bump can re-show the prompt (#967).
func persistFeedbackOptOut(w io.Writer, userMessage string) {
	st, _ := feedback.LoadState()
	if st == nil {
		st = &feedback.State{MaxShows: 3}
	}
	feedback.RecordOptOut(st, Version)
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
	fmt.Fprintln(w, "       agent-deck feedback bug|idea [flags]   (report a problem; see 'agent-deck feedback bug --help')")
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

// ── agent-deck feedback bug|idea (ASK-109) ─────────────────────────────
//
// Builds a structured GitHub issue with internal/issuereport, shows the
// FULL text, and files it only after an explicit yes: "y" at the prompt,
// or --yes (which the Mac app passes only after its user saw the text
// returned by --json --dry-run and clicked Send).

// feedbackReportLogLines is how many debug.log lines a bug report carries.
const feedbackReportLogLines = 40

// Seams for tests; production uses the issuereport functions.
var (
	feedbackCollectDiagnostics = issuereport.CollectDiagnostics
	feedbackGhReady            = issuereport.GhReady
	feedbackFileIssue          = issuereport.File
	feedbackBrowserAvailable   = issuereport.BrowserAvailable
)

// feedbackReportJSON is the --json output the Mac app parses.
type feedbackReportJSON struct {
	OK         bool     `json:"ok"`
	DryRun     bool     `json:"dry_run,omitempty"`
	Method     string   `json:"method,omitempty"`
	Title      string   `json:"title,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Body       string   `json:"body,omitempty"`
	URL        string   `json:"url,omitempty"`
	URLTrimmed *bool    `json:"url_trimmed,omitempty"`
	GhReady    *bool    `json:"gh_ready,omitempty"`
	ErrorCode  string   `json:"error_code,omitempty"`
	Message    string   `json:"message,omitempty"`
}

const errCodeConfirmRequired = "confirm_required"

func writeFeedbackJSON(w io.Writer, v feedbackReportJSON) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // keep <token>/<host> placeholders and & in URLs readable
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// runFeedbackReport implements `agent-deck feedback bug|idea` and returns
// the process exit code.
func runFeedbackReport(ctx context.Context, kind issuereport.Kind, args []string, version string, in io.Reader, w io.Writer) int {
	fs := flag.NewFlagSet("feedback "+string(kind), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	title := fs.String("title", "", "one-line title")
	what := fs.String("what", "", "what happened (idea: the problem it would solve)")
	expected := fs.String("expected", "", "what you expected (idea: the proposed behavior)")
	steps := fs.String("steps", "", "steps to reproduce (idea: alternatives considered)")
	tool := fs.String("tool", "", "agent tool involved (claude, codex, ...)")
	surface := fs.String("surface", "cli", "where the report is filed from (cli, tui, mac app)")
	dryRun := fs.Bool("dry-run", false, "print the exact issue and the fallback URL; send nothing")
	jsonOut := fs.Bool("json", false, "machine output; never prompts")
	yes := fs.Bool("yes", false, "confirm sending (required with --json)")
	noOpen := fs.Bool("no-open", false, "print the prefilled URL instead of opening a browser")
	issueSrc := fs.String("issue", "", "file exactly this previewed issue: a JSON file from --dry-run --json, or - for stdin")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")

	fail := func(code issuereport.ErrCode, msg, url string) int {
		if *jsonOut {
			writeFeedbackJSON(w, feedbackReportJSON{ErrorCode: string(code), Message: msg, URL: url})
		} else {
			fmt.Fprintln(w, "Error: "+msg)
			if url != "" {
				fmt.Fprintln(w, "You can still file it in your browser:")
				fmt.Fprintln(w, url)
			}
		}
		return 1
	}

	if err := fs.Parse(args); err != nil {
		for _, a := range args {
			if a == "--json" || a == "-json" {
				*jsonOut = true
			}
		}
		return fail(issuereport.ErrInvalidInput, err.Error(), "")
	}
	if *help {
		printFeedbackReportHelp(w, kind)
		return 0
	}
	interactive := !*jsonOut && !*dryRun
	var reader *bufio.Reader
	if interactive {
		reader = bufio.NewReader(in)
	}
	notSent := func() int {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Not sent. Nothing left this machine.")
		return 130
	}

	var is issuereport.Issue
	if *issueSrc != "" {
		// --issue: file exactly the issue the caller previewed (the Mac
		// app shows the --dry-run --json text, then sends it back). No
		// rebuild, no new log read: what was shown is what is filed.
		surfaceSet := false
		fs.Visit(func(f *flag.Flag) { surfaceSet = surfaceSet || f.Name == "surface" })
		if *title != "" || *what != "" || *expected != "" || *steps != "" || *tool != "" || surfaceSet || fs.NArg() > 0 {
			return fail(issuereport.ErrInvalidInput, "--issue files a previewed issue as-is; it cannot be combined with --title, --what, --expected, --steps, --tool, --surface or a title argument", "")
		}
		if *issueSrc == "-" && interactive && !*yes {
			return fail(issuereport.ErrInvalidInput, "--issue - reads the issue from stdin, so the y/N prompt cannot; add --yes after showing the text, or pass a file", "")
		}
		var err error
		if is, err = readPreviewedIssue(*issueSrc, in); err != nil {
			return fail(issuereport.ErrInvalidInput, err.Error(), "")
		}
		clean, err := issuereport.SanitizeIssue(kind, is)
		if err != nil {
			return fail(issuereport.ErrInvalidInput, err.Error(), "")
		}
		// The scrubber removed something (a token, a host, an escape
		// sequence). With --yes nobody sees the cleaned text before it
		// goes, so refuse; a prompt or a dry run shows the cleaned text.
		if (clean.Title != is.Title || clean.Body != is.Body) && *yes && !*dryRun {
			return fail(issuereport.ErrInvalidInput, "--issue: the issue has text the scrubber removes (a secret, a host name, a control character); it is not the text a preview printed. Preview it again with --dry-run and send that", "")
		}
		is = clean
	} else {
		if *title == "" && fs.NArg() > 0 {
			*title = strings.Join(fs.Args(), " ")
		}
		r := issuereport.Report{Kind: kind, Title: *title, WhatHappened: *what, Expected: *expected,
			Steps: *steps, Tool: *tool, Surface: *surface}
		if interactive {
			if err := promptMissingReportFields(ctx, w, reader, &r); err != nil {
				return notSent()
			}
		}
		if err := r.Validate(); err != nil {
			return fail(issuereport.CodeOf(err), err.Error(), "")
		}
		logLines := 0
		if kind == issuereport.KindBug {
			logLines = feedbackReportLogLines
		}
		is = issuereport.Build(r, feedbackCollectDiagnostics(version, logLines))
	}
	fallbackURL, trimmed := issuereport.NewIssueURL(is)

	switch {
	case *jsonOut && *dryRun:
		ready := feedbackGhReady(ctx)
		writeFeedbackJSON(w, feedbackReportJSON{OK: true, DryRun: true, Title: is.Title, Labels: is.Labels,
			Body: is.Body, URL: fallbackURL, URLTrimmed: &trimmed, GhReady: &ready})
		return 0
	case *jsonOut && !*yes:
		writeFeedbackJSON(w, feedbackReportJSON{ErrorCode: errCodeConfirmRequired,
			Message: "sending needs --yes after the user has seen the full issue (get it with --dry-run)"})
		return 1
	case *dryRun:
		fmt.Fprintln(w, "Dry run: nothing is sent.")
		fmt.Fprintln(w)
		printIssuePreview(w, is)
		if feedbackGhReady(ctx) {
			fmt.Fprintln(w, "Would file with: gh issue create (gh is installed and logged in)")
		} else {
			fmt.Fprintln(w, "Would file with: your browser (gh is not installed or not logged in)")
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Prefilled GitHub URL (browser fallback):")
		fmt.Fprintln(w, fallbackURL)
		if trimmed {
			fmt.Fprintf(w, "(the URL version is trimmed to stay under %d characters; the log is cut first)\n", issuereport.MaxURLLen)
			fmt.Fprintln(w, trimmedBrowserNote)
		}
		return 0
	}

	if !*jsonOut {
		ready := feedbackGhReady(ctx)
		fmt.Fprintln(w)
		browserTrimmed := !ready && trimmed
		fmt.Fprintln(w, "This issue will be filed PUBLICLY on https://github.com/"+issuereport.Repo+"/issues")
		if browserTrimmed {
			fmt.Fprintln(w, "Full text (the browser page gets a SHORTER version of it; see the note below):")
		} else {
			fmt.Fprintln(w, "Full text (no more, no less):")
		}
		fmt.Fprintln(w)
		printIssuePreview(w, is)
		switch {
		case ready:
			who := "your GitHub account"
			if login := ghUserLogin(); login != "" {
				who = "@" + login
			}
			fmt.Fprintf(w, "How: gh issue create, as %s.\n", who)
		case *noOpen || !feedbackBrowserAvailable():
			fmt.Fprintln(w, "How: gh is not installed or not logged in; you get a prefilled GitHub URL and press Submit there.")
		default:
			fmt.Fprintln(w, "How: gh is not installed or not logged in; your browser opens a prefilled GitHub page and you press Submit there.")
		}
		if browserTrimmed {
			fmt.Fprintln(w, trimmedBrowserNote)
		}
		if !*yes {
			fmt.Fprint(w, "File this on GitHub? [y/N]: ")
			answer, err := readLineCtx(ctx, reader)
			if err != nil && ctx.Err() != nil {
				return notSent()
			}
			if !isYesConfirmation(answer) {
				fmt.Fprintln(w, "Not sent. Nothing left this machine.")
				return 0
			}
		}
	}

	res, err := feedbackFileIssue(ctx, is, issuereport.FileOptions{OpenBrowser: !*jsonOut && !*noOpen})
	if err != nil {
		var ie *issuereport.Error
		url := ""
		if errors.As(err, &ie) {
			url = ie.URL
		}
		code := issuereport.CodeOf(err)
		if code == "" {
			code = issuereport.ErrGhFailed
		}
		return fail(code, err.Error(), url)
	}
	if *jsonOut {
		out := feedbackReportJSON{OK: true, Method: res.Method, URL: res.URL}
		if res.Method == "browser" {
			out.URLTrimmed = &trimmed
		}
		writeFeedbackJSON(w, out)
		return 0
	}
	switch {
	case res.Method == "gh":
		fmt.Fprintln(w, "Filed: "+res.URL)
	case *noOpen:
		fmt.Fprintln(w, "Open this URL and press Submit to file it:")
		fmt.Fprintln(w, res.URL)
	case !res.Opened:
		fmt.Fprintln(w, "No browser can open here (headless or SSH session). Open this URL on any machine and press Submit to file it:")
		fmt.Fprintln(w, res.URL)
	default:
		fmt.Fprintln(w, "Opened your browser on the prefilled issue; press Submit there to file it.")
		fmt.Fprintln(w, res.URL)
	}
	return 0
}

// trimmedBrowserNote is printed whenever the browser fallback will carry a
// shorter body than the preview (the prefilled URL has a length limit).
var trimmedBrowserNote = fmt.Sprintf("Note: the browser page gets a TRIMMED text: a prefilled GitHub URL must stay under %d characters, so the log excerpt (then the longest field) is cut and marked (trimmed). GitHub shows the page before you press Submit.", issuereport.MaxURLLen)

// readLineCtx reads one line, or returns ctx.Err() as soon as ctx is done
// (Ctrl-C at a prompt: signal.NotifyContext cancels ctx while the read is
// still blocked on the terminal).
func readLineCtx(ctx context.Context, reader *bufio.Reader) (string, error) {
	type line struct {
		s   string
		err error
	}
	ch := make(chan line, 1)
	go func() {
		s, err := reader.ReadString('\n')
		ch <- line{s, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case l := <-ch:
		return l.s, l.err
	}
}

// previewedIssueJSON is what --issue reads: the title, labels and body
// keys of the --dry-run --json output (other keys are ignored).
type previewedIssueJSON struct {
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
	Body   string   `json:"body"`
}

// maxIssueJSONBytes bounds what --issue reads (a body is at most
// issuereport.MaxBodyLen; JSON escaping can double it).
const maxIssueJSONBytes = 4*issuereport.MaxBodyLen + 64*1024

func readPreviewedIssue(src string, in io.Reader) (issuereport.Issue, error) {
	var r io.Reader
	if src == "-" {
		r = in
	} else {
		f, err := os.Open(src) // #nosec G304 -- a file the user named on the command line
		if err != nil {
			return issuereport.Issue{}, fmt.Errorf("--issue: %w", err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxIssueJSONBytes+1))
	if err != nil {
		return issuereport.Issue{}, fmt.Errorf("--issue: read: %w", err)
	}
	if len(data) > maxIssueJSONBytes {
		return issuereport.Issue{}, fmt.Errorf("--issue: input is larger than %d bytes", maxIssueJSONBytes)
	}
	var p previewedIssueJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return issuereport.Issue{}, fmt.Errorf("--issue: not the JSON --dry-run --json prints: %w", err)
	}
	return issuereport.Issue{Title: p.Title, Labels: p.Labels, Body: p.Body}, nil
}

// promptMissingReportFields asks for the fields not given as flags. It
// returns ctx.Err() when the user pressed Ctrl-C at a prompt.
func promptMissingReportFields(ctx context.Context, w io.Writer, reader *bufio.Reader, r *issuereport.Report) error {
	var cancelled error
	ask := func(prompt string, dst *string) {
		if cancelled != nil || strings.TrimSpace(*dst) != "" {
			return
		}
		fmt.Fprint(w, prompt)
		line, err := readLineCtx(ctx, reader)
		if err != nil && ctx.Err() != nil {
			cancelled = ctx.Err()
			return
		}
		*dst = strings.TrimSpace(line)
	}
	if r.Kind == issuereport.KindIdea {
		ask("What problem would this solve? ", &r.WhatHappened)
		ask("Proposed behavior: ", &r.Expected)
		ask("Alternatives considered (optional, Enter to skip): ", &r.Steps)
	} else {
		ask("What happened? ", &r.WhatHappened)
		ask("What did you expect? ", &r.Expected)
		ask("Steps to reproduce (optional, Enter to skip): ", &r.Steps)
	}
	ask("Title (optional, Enter to use the first line above): ", &r.Title)
	return cancelled
}

func printIssuePreview(w io.Writer, is issuereport.Issue) {
	const rule = "────────────────────────────────────────────────────────"
	fmt.Fprintln(w, rule)
	fmt.Fprint(w, is.Text())
	fmt.Fprintln(w, rule)
}

func printFeedbackReportHelp(w io.Writer, kind issuereport.Kind) {
	fmt.Fprintf(w, "Usage: agent-deck feedback %s [flags]\n", kind)
	fmt.Fprintln(w)
	if kind == issuereport.KindIdea {
		fmt.Fprintln(w, "Propose an idea as a GitHub issue on asheshgoplani/agent-deck (feature form).")
	} else {
		fmt.Fprintln(w, "Report a bug as a GitHub issue on asheshgoplani/agent-deck (bug form), with")
		fmt.Fprintln(w, "version, OS, terminal and the last lines of the agent-deck log attached.")
	}
	fmt.Fprintln(w, "Home paths become ~; tokens, emails, hostnames and IPs are replaced by placeholders.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Nothing is sent until you have seen the full issue and answered y.")
	fmt.Fprintln(w, "With gh installed and logged in it files via `gh issue create`; otherwise")
	fmt.Fprintln(w, "it opens a prefilled GitHub page in your browser and you press Submit there.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --title T        one-line title (default: first line of --what)")
	if kind == issuereport.KindIdea {
		fmt.Fprintln(w, "  --what TEXT      what problem would this solve?")
		fmt.Fprintln(w, "  --expected TEXT  proposed behavior")
		fmt.Fprintln(w, "  --steps TEXT     alternatives considered (optional)")
	} else {
		fmt.Fprintln(w, "  --what TEXT      what happened")
		fmt.Fprintln(w, "  --expected TEXT  what you expected")
		fmt.Fprintln(w, "  --steps TEXT     steps to reproduce (optional)")
	}
	fmt.Fprintln(w, "  --tool T         agent tool involved (claude, codex, ...)")
	fmt.Fprintln(w, "  --dry-run        print the exact issue and the prefilled URL; send nothing")
	fmt.Fprintln(w, "  --json           JSON output, never prompts; sending also needs --yes")
	fmt.Fprintln(w, "  --yes            confirm sending without the y/N prompt")
	fmt.Fprintln(w, "  --no-open        print the prefilled URL instead of opening a browser")
	fmt.Fprintln(w, "  --issue FILE|-   file exactly the issue a --dry-run --json printed (title, labels,")
	fmt.Fprintln(w, "                   body); nothing is rebuilt. With - it is read from stdin and needs --yes.")
	fmt.Fprintln(w, "                   It is scrubbed again and must carry the form's sections; with --yes,")
	fmt.Fprintln(w, "                   text the scrubber would change is refused (invalid_input)")
}
