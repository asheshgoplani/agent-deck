package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/asheshgoplani/agent-deck/internal/ask"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const askUsage = `Usage: agent-deck ask <command> [options]

Ask the owner for a secret, a choice or a short text in a dialog: the
AgentDeck app, else the TUI, else a command the owner runs in a terminal
(docs/ask.md). A secret goes from the dialog to this host's vault
(/usr/local/bin/deck-secret); this command never sees or prints it.

Ask (agents):
  secret <scope/NAME> [--reason "why"] [--replace]   store a secret in the vault
  choice "question" --option A --option B [...]      ask for one of 2 to 6 options
  text "question"                                    ask for one short line of text
    --session <id|title>  asking session (default: $AGENTDECK_INSTANCE_ID, then the tmux session)
    --wait 10m            how long the request stays open (at most 30m)
    --fallback-after 20s  print the terminal fallback when no dialog shows it by then
    --no-fallback         exit 5 instead when no dialog shows it by then
    --json                print {"status","name","replaced","answered_by","answer"}

Dialog side (the app, the TUI, the owner):
  deliver <request_id>    stdin: a JSON header line, then the value (never argv)
  cancel <request_id>     --by app|tui|terminal --nonce N   (or --unshown: no dialog showed it)
  answer <request_id>     answer in this terminal (hidden prompt; a terminal only)
  show <request_id>       one request, never a value [--json]
  list                    pending requests, never values [--json] [--all]

