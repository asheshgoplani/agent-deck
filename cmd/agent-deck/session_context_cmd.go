package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/registry"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/sessionhost"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/verify"
)

// Exit codes for `session context`. Producing an honest report is a success
// even when the report says the harness cannot be measured, so an unsupported
// tool exits 0 with a populated inventory. --strict is the opt-in that turns a
// self-contradicting report into a failing exit for CI.
// Every distinguishable outcome has its own code, because a gate that cannot
// tell them apart silently downgrades to "did it crash?":
//
//	0  the command did what it was asked; --verify additionally graded at
//	   least one group and every graded group agreed within tolerance
//	1  the command could not do its job (bad arguments, no live pane, an
//	   unreadable panel) — nothing was measured or compared
//	2  no session matched the reference
//	3  --strict: the report contradicts itself or fails an invariant
//	4  --verify: a comparable group disagreed with the harness's own
//	   accounting beyond tolerance — we checked and we were wrong
//	5  --verify: nothing could be graded — we checked and learned nothing
const (
	contextExitOK           = 0
	contextExitError        = 1
	contextExitNotFound     = 2
	contextExitUnreconciled = 3
	// contextExitDrift is returned by --verify when a comparable group
	// disagrees with the harness's own accounting beyond tolerance. It is its
	// own code so CI can distinguish "we could not check" (1) from "we checked
	// and we were wrong" (4).
	contextExitDrift = 4
	// contextExitIndeterminate is returned by --verify when the panel was read
	// but no group could be graded — every row came back harness-silent or
	// ours-unknown, so no agreement is claimed about anything.
	//
	// It is not 0. A gate that reads 0 as "verified" would take the one outcome
	// that proves nothing and record it as a pass, which is precisely the
	// plausible-looking lie this command exists to make impossible. It is not 4
	// either: nothing disagreed, so failing it as drift would be a different
	// lie in the other direction.
	contextExitIndeterminate = 5
)

// Tab names accepted by --tab.
const (
	contextTabOverview  = "overview"
	contextTabBreakdown = "breakdown"
	contextTabVerify    = "verify"
)

// defaultContextTimeout bounds inspection. Everything on the path is a bounded
// head-read of a local file, so this is a backstop against a pathological or
// networked filesystem rather than an expected wait.
const defaultContextTimeout = 20 * time.Second

// defaultVerifyTimeout bounds how long --verify waits for the harness's own
// panel to render. It is longer than the inspection budget because the wait is
// on a live agent redrawing its TUI, not on a local file read.
const defaultVerifyTimeout = 30 * time.Second

