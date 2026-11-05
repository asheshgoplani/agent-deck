package main

// ASK-205: the report -> fix -> "what's new" -> "is it fixed for you?" loop.
//
//   agent-deck feedback bug --from-agent ...   (agents: non-interactive, de-duplicated)
//   agent-deck feedback mine                   (reports filed from this machine)
//   agent-deck feedback fixed N --fixed|--not-fixed
//
// Every report number is recorded in internal/reportloop's store; the
// release CHANGELOG ties it to the version that fixes it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"golang.org/x/term"
)

var reportLoopLog = logging.ForComponent(logging.CompUpdate)

// agentLabel marks issues an agent session filed.
const agentLabel = "found-by-agent"

// agentFooter is appended to the body of an agent-filed issue.
const agentFooter = "Found by an agent session (agent-deck --from-agent)."

// agentInstanceEnv is set inside every agent-deck session; --from-agent
// refuses to run outside one unless --yes is given.
const agentInstanceEnv = "AGENTDECK_INSTANCE_ID"

// agentModelEnv names the model an agent session runs; an agent's issue
// declares it in the AI disclosure ("unsure" when unset).
const agentModelEnv = "AGENTDECK_AGENT_MODEL"

// errCodeConsentRequired: --from-agent while [feedback] agent_reports is off.
const errCodeConsentRequired = "consent_required"

// agentConsentMessage tells a person how to opt in to agent posting.
const agentConsentMessage = "agent sessions may not post on GitHub from this machine: [feedback] agent_reports is off. " +
	"A person opts in once with `agent-deck feedback agent-consent`, which shows what is posted, where and as which " +
	"GitHub account, and asks y/N"

// agentReportsAllowed reports whether a person turned on [feedback]
// agent_reports (`agent-deck feedback agent-consent`). --yes never stands in.
func agentReportsAllowed() bool {
	cfg, err := session.LoadUserConfig()
	return err == nil && cfg != nil && cfg.Feedback.AgentReports
}

// readerIsTerminal reports whether in is an interactive terminal; a pipe,
// a file or a test reader is not. A variable so tests can stand in a terminal.
var readerIsTerminal = func(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) // #nosec G115 -- a file descriptor fits in int
}

// inAgentSession reports that this process runs inside an agent-deck
// session (AGENTDECK_INSTANCE_ID set to a non-blank value).
func inAgentSession() bool {
	return strings.TrimSpace(os.Getenv(agentInstanceEnv)) != ""
}

// inSessionConsentMessage is the consent_required message when a post
// without --from-agent comes from inside an agent session:
// nobody there is known to be a person, so it is an agent's post.
const inSessionConsentMessage = "this runs inside an agent session (" + agentInstanceEnv + " is set), " +
	"so it posts as an agent; nothing was sent. A person posts from the TUI, the Mac app or a terminal outside agent-deck sessions; for agents, " + agentConsentMessage

// agentPostInSession decides a real post (not a dry run) without
// --from-agent: inside an agent session (terminal or not) it is an
// agent's post. It returns asAgent=true when [feedback] agent_reports lets
// agents post (the caller then takes the --from-agent path: AI-authored, no
// log tail, label and footer, de-duplicated, recorded as an agent's), and
// refuse=true when it is off. Outside a session (the Mac app runs --json
// --yes from a GUI process without the env) it changes nothing.
func agentPostInSession(in io.Reader) (asAgent, refuse bool) {
	// The session env alone decides: an agent can put a pseudo-terminal on
	// stdin (script -q, or a tool whose shell is a pty), so a terminal is
	// no sign of a person here. People post from the TUI (which drops the
	// env for its child), the Mac app, or a shell outside a session.
	if !inAgentSession() {
		return false, false
	}
	if !agentReportsAllowed() {
		return false, true
	}
	return true, false
}