Output: secret "stored scope/NAME (replaced=no, by app)"; choice and text the
answer only. Exit codes: 0 stored or answered, 2 refused, 3 cancelled,
4 timed out, 5 nothing can show it (or no vault on this host), 1 other error.`

// deliverHeaderMax caps the deliver header line.
const deliverHeaderMax = 4 << 10

func handleAsk(profile string, args []string) {
	stopLog := initAskDebugLogging()
	code := runAsk(profile, args, os.Stdin, os.Stdout, os.Stderr)
	stopLog()
	if code != 0 {
		exitCLI(code)
	}
}

// initAskDebugLogging writes the ask lifecycle (internal/ask, component
// "ask": ids, states, who acted; never a value, an answer or a nonce) to the
// XDG cache debug.log when AGENTDECK_DEBUG is set, like the TUI. Without it
// nothing is logged. It returns the shutdown to run before exiting.
func initAskDebugLogging() func() {
	if os.Getenv("AGENTDECK_DEBUG") == "" {
		return func() {}
	}
	cacheDir, err := ensureEffectiveCacheDir()
	if err != nil {
		return func() {}
	}
	logging.Init(logging.Config{Debug: true, LogDir: cacheDir, Level: "debug", Format: "json", MaxSizeMB: 10, MaxBackups: 5, MaxAgeDays: 10, Compress: true})
	return logging.Shutdown
}

func runAsk(profile string, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, askUsage)
		return 2
	}
	rest := args[1:]
	switch args[0] {
	case "help", "-h", "--help", "-help":
		fmt.Fprintln(out, askUsage)
		return 0
	case "secret":
		return runAskRequest(profile, ask.KindSecret, rest, out, errOut)
	case "choice":
		return runAskRequest(profile, ask.KindChoice, rest, out, errOut)
	case "text":
		return runAskRequest(profile, ask.KindText, rest, out, errOut)
	case "deliver":
		return runAskDeliver(profile, rest, in, out, errOut)
	case "cancel":
		return runAskCancel(profile, rest, out, errOut)
	case "show":
		return runAskShow(profile, rest, out, errOut)
	case "list":
		return runAskList(profile, rest, out, errOut)
	case "answer":
		return runAskAnswer(profile, rest, in, out, errOut)
	}
	fmt.Fprintf(errOut, "Error: ask: unknown command %q\n%s\n", truncateForError(args[0]), askUsage)
	return 2
}

// askFlags is a FlagSet whose usage starts with the ask usage line.
func askFlags(name string, args []string, out, errOut io.Writer, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet("ask "+name, flag.ContinueOnError)
	fs.SetOutput(helpOutput(args, out, errOut))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck ask "+usage)
		fs.PrintDefaults()
	}
	return fs
}

// parseAsk parses args; it returns -1 to go on, else the exit code.
func parseAsk(fs *flag.FlagSet, args []string, nargs int) int {
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != nargs {
		fs.SetOutput(os.Stderr)
		fs.Usage()
		return 2
	}
	return -1
}

type askOptionList []string

func (l *askOptionList) String() string     { return strings.Join(*l, ", ") }
func (l *askOptionList) Set(v string) error { *l = append(*l, v); return nil }

// askExit maps an ask error to its exit code and prints its fixed message.
// No vault is exit 5 for the asker ("nothing can store it"), a refusal (2)
// on the dialog side.
func askExit(errOut io.Writer, prefix string, err error) int {
	switch {
	case errors.Is(err, ask.ErrNoVault) && prefix == "ask":
		fmt.Fprintf(errOut, "Error: %s: %v\n", prefix, err)
		return 5
	case ask.IsRefusal(err):
		fmt.Fprintf(errOut, "Error: %s: %v\n", prefix, err)
		return 2
	}
	fmt.Fprintf(errOut, "Error: %s: %v\n", prefix, err)
	return 1
}

func runAskRequest(profile string, kind ask.Kind, args []string, out, errOut io.Writer) int {
	positional := map[ask.Kind]string{ask.KindSecret: "<scope/NAME>", ask.KindChoice: `"question" --option A --option B [...]`, ask.KindText: `"question"`}[kind]
	fs := askFlags(string(kind), args, out, errOut, string(kind)+" "+positional+" [options]")
	identifier := fs.String("session", "", "asking session id or title (default: $AGENTDECK_INSTANCE_ID, then the calling tmux session)")
	reason := fs.String("reason", "", "why you ask, shown to the owner (at most 200 characters)")
	wait := fs.Duration("wait", ask.DefaultWait, "how long the request stays open (at most 30m0s)")
	fallbackAfter := fs.Duration("fallback-after", ask.DefaultFallbackAfter, "print the terminal fallback when no dialog showed the request by then")
	noFallback := fs.Bool("no-fallback", false, "exit 5 instead of the terminal fallback")
	jsonOut := fs.Bool("json", false, "print the result as JSON")
	var options askOptionList
	replace := new(bool)
	switch kind {
	case ask.KindChoice:
		fs.Var(&options, "option", "an option (repeat: 2 to 6)")
	case ask.KindSecret:
		replace = fs.Bool("replace", false, "the name exists in the vault: replace it (the owner is warned)")
	}
	if code := parseAsk(fs, args, 1); code >= 0 {
		return code
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask", err)
	}
	envID := os.Getenv("AGENTDECK_INSTANCE_ID")
	inst, err := resolveCallingSession(profile, *identifier, envID, currentTmuxSessionName)
	if err != nil {
		return askExit(errOut, "ask", &ask.Refusal{Msg: err.Error()})
	}
	verified, err := verifyAskCaller(profile, inst, envID)
	if err != nil {
		return askExit(errOut, "ask", err)
	}
	if kind == ask.KindSecret {
		if err := ask.CheckVault(); err != nil {
			return askExit(errOut, "ask", err)
		}
	}
	host, _ := os.Hostname()
	req := ask.NewRequest{
		Kind: kind, SessionID: inst.ID, SessionTitle: inst.Title, Host: host, Reason: *reason,
		CallerVerified: verified, Sandboxed: inst.Sandbox != nil, Wait: *wait, PID: os.Getpid(),
	}
	if kind == ask.KindSecret {
		req.Name, req.Replace = fs.Arg(0), *replace
	} else {
		req.Question, req.Options = fs.Arg(0), options
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	rec, err := ask.Ask(ctx, profile, req, ask.AwaitOptions{
		FallbackAfter: *fallbackAfter,
		NoFallback:    *noFallback,
		OnFallback:    func(r ask.Record) { fmt.Fprint(errOut, askFallbackText(profile, r)) },
		OnCreated: func(r ask.Record) {
			if r.Replace {
				fmt.Fprintf(errOut, "warning: this request REPLACES the existing secret %s\n", r.Name)
			}
		},
	})
	if errors.Is(err, ask.ErrNoUI) {
		fmt.Fprintf(errOut, "Error: ask: nothing showed this request within %s (--no-fallback)\n", *fallbackAfter)
		return 5
	}
	if err != nil {
		return askExit(errOut, "ask", err)
	}
	return finishAsk(profile, rec, *jsonOut, out, errOut)
}

// askFallbackText is printed once (stderr) when no dialog showed the
// request: the command the owner runs in his own terminal.
func askFallbackText(profile string, r ask.Record) string {
	cmd := "agent-deck ask answer " + r.RequestID
	if profile != session.DefaultProfile {
		cmd = "agent-deck -p " + profile + " ask answer " + r.RequestID
	}
	text := "No AgentDeck app or TUI is showing this request. In your own terminal run:\n  " + cmd + "\n"
	if r.Kind == ask.KindSecret && !r.Replace {
		text += "(or: deck-secret set " + r.Name + ")\n"
	}
	return text
}

type askOutcome struct {
	Status     ask.State `json:"status"`
	Name       string    `json:"name,omitempty"`
	Replaced   string    `json:"replaced,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"`
	Answer     *string   `json:"answer,omitempty"`
}

// finishAsk prints the asker's result: never a value, only stored / the
// choice or text answer / cancelled / timed out.
func finishAsk(profile string, rec ask.Record, jsonOut bool, out, errOut io.Writer) int {
	o := askOutcome{Status: rec.State, Name: rec.Name, AnsweredBy: rec.AnsweredBy}
	code := 0
	var line string
	switch rec.State {
	case ask.StateStored:
		o.Replaced = rec.Replaced
		line = fmt.Sprintf("stored %s (replaced=%s, by %s)", rec.Name, rec.Replaced, rec.AnsweredBy)
		if rec.AnsweredBy == "direct" {
			line = fmt.Sprintf("stored %s (direct deck-secret set, unverified)", rec.Name)
		}
	case ask.StateAnswered:
		answer := rec.Answer
		o.Answer = &answer
		line = answer
		defer func() { _ = ask.ClearAnswer(profile, rec.RequestID) }()
	case ask.StateCancelled:
		fmt.Fprintf(errOut, "cancelled by %s\n", rec.AnsweredBy)
		code = 3
	case ask.StateExpired:
		fmt.Fprintf(errOut, "timed out after %s\n", rec.ExpiresAt.Sub(rec.RequestedAt))
		code = 4
	default:
		fmt.Fprintf(errOut, "Error: ask: request ended in state %q\n", rec.State)
		return 1
	}
	if jsonOut {
		b, _ := json.Marshal(o)
		fmt.Fprintln(out, string(b))
	} else if code == 0 {
		fmt.Fprintln(out, line)
	}
	return code
}

// readDeliverInput reads the header line (at most deliverHeaderMax) and the
// value (at most MaxSecretBytes plus one trailing newline) from stdin. It
// reads the header a byte at a time so no value bytes sit in a buffer it
// cannot wipe; the caller clears the returned buffer.
func readDeliverInput(in io.Reader) (ask.Header, []byte, []byte, error) {
	var h ask.Header
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			line = append(line, one[0])
			if len(line) > deliverHeaderMax {
				return h, nil, nil, &ask.Refusal{Msg: "header line missing or over 4 KiB"}
			}
		}
		if err != nil {
			return h, nil, nil, &ask.Refusal{Msg: "header line missing or over 4 KiB"}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return h, nil, nil, &ask.Refusal{Msg: "header is not the JSON deliver header"}
	}
	buf := make([]byte, ask.MaxSecretBytes+2)
	n, err := io.ReadFull(in, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		clear(buf)
		return h, nil, nil, fmt.Errorf("read the value: %w", err)
	}
	if n == len(buf) || (n == ask.MaxSecretBytes+1 && buf[n-1] != '\n') {
		clear(buf)
		return h, nil, nil, ask.ErrTooLarge
	}
	return h, buf, buf[:n], nil
}

func runAskDeliver(profile string, args []string, in io.Reader, out, errOut io.Writer) int {
	_ = ask.Harden()
	fs := askFlags("deliver", args, out, errOut, "deliver <request_id>   (stdin: JSON header line, then the value)")
	if code := parseAsk(fs, args, 1); code >= 0 {
		return code
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask deliver", err)
	}
	h, buf, value, err := readDeliverInput(in)
	defer clear(buf)
	if err != nil {
		return askExit(errOut, "ask deliver", err)
	}
	if h.RequestID != fs.Arg(0) {
		return askExit(errOut, "ask deliver", ask.ErrHeaderMismatch)
	}
	res, err := ask.Deliver(profile, h, value)
	if err != nil {
		return askExit(errOut, "ask deliver", err)
	}
	b, _ := json.Marshal(struct {
		Status   ask.State `json:"status"`
		Replaced string    `json:"replaced"`
	}{res.Status, res.Replaced})
	fmt.Fprintln(out, string(b))
	return 0
}

func runAskCancel(profile string, args []string, out, errOut io.Writer) int {
	fs := askFlags("cancel", args, out, errOut, "cancel <request_id> [--by app|tui|terminal] (--nonce N | --unshown)")
	by := fs.String("by", "app", "who cancels: app, tui or terminal")
	nonce := fs.String("nonce", "", "the nonce the dialog announced in macapp.ask.seen (required)")
	unshown := fs.Bool("unshown", false, "cancel a request no dialog showed (refused once one did; noted in the record)")
	fs.Bool("json", true, "print the result as JSON (always on)")
	if code := parseAsk(fs, args, 1); code >= 0 {
		return code
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask cancel", err)
	}
	if *unshown && *nonce != "" {
		return askExit(errOut, "ask cancel", &ask.Refusal{Msg: "--unshown takes no --nonce"})
	}
	cancel := func() (ask.Result, error) { return ask.Cancel(profile, fs.Arg(0), *by, *nonce) }
	if *unshown {
		cancel = func() (ask.Result, error) { return ask.CancelUnshown(profile, fs.Arg(0), *by) }
	}
	if _, err := cancel(); err != nil {
		return askExit(errOut, "ask cancel", err)
	}
	fmt.Fprintln(out, `{"status":"cancelled"}`)
	return 0
}

func runAskShow(profile string, args []string, out, errOut io.Writer) int {
	fs := askFlags("show", args, out, errOut, "show <request_id> [--json]")
	jsonOut := fs.Bool("json", false, "print the record as JSON")
	if code := parseAsk(fs, args, 1); code >= 0 {
		return code
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask show", err)
	}
	rec, err := ask.Show(profile, fs.Arg(0))
	if err != nil {
		return askExit(errOut, "ask show", err)
	}
	if *jsonOut {
		b, _ := json.Marshal(rec)
		fmt.Fprintln(out, string(b))
		return 0
	}
	fmt.Fprint(out, askFacts(rec))
	return 0
}

func runAskList(profile string, args []string, out, errOut io.Writer) int {
	fs := askFlags("list", args, out, errOut, "list [--json] [--all]")
	jsonOut := fs.Bool("json", false, "print the records as JSON")
	all := fs.Bool("all", false, "include finished requests kept for 24 h")
	if code := parseAsk(fs, args, 0); code >= 0 {
		return code
	}
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask list", err)
	}
	list, err := ask.List(profile)
	if *all {
		list, err = ask.ListAll(profile)
	}
	if err != nil {
		return askExit(errOut, "ask list", err)
	}
	if *jsonOut {
		if list == nil {
			list = []ask.Record{}
		}
		b, _ := json.Marshal(list)
		fmt.Fprintln(out, string(b))
		return 0
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "No pending requests.")
		return 0
	}
	for _, r := range list {
		what := r.Name
		if r.Kind != ask.KindSecret {
			what = strconv.Quote(r.Question)
		}
		fmt.Fprintf(out, "%s  %-7s %-9s %s  %s (expires %s)\n", r.RequestID, r.Kind, r.State, truncateWidth(r.SessionTitle, 30), what, r.ExpiresAt.Local().Format(time.TimeOnly))
	}
	return 0
}

// askFacts are the host's facts about a request, then what the agent says.
func askFacts(r ask.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Request  %s (%s, %s)\n", r.RequestID, r.Kind, r.State)
	fmt.Fprintf(&b, "Host     %s  profile %s\n", r.Host, r.Profile)
	fmt.Fprintf(&b, "Session  %s (%s)\n", r.SessionTitle, r.SessionID)
	if !r.CallerVerified {
		b.WriteString("         Unverified caller (outside any agent-deck session)\n")
	}
	if r.Kind == ask.KindSecret {
		fmt.Fprintf(&b, "Name     %s\n", r.Name)
		if r.Replace {
			b.WriteString("         REPLACES an existing secret\n")
		}
	} else {
		fmt.Fprintf(&b, "Question %s\n", r.Question)
		for i, o := range r.Options {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, o)
		}
	}
	if r.Reason != "" {
		fmt.Fprintf(&b, "The agent says: %q\n", r.Reason)
	}
	fmt.Fprintf(&b, "Expires  %s\n", r.ExpiresAt.Local().Format(time.DateTime))
	return b.String()
}

// runAskAnswer is the terminal fallback for the owner: the facts, a hidden
// prompt (twice for a secret), then the same Deliver the dialogs use. It
// refuses unless stdin is a terminal, so an agent cannot pipe into it.
func runAskAnswer(profile string, args []string, in io.Reader, out, errOut io.Writer) int {
	_ = ask.Harden()
	fs := askFlags("answer", args, out, errOut, "answer <request_id>   (the owner, in a terminal)")
	if code := parseAsk(fs, args, 1); code >= 0 {
		return code
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(errOut, "Error: ask answer: stdin is not a terminal; this command is for the owner in a terminal")
		return 2
	}
	fd := int(f.Fd())
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return askExit(errOut, "ask answer", err)
	}
	rec, err := ask.Show(profile, fs.Arg(0))
	if err != nil {
		return askExit(errOut, "ask answer", err)
	}
	if rec.State != ask.StatePending {
		return askExit(errOut, "ask answer", ask.ErrNotPending)
	}
	fmt.Fprint(errOut, askFacts(rec))
	nonce, err := ask.MarkSeen(profile, rec.RequestID, "terminal")
	if err != nil {
		return askExit(errOut, "ask answer", err)
	}
	h := ask.Header{RequestID: rec.RequestID, Kind: rec.Kind, Name: rec.Name, Replace: rec.Replace, Nonce: nonce, AnsweredBy: "terminal"}
	var value []byte
	switch rec.Kind {
	case ask.KindSecret:
		fmt.Fprint(errOut, "Value (hidden, empty cancels): ")
		v1, err := term.ReadPassword(fd)
		fmt.Fprintln(errOut)
		if err != nil || len(v1) == 0 {
			clear(v1)
			return askCancelFromTerminal(profile, rec.RequestID, nonce, out, errOut)
		}
		fmt.Fprint(errOut, "Again: ")
		v2, err := term.ReadPassword(fd)
		fmt.Fprintln(errOut)
		same := err == nil && bytes.Equal(v1, v2)
		clear(v2)
		if !same {
			clear(v1)
			fmt.Fprintln(errOut, "Error: ask answer: the two values differ; nothing stored (run it again)")
			return 2
		}
		value = v1
	default:
		if rec.Kind == ask.KindText {
			fmt.Fprintln(errOut, "Your answer goes to the agent and its transcript. Never type a secret here.")
		}
		fmt.Fprint(errOut, "Answer (empty cancels): ")
		line, _ := bufio.NewReader(f).ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return askCancelFromTerminal(profile, rec.RequestID, nonce, out, errOut)
		}
		if n, err := strconv.Atoi(line); err == nil && rec.Kind == ask.KindChoice && n >= 1 && n <= len(rec.Options) {
			line = rec.Options[n-1]
		}
		value = []byte(line)
	}
	res, err := ask.Deliver(profile, h, value)
	if err != nil {
		return askExit(errOut, "ask answer", err)
	}
	if res.Status == ask.StateStored {
		fmt.Fprintf(out, "stored %s (replaced=%s)\n", rec.Name, res.Replaced)
	} else {
		fmt.Fprintln(out, "answered")
	}
	return 0
}

func askCancelFromTerminal(profile, id, nonce string, out, errOut io.Writer) int {
	if _, err := ask.Cancel(profile, id, "terminal", nonce); err != nil {
		return askExit(errOut, "ask answer", err)
	}
	fmt.Fprintln(out, "cancelled")
	return 3
}
