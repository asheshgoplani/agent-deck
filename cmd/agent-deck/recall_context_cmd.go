package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall/enrich"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
)

// Recall phase 4 (docs/recall.md): `recall context` hands a found
// conversation to a session, `recall enrich` drains the cheap classifiers.

// ---- context -------------------------------------------------------------

// intoCurrent is the --into value meaning "the session this command runs
// in", resolved from AGENTDECK_INSTANCE_ID, which agent-deck exports into
// every session it starts.
const intoCurrent = "current"

func handleRecallContext(profile string, args []string) {
	fs := newRecallFlagSet("recall context")
	jsonOutput := fs.Bool("json", false, "Output as JSON (never with --into)")
	tier := fs.String("tier", query.TierExcerpt, "card (about 60 tokens), brief (card + derived + files, about 300) or excerpt (brief + the newest turns under --budget)")
	budget := fs.Int("budget", query.DefaultContextBudget, "Token budget for the excerpt tier")
	into := fs.String("into", "", "Deliver the text to a session as a prompt: 'current' (the session this runs in) or a session id/title; otherwise print it")
	noWait := fs.Bool("no-wait", false, "With --into: send without waiting for the target to be ready")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall context <session> [--tier card|brief|excerpt] [--budget 4000] [--into current|<session>] [--json]

Render one past conversation as plain text a session of any harness can take as
context: the card, the derived summary (stale ones marked), the touched files
and, at the excerpt tier, the newest turns that fit the budget. --into current
delivers it to the calling session (a Codex session running this from its shell
receives a Claude conversation in its own prompt); --into <session> to another
one. Delivery uses 'session send'. A card pulled from another machine stops at
the brief tier.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	if *into != "" && *jsonOutput {
		out.Error("--json and --into cannot be combined: the delivery result is the output", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	target, err := resolveIntoTarget(*into)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	res, err := query.New(env.st, env.stateDB).Context(context.Background(), fs.Arg(0), *tier, *budget)
	env.close()
	if err != nil {
		code := recallLookupCode(err)
		out.Error(err.Error(), code)
		if errors.Is(err, query.ErrNotFound) || errors.Is(err, query.ErrTier) || errors.Is(err, query.ErrDigestOnly) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if target == "" {
		if *jsonOutput {
			out.printJSON(map[string]any{"success": true, "context": res})
			return
		}
		fmt.Print(res.Text)
		return
	}
	// The rendered text is the message; session send owns readiness,
	// the composer guard, the transport choice (tmux keystrokes, or the
	// Claude socket when opted in) and the delivery verdict.
	turns := ""
	if res.Tier == query.TierExcerpt {
		turns = fmt.Sprintf(", %d of %d messages", res.Included, res.Messages)
	}
	fmt.Fprintf(os.Stderr, "recall context: %s tier, %d chars%s -> session %s\n", res.Tier, res.Chars, turns, target)
	sendArgs := []string{}
	if *noWait {
		sendArgs = append(sendArgs, "--no-wait")
	}
	sendArgs = append(sendArgs, "--", target, res.Text)
	handleSessionSend(profile, sendArgs)
}

// resolveIntoTarget turns --into into a session reference: "" for print,
// AGENTDECK_INSTANCE_ID for current, else the value itself.
func resolveIntoTarget(into string) (string, error) {
	into = strings.TrimSpace(into)
	if into != intoCurrent {
		return into, nil
	}
	id := strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
	if id == "" {
		return "", errors.New("--into current needs AGENTDECK_INSTANCE_ID (set inside every session agent-deck starts); outside one, name the session: --into <id|title>")
	}
	return id, nil
}

// ---- enrich --------------------------------------------------------------

func handleRecallEnrich(profile string, args []string) {
	fs := newRecallFlagSet("recall enrich")
	jsonOutput, force, _ := recallBatchFlags(fs)
	costClass := fs.String("cost-class", enrich.CostCheap, "Queue rows to drain: cheap (the rules classifiers); llm is never drained automatically")
	kinds := fs.String("kind", "", "Only these kinds, comma-separated ("+strings.Join(enrich.CheapKinds, ", ")+")")
	limit := fs.Int("limit", 0, "Queue rows to process in this run (0: every pending row)")
	budget := fs.Duration("budget", 0, "Stop after this long (the rest waits for the next run)")
	retryFailed := fs.Bool("retry-failed", false, "Put rows that failed too often back in the queue first")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall enrich [--cost-class cheap] [--kind lost_time,session_kind,outcome] [--limit N] [--budget 30s] [--force] [--json]

Drain the enrichment queue: run the rules classifiers ("where did we lose
time", session kind, outcome) over the indexed rows of every session whose
content or hints changed, and write the derived artifacts recall show and
recall context print. Same load gate as the sweep. The sweep already drains
the cheap class within its budget; this runs the rest, or reruns after a
rules change.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	var kindList []string
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kindList = append(kindList, k)
		}
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	release := env.lock(out)
	defer release()
	opts := enrich.Options{CostClass: *costClass, Kinds: kindList, Limit: *limit}
	if *budget > 0 {
		opts.Budget = reader.NewBudget(*budget, 0)
	}
	if !*force {
		opts.Gate = env.ingestOptions(false).Gate
	}
	d := enrich.New(env.st, opts)
	retried := 0
	if *retryFailed {
		n, err := d.RetryFailed()
		if err != nil {
			out.Error("recall enrich: "+err.Error(), ErrCodeInvalidOperation)
			os.Exit(1)
		}
		retried = n
	}
	ctx, cancel := interruptibleContext()
	defer cancel()
	res, err := d.Drain(ctx)
	if err != nil {
		if errors.Is(err, ingest.ErrGated) {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(3)
		}
		out.Error("recall enrich: "+err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "result": res, "retried": retried})
		return
	}
	fmt.Printf("enrich done: %d pending, %d processed, %d artifact(s) written, %d failed, %d left for the next run, %.1fs\n",
		res.Pending, res.Processed, res.Written, res.Failed, res.Deferred, float64(res.ElapsedMS)/1000)
	if retried > 0 {
		fmt.Printf("  %d failed row(s) were put back in the queue first\n", retried)
	}
}