// ghIssueRef is one row of `gh issue list --json number,title,url,body`.
type ghIssueRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// Seams for tests: production runs gh / reads the real store and changelog.
var (
	feedbackGhListIssues = ghListAgentIssues
	feedbackGhComment    = ghIssueComment
	feedbackReportStore  = reportloop.Default
)

const ghLoopTimeout = 60 * time.Second

// runGh runs gh (found on PATH) with the childenv-based gh environment and
// returns its stdout. A failure carries gh's first stderr line, scrubbed.
func runGh(ctx context.Context, stdin string, args ...string) (string, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("finding gh: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, ghLoopTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, args...) // #nosec G204 -- gh with our own args
	cmd.Env = issuereport.GhEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		home, _ := os.UserHomeDir()
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		msg = strings.TrimSpace(issuereport.Scrub(msg, home))
		if msg == "" {
			return "", fmt.Errorf("gh %s: %w", args[0]+" "+args[1], err)
		}
		return "", fmt.Errorf("gh %s: %s: %w", args[0]+" "+args[1], msg, err)
	}
	return stdout.String(), nil
}

// ghListAgentIssues lists open issues matching search (agentDedupeSearch).
// It does not filter on the found-by-agent label: an issue filed while the
// label did not exist carries only the footer.
func ghListAgentIssues(ctx context.Context, search string) ([]ghIssueRef, error) {
	out, err := runGh(ctx, "", "issue", "list", "--repo", issuereport.Repo, "--state", "open",
		"--search", search, "--json", "number,title,url,body", "--limit", "20")
	if err != nil {
		return nil, err
	}
	var refs []ghIssueRef
	if err := json.Unmarshal([]byte(out), &refs); err != nil {
		return nil, fmt.Errorf("parsing gh issue list output: %w", err)
	}
	return refs, nil
}