// handleSessionContext implements `agent-deck session context`.
//
// It resolves the session by agent-deck's usual title / id-prefix / path rules,
// builds a request with the per-harness paths the adapter must not resolve for
// itself, dispatches through the adapter registry, and renders. The engine is
// fully exercisable from here: the TUI is a second renderer over the same
// report, never a second implementation.
func handleSessionContext(profile string, args []string) {
	fs := flag.NewFlagSet("session context", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Emit the report as JSON (stable schema; carries provenance on every figure)")
	quiet := fs.Bool("quiet", false, "One-line summary: the gauge figure and how many items you can act on")
	quietShort := fs.Bool("q", false, "One-line summary (short for --quiet)")
	verbose := fs.Bool("verbose", false, "Include the per-category notes explaining how each figure was obtained")
	tab := fs.String("tab", contextTabOverview, "Which view to print: overview, breakdown or verify")
	item := fs.String("item", "", "Print one item's provenance, lever and verbatim text (id from --tab breakdown)")
	all := fs.Bool("all", false, "Breakdown: print every item instead of the ranked top")
	capsOnly := fs.Bool("capabilities", false, "Print what this harness can report, without inspecting anything")
	glossary := fs.Bool("glossary", false, "Define every word these screens use — adapter, basis, anchor, residual, CAPT/RECON/ABSENT — and exit")
	strict := fs.Bool("strict", false, "Exit 3 when the report fails its own reconciliation or breaks an invariant")
	timeout := fs.Duration("timeout", defaultContextTimeout, "Abort inspection after this long")
	verifyLive := fs.Bool("verify", false, "Compare against the harness's own accounting. MUTATES the live session: sends /context (Claude) or /status (Codex) and reads the panel back")
	verifyYes := fs.Bool("yes", false, "Skip the --verify confirmation prompt (for CI; the default always asks before typing into a session)")
	verifyTimeout := fs.Duration("verify-timeout", defaultVerifyTimeout, "How long to wait for the harness's own panel to render")
	verifyReadyWait := fs.Duration("verify-ready-timeout", defaultContextVerifyReadyWait, "--verify: how long to wait for the agent to go idle before typing into it")
	tolTokens := fs.Int("tolerance-tokens", verify.DefaultTolerance().AbsTokens, "--verify: absolute tolerance floor, in tokens")
	tolPct := fs.Float64("tolerance-pct", verify.DefaultTolerance().Pct, "--verify: relative tolerance, as a percentage of the harness figure")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session context [id|title] [options]")
		fmt.Println()
		fmt.Println("Show everything that is being passed to the agent for a session: the instruction-file")
		fmt.Println("hierarchy, skills, agents, tool schemas and MCP servers, ranked by what each costs.")
		fmt.Println()
		fmt.Println("This measures the FIXED STARTUP OVERHEAD — the bytes loaded into every turn, which is")
		fmt.Println("the part you can clean up. It does not measure your conversation, which is what")
		fmt.Println("usually fills a context window.")
		fmt.Println()
		fmt.Println("Every figure states where it came from. A number the harness measured, a number")
		fmt.Println("agent-deck estimated and a number that does not exist are rendered differently, and")
		fmt.Println("an unknown is never printed as zero. Harnesses with no token accounting print the")
		fmt.Println("inventory with '—' for every figure and exit 0.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session context my-project                  # overview")
		fmt.Println("  agent-deck session context my-project --tab breakdown  # what to remove, and how")
		// Item ids are the identifiers the report prints, and for a memory file
		// that is the absolute path. The old example, memory:0, matched nothing
		// on any of 7,693 real item ids — a copy-pasteable line that always
		// exits 2 is worse documentation than none.
		fmt.Println("  agent-deck session context my-project --item memory:/path/to/CLAUDE.md")
		fmt.Println("                                                        # one item's text (ids come from --tab breakdown)")
		fmt.Println("  agent-deck session context my-project --tab verify     # the arithmetic")
		fmt.Println("  agent-deck session context my-project --json | jq .    # full report")
		fmt.Println("  agent-deck session context --glossary                  # what adapter, basis, anchor, CAPT, RECON… mean")
		fmt.Println("  agent-deck session context my-project --capabilities   # what this harness can tell you")
		fmt.Println("  agent-deck session context my-project --verify         # diff against the harness's own /context")
		fmt.Println()
		fmt.Println("--verify is the only part of this command that writes anything: it types the harness's own")
		fmt.Println("accounting command into the live session and reads the panel back, so it asks first. It waits")
		fmt.Println("for the agent to go idle and moves an unsent draft out of the composer, but the command still")
		fmt.Println("joins that conversation for good. The context hotkey in the TUI never does this.")
		fmt.Println()
		fmt.Println("Exit codes:")
		fmt.Println("  0  done; with --verify, every graded group agreed within tolerance")
		fmt.Println("  1  could not run (bad arguments, no live pane, unreadable panel) — nothing was compared")
		fmt.Println("  2  no session matched")
		fmt.Println("  3  --strict: the report failed its own reconciliation or an invariant")
		fmt.Println("  4  --verify: a group disagreed with the harness beyond tolerance")
		fmt.Println("  5  --verify: nothing could be graded, so no agreement is claimed (never read this as a pass)")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(contextExitError)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	// Before anything else, including session resolution: the glossary is the
	// screen you reach for when you do not yet know what you are looking at, so
	// requiring a resolvable session to read it would gate the definitions
	// behind the vocabulary they define.
	if *glossary {
		printContextGlossary(out)
		os.Exit(contextExitOK)
	}

	tabName := strings.ToLower(strings.TrimSpace(*tab))
	switch tabName {
	case contextTabOverview, contextTabBreakdown, contextTabVerify:
	default:
		out.Error(fmt.Sprintf("unknown --tab %q: expected overview, breakdown or verify", *tab), ErrCodeInvalidOperation)
		os.Exit(contextExitError)
	}

	identifier := fs.Arg(0)

	reg := registry.Default()
	host := sessionhost.New()

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(contextExitError)
	}

	inst, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if inst == nil {
		// --capabilities is documented as answerable "without inspecting
		// anything", and it inspects nothing — so requiring a resolvable
		// session to answer it was the command contradicting its own help. With
		// a --tool it answers for that harness; with neither it lists every
		// adapter, which is the honest reading of "what can this thing report".
		if *capsOnly {
			printContextCapabilityCatalogue(out, reg, host, profile)
			os.Exit(contextExitOK)
		}
		out.Error(contextResolutionHint(errMsg, errCode, identifier, profile), errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(contextExitNotFound)
		}
		os.Exit(contextExitError)
	}

	if *capsOnly {
		caps, capsErr := reg.Capabilities(inst.Tool, host)
		if capsErr != nil {
			// No adapter at all, not even the fallback: say so plainly rather
			// than inventing a screen. This is honest degradation, not failure.
			printContextUnsupported(out, inst.Tool, capsErr)
			os.Exit(contextExitOK)
		}
		view := contextCapabilitiesView(identifier, inst.Title, profile, inst.Tool)
		out.Print(renderContextCapabilities(caps, inst.Tool), buildContextCapabilitiesJSON(view, caps))
		os.Exit(contextExitOK)
	}

	req, warnings, err := sessionhost.BuildRequest(inst, instances, sessionhost.RequestOptions{SessionRef: contextSessionRef(identifier, inst.Title)})
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(contextExitError)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	rep, err := reg.Inspect(ctx, req)
	if err != nil {
		if errors.Is(err, ctxinspect.ErrUnsupported) {
			printContextUnsupported(out, inst.Tool, err)
			os.Exit(contextExitOK)
		}
		// A parse failure, an unreadable transcript: a real failure, reported
		// as one. It must never degrade into a report full of zeroes.
		out.Error(fmt.Sprintf("inspecting the context of session %q: %v", inst.Title, err), ErrCodeInvalidOperation)
		os.Exit(contextExitError)
	}

	view := contextView{
		Ref:      identifier,
		Title:    inst.Title,
		Profile:  profile,
		Tool:     inst.Tool,
		Report:   rep,
		Warnings: warnings,
		Verbose:  *verbose,
	}

	// A command whose entire product is a report must not answer -q with zero
	// bytes: silence and a crash look identical from a shell. The one line it
	// prints is the one a human would read out.
	if quietMode && !*jsonOutput && strings.TrimSpace(*item) == "" && !*verifyLive {
		fmt.Println(renderContextQuiet(view))
		os.Exit(contextExitStatus(rep, *strict))
	}

	if *verifyLive {
		os.Exit(runContextVerify(out, view, contextLivePane(inst), contextVerifyOptions{
			Yes:       *verifyYes,
			Timeout:   *verifyTimeout,
			ReadyWait: *verifyReadyWait,
			Tolerance: verify.Tolerance{AbsTokens: *tolTokens, Pct: *tolPct},
		}, *jsonOutput))
	}

	if strings.TrimSpace(*item) != "" {
		ri, findErr := findContextItem(rep, *item)
		if findErr != nil {
			out.Error(findErr.Error(), ErrCodeNotFound)
			os.Exit(contextExitNotFound)
		}
		out.Print(renderContextItem(view, ri), buildContextItemJSON(view, ri))
		os.Exit(contextExitStatus(rep, *strict))
	}

	if *jsonOutput {
		// The tab selects a human view; the JSON document is always the whole
		// report, so a consumer never has to know which tab produced it.
		out.Print("", buildContextJSON(view))
		os.Exit(contextExitStatus(rep, *strict))
	}

	switch tabName {
	case contextTabBreakdown:
		out.Print(renderContextBreakdown(view, *all), nil)
	case contextTabVerify:
		out.Print(renderContextVerify(view), nil)
	default:
		out.Print(renderContextOverview(view), nil)
	}
	os.Exit(contextExitStatus(rep, *strict))
}