// ghIssueComment posts body on issue n and returns the comment URL.
func ghIssueComment(ctx context.Context, n int, body string) (string, error) {
	out, err := runGh(ctx, body, "issue", "comment", strconv.Itoa(n), "--repo", issuereport.Repo, "--body-file", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(lastLine(out)), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ── title matching (dedupe) ────────────────────────────────────────────

var titleKindRe = regexp.MustCompile(`(?i)^\s*\[(bug|feature)\]`)

// titleKind is "bug" or "feature" from a title's first form prefix, or "".
func titleKind(s string) string {
	if m := titleKindRe.FindStringSubmatch(s); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// normalizeReportTitle lowercases, drops the [bug]/[feature] prefixes and
// turns every run of punctuation and space into one space.
func normalizeReportTitle(s string) string {
	s = strings.ToLower(issuereport.StripKindPrefix(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// sameReportTitle reports whether two issue titles name the same problem:
// the same kind (when both carry a form prefix) and exactly equal after
// normalizing. Near matches are NOT the same: "works" vs "fails" or an
// added "not" differ by one word and are opposite reports.
func sameReportTitle(a, b string) bool {
	if ka, kb := titleKind(a), titleKind(b); ka != "" && kb != "" && ka != kb {
		return false
	}
	na, nb := normalizeReportTitle(a), normalizeReportTitle(b)
	return na != "" && na == nb
}

// agentDedupeSearch is the gh search for open agent issues with title's
// problem: the footer every agent issue carries, plus the title words.
func agentDedupeSearch(title string) string {
	return `"Found by an agent session" in:body ` + normalizeReportTitle(title) + " in:title"
}

// seenAgainMaxRunes caps the what-happened text of a "seen again" comment.
const seenAgainMaxRunes = 4000

// capSeenAgain cuts s to at most seenAgainMaxRunes characters (on a rune
// boundary), ending the cut text with "… (trimmed)".
func capSeenAgain(s string) string {
	r := []rune(s)
	if len(r) <= seenAgainMaxRunes {
		return s
	}
	const mark = "\n… (trimmed)"
	return string(r[:seenAgainMaxRunes-len([]rune(mark))]) + mark
}

// labelMissing reports whether a gh issue create failure is a label that
// does not exist on the repo (gh: "could not add label: 'x' not found").
func labelMissing(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "label") && strings.Contains(m, "not found")
}

// scrubText is the scrubber pass Build applies to every user field.
func scrubText(s string) string {
	home, _ := os.UserHomeDir()
	return strings.TrimSpace(issuereport.Scrub(strings.TrimSpace(issuereport.StripControl(s, true)), home))
}

// issueNumberFromURL returns N from https://github.com/o/r/issues/N.
func issueNumberFromURL(u string) int {
	i := strings.LastIndex(u, "/issues/")
	if i < 0 {
		return 0
	}
	rest := u[i+len("/issues/"):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return n
}

// recordReport stores a filed report; a failure is logged and returned as a
// note for the caller to print, never an error (the issue is filed).
func recordReport(r reportloop.Report) string {
	if r.Number <= 0 {
		return ""
	}
	store, err := feedbackReportStore()
	if err == nil {
		err = store.Record(r)
	}
	if err != nil {
		reportLoopLog.Warn("report_record_failed", slog.Int("number", r.Number), slog.String("error", err.Error()))
		return fmt.Sprintf("Note: could not record #%d in this machine's report list: %v", r.Number, err)
	}
	return ""
}

// ── feedback bug --from-agent ─────────────────────────────────────────

// fileAgentReport is the --from-agent path of `feedback bug|idea`: no
// prompt, no browser, gh only, de-duplicated against open agent issues.
func fileAgentReport(ctx context.Context, kind issuereport.Kind, is issuereport.Issue, what, version string, jsonOut bool, w io.Writer,
	fail func(issuereport.ErrCode, string, string) int) int {
	if !feedbackGhReady(ctx) {
		return fail(issuereport.ErrGhFailed, "gh is not installed or not logged in; --from-agent files only through gh (run `gh auth login`)", "")
	}
	refs, err := feedbackGhListIssues(ctx, agentDedupeSearch(is.Title))
	if err != nil {
		return fail(issuereport.ErrGhFailed, "checking open agent reports for a duplicate: "+err.Error(), "")
	}
	rec := reportloop.Report{Title: is.Title, Kind: string(kind), FiledVersion: strings.TrimPrefix(version, "v"), FromAgent: true}
	deduped := false
	var number int
	var url string
	for _, ref := range refs {
		// Only an agent issue (it carries the footer) with exactly this title.
		if !strings.Contains(ref.Body, agentFooter) || !sameReportTitle(ref.Title, is.Title) {
			continue
		}
		body := fmt.Sprintf("Seen again on v%s (%s/%s) by an agent session.", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
		if s := capSeenAgain(scrubText(what)); s != "" {
			body += "\n\n" + s
		}
		curl, err := feedbackGhComment(ctx, ref.Number, body)
		if err != nil {
			return fail(issuereport.ErrGhFailed, fmt.Sprintf("commenting on duplicate #%d: %v", ref.Number, err), "")
		}
		deduped, number, url = true, ref.Number, ref.URL
		if url == "" {
			url = curl
		}
		rec.Title = ref.Title
		break
	}
	if !deduped {
		res, err := feedbackFileIssue(ctx, is, issuereport.FileOptions{OpenBrowser: false})
		if err != nil && issuereport.CodeOf(err) == issuereport.ErrGhFailed && labelMissing(err) && len(is.Labels) > 0 {
			// The repo lacks a label (found-by-agent is created by a
			// maintainer): file once more without labels; the footer
			// still marks it as an agent issue.
			reportLoopLog.Warn("agent_report_label_missing_retry", slog.String("error", err.Error()))
			unlabelled := is
			unlabelled.Labels = nil
			res, err = feedbackFileIssue(ctx, unlabelled, issuereport.FileOptions{OpenBrowser: false})
		}
		if err != nil {
			code := issuereport.CodeOf(err)
			if code == "" || code == issuereport.ErrBrowserFailed {
				code = issuereport.ErrGhFailed
			}
			return fail(code, err.Error(), "")
		}
		if res.Method != "gh" {
			return fail(issuereport.ErrGhFailed, "gh did not file the issue; --from-agent never falls back to a browser", "")
		}
		number, url = issueNumberFromURL(res.URL), res.URL
	}
	rec.Number, rec.URL, rec.Deduped = number, url, deduped
	note := recordReport(rec)
	if jsonOut {
		writeFeedbackJSON(w, feedbackReportJSON{OK: true, Number: number, URL: url, Deduped: &deduped, Method: "gh"})
		return 0
	}
	if deduped {
		fmt.Fprintf(w, "Already reported as #%d; added a \"seen again\" comment: %s\n", number, url)
	} else {
		fmt.Fprintln(w, "Filed: "+url)
	}
	if note != "" {
		fmt.Fprintln(w, note)
	}
	return 0
}

// agentIssue adds the found-by-agent label and footer to a built issue.
func agentIssue(is issuereport.Issue) issuereport.Issue {
	is.Labels = append(append([]string(nil), is.Labels...), agentLabel)
	is.Body = strings.TrimRight(is.Body, "\n") + "\n" + agentFooter + "\n"
	return is
}

// ── feedback mine ─────────────────────────────────────────────────────

type feedbackMineJSON struct {
	OK      bool                `json:"ok"`
	Reports []reportloop.Report `json:"reports"`
}

// runFeedbackMine implements `agent-deck feedback mine [--json]`.
func runFeedbackMine(ctx context.Context, args []string, version string, w io.Writer) int {
	fs := flag.NewFlagSet("feedback mine", flag.ContinueOnError)
	current := strings.TrimPrefix(version, "v")
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(w, "Error: "+err.Error())
		return 1
	}
	if *help {
		fmt.Fprintln(w, "Usage: agent-deck feedback mine [--json]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "List the issues filed (or confirmed as duplicates) from this machine, the")
		fmt.Fprintln(w, "version each was filed on, the release that fixed it and your answer to")
		fmt.Fprintln(w, "\"Is it fixed for you?\". Nothing is sent; the list lives in reports.json.")
		return 0
	}
	store, err := feedbackReportStore()
	var st *reportloop.State
	if err == nil {
		st, err = store.Load()
	}
	if err != nil {
		if *jsonOut {
			writeFeedbackJSON(w, feedbackReportJSON{ErrorCode: "store_failed", Message: err.Error()})
		} else {
			fmt.Fprintln(w, "Error: "+err.Error())
		}
		return 1
	}
	reports := st.Reports
	if reports == nil {
		reports = []reportloop.Report{}
	}
	// fixed_in from the changelog (best effort, read-only): the version
	// after the filed one that cites the number, not newer than this one.
	// Without the changelog the stored value stands (unless not installed).
	changelog := ""
	if len(reports) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		cl, err := whatsNewFetchChangelog(cctx)
		cancel()
		if err != nil {
			reportLoopLog.Debug("feedback_mine_changelog_unavailable", slog.String("error", err.Error()))
		} else {
			changelog = cl
		}
	}
	for i := range reports {
		// Read-only: What's new persists the same refresh.
		reportloop.Refresh(changelog, &reports[i], current)
	}
	if *jsonOut {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(feedbackMineJSON{OK: true, Reports: reports})
		return 0
	}
	if len(reports) == 0 {
		fmt.Fprintln(w, "No reports filed from this machine yet.")
		fmt.Fprintln(w, "Report a problem with: agent-deck feedback bug")
		return 0
	}
	for _, r := range reports {
		state := "not fixed yet"
		if r.FixedIn != "" {
			state = "fixed in v" + r.FixedIn
		}
		answer := ""
		switch {
		case r.Feedback != nil:
			answer = ", your answer: " + r.Feedback.Answer
		case r.FixedIn != "":
			answer = ", is it fixed for you? " + reportloop.FeedbackCommand(r.Number)
		}
		who := ""
		if r.FromAgent {
			who = ", by an agent"
		}
		fmt.Fprintf(w, "#%d  %s\n     filed in v%s%s, %s%s\n", r.Number, r.Title, r.FiledVersion, who, state, answer)
		if r.URL != "" {
			fmt.Fprintf(w, "     %s\n", r.URL)
		}
	}
	return 0
}

// ── feedback fixed ────────────────────────────────────────────────────

type feedbackFixedJSON struct {
	OK     bool   `json:"ok"`
	DryRun bool   `json:"dry_run,omitempty"`
	Number int    `json:"number,omitempty"`
	Answer string `json:"answer,omitempty"`
	Body   string `json:"body,omitempty"`
	// Account (the gh login, "" when unknown) and GhReady are set on a
	// dry run, so a front end can show the full disclosure.
	Account   *string `json:"account,omitempty"`
	GhReady   *bool   `json:"gh_ready,omitempty"`
	URL       string  `json:"url,omitempty"`
	Note      string  `json:"note,omitempty"`
	ErrorCode string  `json:"error_code,omitempty"`
	Message   string  `json:"message,omitempty"`
}

func writeLoopJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// fixedCommentBody is the exact comment `feedback fixed` posts.
func fixedCommentBody(fixed bool, version, comment string) string {
	v := strings.TrimPrefix(version, "v")
	b := "Fixed for me in v" + v + "."
	if !fixed {
		b = "Still broken for me in v" + v + "."
	}
	if comment != "" {
		b += "\n\n" + comment
	}
	return b + fmt.Sprintf("\n\n(%s/%s, via agent-deck feedback fixed)", runtime.GOOS, runtime.GOARCH)
}

// runFeedbackFixed implements `agent-deck feedback fixed N --fixed|--not-fixed`.
func runFeedbackFixed(ctx context.Context, args []string, version string, in io.Reader, w io.Writer) int {
	fs := flag.NewFlagSet("feedback fixed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fixed := fs.Bool("fixed", false, "it is fixed for me")
	notFixed := fs.Bool("not-fixed", false, "it is still broken for me")
	comment := fs.String("comment", "", "optional one-line comment")
	dryRun := fs.Bool("dry-run", false, "print the exact comment; send nothing")
	jsonOut := fs.Bool("json", false, "JSON output; never prompts (posting needs --yes)")
	yes := fs.Bool("yes", false, "post without the y/N prompt")
	fromAgent := fs.Bool("from-agent", false, "an agent session answers; never prompts")
	again := fs.Bool("again", false, "post another answer on a report already answered")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")

	fail := func(code, msg string) int {
		if *jsonOut {
			writeLoopJSON(w, feedbackFixedJSON{ErrorCode: code, Message: msg})
		} else if code == errCodeConfirmRequired || code == errCodeConsentRequired {
			fmt.Fprintln(w, "Error ("+code+"): "+msg)
		} else {
			fmt.Fprintln(w, "Error: "+msg)
		}
		return 1
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		for _, a := range args {
			if a == "--json" || a == "-json" {
				*jsonOut = true
			}
		}
		return fail(string(issuereport.ErrInvalidInput), err.Error())
	}
	if *help {
		printFeedbackFixedHelp(w)
		return 0
	}
	if fs.NArg() != 1 {
		return fail(string(issuereport.ErrInvalidInput), "give exactly one issue number: agent-deck feedback fixed N --fixed|--not-fixed")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(fs.Arg(0), "#"))
	if err != nil || n <= 0 {
		return fail(string(issuereport.ErrInvalidInput), fmt.Sprintf("%q is not an issue number", fs.Arg(0)))
	}
	if *fixed == *notFixed {
		return fail(string(issuereport.ErrInvalidInput), "say --fixed or --not-fixed (exactly one)")
	}
	answer := "yes"
	if *notFixed {
		answer = "no"
	}
	body := fixedCommentBody(*fixed, version, scrubText(*comment))

	if !*dryRun && !*fromAgent {
		asAgent, refuse := agentPostInSession(in)
		if refuse {
			return fail(errCodeConsentRequired, inSessionConsentMessage)
		}
		*fromAgent = asAgent
	}
	if !*dryRun && *fromAgent && !agentReportsAllowed() {
		return fail(errCodeConsentRequired, agentConsentMessage)
	}
	if !*dryRun && *fromAgent && os.Getenv(agentInstanceEnv) == "" && !*yes {
		return fail(string(issuereport.ErrInvalidInput), "--from-agent runs inside an agent-deck session ("+agentInstanceEnv+" is not set); add --yes to post anyway")
	}

	var rep *reportloop.Report
	if store, err := feedbackReportStore(); err == nil {
		if st, err := store.Load(); err == nil {
			rep = st.Find(n)
		}
	}
	switch {
	case *fromAgent && (rep == nil || !rep.FromAgent):
		// An agent answers only for a report an agent filed from here.
		return fail(string(issuereport.ErrInvalidInput), fmt.Sprintf("--from-agent answers only an agent report filed from this machine, and #%d is not one (see `agent-deck feedback mine`); a person answers it without --from-agent", n))
	case *fromAgent && rep.Feedback != nil:
		return fail(string(issuereport.ErrInvalidInput), fmt.Sprintf("#%d already has an answer (%s on v%s); an agent answers once", n, rep.Feedback.Answer, rep.Feedback.Version))
	case rep != nil && rep.Feedback != nil && !*again:
		return fail(string(issuereport.ErrInvalidInput), fmt.Sprintf("you already answered #%d (%s on v%s); add --again to post another answer", n, rep.Feedback.Answer, rep.Feedback.Version))
	}
	// A dry run passed the same checks a post makes (a refusal above is
	// reported as ok:false with error_code, so a front end disables posting).
	if *dryRun {
		if *jsonOut {
			login, ready := ghUserLogin(), feedbackGhReady(ctx)
			writeLoopJSON(w, feedbackFixedJSON{OK: true, DryRun: true, Number: n, Body: body, Account: &login, GhReady: &ready})
			return 0
		}
		fmt.Fprintln(w, "Dry run: nothing is sent.")
		renderPublicPostDisclosure(w, issueDisclosure(n), body, ghUserLogin())
		return 0
	}
	if *jsonOut && !*yes && !*fromAgent {
		writeLoopJSON(w, feedbackFixedJSON{ErrorCode: errCodeConfirmRequired,
			Message: "posting needs --yes after the user has seen the exact comment (get it with --dry-run)"})
		return 1
	}

	note := ""
	if rep == nil {
		note = fmt.Sprintf("Note: #%d is not one of this machine's reports; the answer is posted but not recorded here.", n)
	}

	if !*jsonOut && !*fromAgent && !*yes {
		renderPublicPostDisclosure(w, issueDisclosure(n), body, ghUserLogin())
		if note != "" {
			fmt.Fprintln(w, note)
		}
		fmt.Fprint(w, "Post this? [y/N]: ")
		line, err := readLineCtx(ctx, bufio.NewReader(in))
		if (err != nil && ctx.Err() != nil) || !isYesConfirmation(line) {
			fmt.Fprintln(w, "Not posted. Nothing left this machine.")
			return 0
		}
	}

	if !feedbackGhReady(ctx) {
		return fail(string(issuereport.ErrGhFailed), "gh is not installed or not logged in; the answer was NOT posted (run `gh auth login`)")
	}
	url, err := feedbackGhComment(ctx, n, body)
	if err != nil {
		return fail(string(issuereport.ErrGhFailed), fmt.Sprintf("posting on #%d: %v; the answer was NOT posted", n, err))
	}
	if store, err := feedbackReportStore(); err == nil {
		if err := store.RecordFeedback(n, answer, version); err != nil && !errors.Is(err, reportloop.ErrUnknownReport) {
			reportLoopLog.Warn("report_feedback_record_failed", slog.Int("number", n), slog.String("error", err.Error()))
			note = fmt.Sprintf("Note: posted, but could not record the answer locally: %v", err)
		}
	}
	if *jsonOut {
		writeLoopJSON(w, feedbackFixedJSON{OK: true, Number: n, Answer: answer, URL: url, Note: note})
		return 0
	}
	if note != "" && (*fromAgent || *yes) {
		fmt.Fprintln(w, note)
	}
	fmt.Fprintf(w, "Posted on #%d: %s\n", n, url)
	return 0
}

// publicPost describes where a public post lands, for the disclosure.
type publicPost struct {
	subject string // "This feedback"
	where   string // URL
	place   string // "discussion" / "issue"
	page    string // "that discussion page"
}

func issueDisclosure(n int) publicPost {
	return publicPost{subject: "This answer", where: fmt.Sprintf("https://github.com/%s/issues/%d", issuereport.Repo, n),
		place: "issue", page: "that issue page"}
}

func printFeedbackFixedHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck feedback fixed N --fixed|--not-fixed [--comment C] [--dry-run] [--json] [--yes] [--from-agent] [--again]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Answer \"Is it fixed for you?\" on issue #N with a public comment:")
	fmt.Fprintln(w, "  \"Fixed for me in vX.\" or \"Still broken for me in vX.\", your comment (scrubbed:")
	fmt.Fprintln(w, "  home paths become ~, tokens and host names are replaced), then (<os>/<arch>).")
	fmt.Fprintln(w, "The CLI shows where it posts, as which GitHub account, and the exact text, then")
	fmt.Fprintln(w, "asks y/N (default N). It posts with `gh issue comment` and records your answer")
	fmt.Fprintln(w, "so What's new asks once; a second answer on the same report needs --again.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --fixed | --not-fixed   your answer (exactly one)")
	fmt.Fprintln(w, "  --comment TEXT          optional comment")
	fmt.Fprintln(w, "  --dry-run               print the exact comment; send nothing (--json adds account, gh_ready)")
	fmt.Fprintln(w, "  --json                  JSON output, never prompts; posting also needs --yes")
	fmt.Fprintln(w, "  --yes                   post without the y/N prompt")
	fmt.Fprintln(w, "  --again                 answer a report you already answered")
	fmt.Fprintln(w, "  --from-agent            an agent session answers: never prompts; only an unanswered report an")
	fmt.Fprintln(w, "                          agent filed from this machine; needs [feedback] agent_reports (a person")
	fmt.Fprintln(w, "                          runs 'agent-deck feedback agent-consent'; --yes does not stand in) and")
	fmt.Fprintln(w, "                          "+agentInstanceEnv+" or --yes")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Inside an agent session ("+agentInstanceEnv+" set), terminal or not, a post")
	fmt.Fprintln(w, "is an agent's even without --from-agent: it refuses with consent_required unless")
	fmt.Fprintln(w, "[feedback] agent_reports is on, and then takes the --from-agent path. --dry-run")
	fmt.Fprintln(w, "sends nothing and runs the same checks (a refusal is ok:false with error_code).")
}

// ── feedback agent-consent ────────────────────────────────────────────

// runFeedbackAgentConsent implements `agent-deck feedback agent-consent
// [--off]`: a person reads what agent sessions would post, where and as
// whom, and answers y/N; y sets [feedback] agent_reports = true.
func runFeedbackAgentConsent(ctx context.Context, args []string, in io.Reader, w io.Writer) int {
	fs := flag.NewFlagSet("feedback agent-consent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	off := fs.Bool("off", false, "turn agent reports off")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "agent-consent takes only --off"
		if err != nil {
			msg = err.Error()
		}
		fmt.Fprintln(w, "Error: "+msg)
		return 1
	}
	if *help {
		printFeedbackAgentConsentHelp(w)
		return 0
	}
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		fmt.Fprintf(w, "Error: reading config.toml: %v\n", err)
		return 1
	}
	save := func(on bool) int {
		cfg.Feedback.AgentReports = on
		if err := session.SaveUserConfig(cfg); err != nil {
			fmt.Fprintf(w, "Error: saving config.toml: %v\n", err)
			return 1
		}
		session.ClearUserConfigCache()
		return 0
	}
	if *off {
		if code := save(false); code != 0 {
			return code
		}
		fmt.Fprintln(w, "Agent reports are off: agent sessions on this machine post nothing on GitHub.")
		return 0
	}
	// Turning agent posting on is a person's decision at a terminal, never
	// a piped "y" (an agent can pipe one), whatever the environment says.
	if !readerIsTerminal(in) {
		fmt.Fprintln(w, "Error ("+errCodeConfirmRequired+"): a person turns this on in their own terminal; stdin here is not a terminal. Nothing changed: agent reports stay off.")
		return 1
	}
	// Not from inside an agent session either: an agent can fake a terminal.
	if inAgentSession() {
		fmt.Fprintln(w, "Error ("+errCodeConfirmRequired+"): this runs inside an agent session ("+agentInstanceEnv+" is set); a person turns this on in a terminal outside agent-deck sessions. Nothing changed: agent reports stay off.")
		return 1
	}
	renderAgentConsentDisclosure(w, ghUserLogin())
	fmt.Fprint(w, "Let agent sessions on this machine post like this? [y/N]: ")
	line, err := readLineCtx(ctx, bufio.NewReader(in))
	if (err != nil && ctx.Err() != nil) || !isYesConfirmation(line) {
		fmt.Fprintln(w, "Nothing changed: agent reports stay off.")
		return 0
	}
	if code := save(true); code != 0 {
		return code
	}
	fmt.Fprintln(w, "Agent reports are on ([feedback] agent_reports = true). Turn them off with: agent-deck feedback agent-consent --off")
	return 0
}

// renderAgentConsentDisclosure shows what turning on agent reports means.
func renderAgentConsentDisclosure(w io.Writer, login string) {
	as := "your GitHub account (the one gh is logged in as)"
	if login != "" {
		as = "@" + login + " (your own GitHub account, via gh)"
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Agent sessions on this machine would post PUBLICLY on GitHub, with no prompt:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Where:  https://github.com/"+issuereport.Repo+"/issues")
	fmt.Fprintln(w, "  As:     "+as)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "What is posted:")
	fmt.Fprintln(w, "  - new issues (feedback bug|idea --from-agent): the title and text the agent wrote,")
	fmt.Fprintln(w, "    scrubbed (home paths become ~; tokens, emails, host names and IPs are replaced),")
	fmt.Fprintln(w, "    the agent-deck version, OS and terminal; no log lines. Each is marked AI-authored,")
	fmt.Fprintln(w, "    labelled "+agentLabel+" and ends with \""+agentFooter+"\"")
	fmt.Fprintln(w, "  - \"Seen again on vX\" comments on an open agent issue with the same title")
	fmt.Fprintln(w, "  - \"Fixed for me\" / \"Still broken for me\" answers on issues an agent filed from here")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Anyone can read these and sees your GitHub username next to them.")
	fmt.Fprintln(w, "Turn it off any time: agent-deck feedback agent-consent --off")
	fmt.Fprintln(w)
}

func printFeedbackAgentConsentHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck feedback agent-consent [--off]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Let agent sessions on this machine file issues and answers on GitHub with")
	fmt.Fprintln(w, "--from-agent. It shows what is posted, where and as which GitHub account, then")
	fmt.Fprintln(w, "asks y/N (default N); y sets [feedback] agent_reports = true in config.toml.")
	fmt.Fprintln(w, "Until then --from-agent refuses with consent_required (--yes does not stand in).")
	fmt.Fprintln(w, "Turning it on needs a terminal on stdin: a piped answer is refused with")
	fmt.Fprintln(w, "confirm_required and nothing changes.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --off   turn agent reports off again")
}