// contextExitStatus maps a produced report to an exit code.
//
// Producing a report is a success: an honest "this harness cannot be measured"
// is the feature working, not failing. --strict opts into a failing exit when
// the report contradicts itself, which is the form CI wants.
func contextExitStatus(rep *ctxinspect.Report, strict bool) int {
	if !strict {
		return contextExitOK
	}
	if len(rep.Violations) > 0 || rep.Reconciliation.Status == ctxinspect.ReconFailed {
		return contextExitUnreconciled
	}
	return contextExitOK
}

// printContextGlossary defines the vocabulary these screens use.
//
// It exists because a reviewer met adapter, basis, anchor, projected, residual,
// unattributed remainder, reconciliation, CAPT, RECON, ABSENT, POTENTIAL and 🔒
// on one screen, and not one of them was defined anywhere. The column legend on
// the report covers the codes that sit in a table cell; the concepts need more
// room than a report has, so they live one flag away.
func printContextGlossary(out *CLIOutput) {
	var b strings.Builder
	for _, line := range ctxtext.GlossaryLines() {
		b.WriteString(line + "\n")
	}
	terms := ctxtext.Glossary()
	doc := make([]map[string]string, 0, len(terms))
	for _, t := range terms {
		doc = append(doc, map[string]string{"term": t.Term, "definition": t.Def})
	}
	out.Print(b.String(), map[string]interface{}{
		"schema_version": contextSchemaVersion,
		"glossary":       doc,
	})
}

// printContextCapabilityCatalogue answers "what can this command tell me"
// without a session, by listing what every registered adapter declares.
func printContextCapabilityCatalogue(out *CLIOutput, reg *ctxinspect.Registry, host ctxinspect.Host, profile string) {
	adapters := reg.Adapters()
	var human strings.Builder
	human.WriteString("no session was named, so this is what each adapter can report for the harness it claims.\n")
	human.WriteString("name a session to see what it can report for that one: agent-deck session context <id> --capabilities\n\n")

	docs := make([]map[string]interface{}, 0, len(adapters))
	for _, a := range adapters {
		caps := a.Capabilities()
		human.WriteString(renderContextCapabilities(caps, a.Name()))
		human.WriteString("\n")
		docs = append(docs, map[string]interface{}{"adapter": a.Name(), "capabilities": caps})
	}
	if len(adapters) == 0 {
		human.WriteString("no adapters are registered in this build.\n")
	}
	out.Print(human.String(), map[string]interface{}{
		"schema_version": contextSchemaVersion,
		"profile":        profile,
		"adapters":       docs,
	})
}

// contextResolutionHint turns an unresolved reference into something a reader
// can act on: what the command tried, and what to pass instead.
//
// With no argument the command adopts the session id embedded in the name of
// the tmux session it is running inside. When that lookup fails the raw error
// reads `session "72aad8b2" not found` — a hex id the user never chose, never
// typed and cannot look up, describing an input they did not know existed. That
// is the first thing a cold run meets, and it is the worst possible first
// sentence: it looks like the tool is broken rather than unaimed.
func contextResolutionHint(msg, code, identifier, profile string) string {
	if code != ErrCodeNotFound {
		return msg
	}
	next := "Run 'agent-deck list' to see the sessions it knows, then name one: agent-deck session context <title>"
	if strings.TrimSpace(identifier) != "" {
		return msg + ". " + next
	}
	scope := "this profile"
	if p := strings.TrimSpace(profile); p != "" {
		scope = fmt.Sprintf("profile %q", p)
	}
	if detected := GetCurrentSessionID(); detected != "" {
		return fmt.Sprintf("no session was named, so agent-deck inspected the one it is running inside: it read the id %q out of the tmux session name (agentdeck_<title>_%s), and %s has no session with that id. %s",
			detected, detected, scope, next)
	}
	return fmt.Sprintf("no session was named, and this shell is not inside an agent-deck session, so there was nothing to auto-detect. %s", next)
}

// printContextUnsupported reports a tool with no adapter at all. The caller
// exits 0: there is nothing wrong with the request, agent-deck simply has
// nothing true to say about this tool, and saying so is the correct answer.
func printContextUnsupported(out *CLIOutput, tool string, cause error) {
	human := fmt.Sprintf("token accounting unsupported for %s: no context-inspection adapter claims this tool, and agent-deck will not guess a number.\n", displayTool(tool))
	out.Print(human, map[string]interface{}{
		"schema_version": contextSchemaVersion,
		"supported":      false,
		"tool":           tool,
		"reason":         cause.Error(),
	})
}

// displayTool renders a tool name for a sentence, naming an empty one rather
// than leaving a gap.
func displayTool(tool string) string {
	if strings.TrimSpace(tool) == "" {
		return "(this session records no tool)"
	}
	return fmt.Sprintf("%q", tool)
}

// contextSessionRef picks the name to interpolate into command levers: what the
// user typed, else the title they would type.
func contextSessionRef(identifier, title string) string {
	if v := strings.TrimSpace(identifier); v != "" {
		return v
	}
	return strings.TrimSpace(title)
}
