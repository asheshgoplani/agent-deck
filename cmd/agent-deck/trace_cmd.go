package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/trace"
)

const traceUsage = `Usage: agent-deck trace [session-id]       spans of one session (turn > tool calls)
       agent-deck trace                    list recent traced sessions
       agent-deck trace stats [--since 24h] [--tool claude|codex] [--json]
       agent-deck trace ingest [--json]    move the hook spool into trace.db
       agent-deck trace hook               trace-only hook receiver (reads a hook payload on stdin)

Agent activity tracing (ASK-129). Off by default: set [trace] level = "metadata"
or "summaries" in config.toml, or AGENTDECK_TRACE=metadata|summaries.
Arguments and results are never stored; summaries are shape-only and scrubbed.`

// traceLevel resolves the tracing level: AGENTDECK_TRACE wins; the config
// file is read only when the env is unset.
func traceLevel() trace.Level {
	if v := os.Getenv(trace.EnvLevel); strings.TrimSpace(v) != "" {
		return trace.ParseLevel(v)
	}
	return trace.ParseLevel(session.TraceLevelSetting())
}

// traceProfileDir is the data dir trace.db and the spool live under.
func traceProfileDir(profile string) (string, string, error) {
	p := session.GetEffectiveProfile(profile)
	dir, err := session.GetProfileDir(p)
	return p, dir, err
}

func traceHookContext(instanceID string, level trace.Level, at time.Time) trace.HookContext {
	host, _ := os.Hostname()
	return trace.HookContext{
		Tool:     strings.TrimSpace(os.Getenv("AGENTDECK_TOOL")),
		Instance: instanceID,
		Host:     host,
		At:       at,
		Level:    level,
	}
}

// spoolTraceFromHook is the hook handlers' only tracing work: normalize the
// payload (redacted) and append one line to the profile's spool. No SQLite,
// no locks, errors ignored, panics contained: tracing never fails a hook.
func spoolTraceFromHook(instanceID string, data []byte, at time.Time) {
	defer func() { _ = recover() }()
	level := traceLevel()
	if level == trace.LevelOff {
		return
	}
	ctx := traceHookContext(instanceID, level, at)
	profile, dir, err := traceProfileDir("")
	if err != nil {
		return
	}
	ctx.Profile = profile
	if rec, ok := trace.NormalizeHook(data, ctx); ok {
		_ = trace.AppendSpool(dir, rec)
	}
}

// spoolTraceFromCodexNotify records a codex-notify turn edge.
func spoolTraceFromCodexNotify(instanceID, event, sessionID, turnID string) {
	defer func() { _ = recover() }()
	level := traceLevel()
	if level == trace.LevelOff {
		return
	}
	started, completed := codexTurnEdge(event)
	ctx := traceHookContext(instanceID, level, time.Now())
	ctx.Tool = "codex"
	profile, dir, err := traceProfileDir("")
	if err != nil {
		return
	}
	ctx.Profile = profile
	if rec, ok := trace.NormalizeCodexNotify(event, sessionID, turnID, started, completed, ctx); ok {
		_ = trace.AppendSpool(dir, rec)
	}
}

func handleTrace(profile string, args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h", "help":
			fmt.Fprintln(os.Stdout, traceUsage)
			markCLINoop()
			return
		case "stats":
			handleTraceStats(profile, args[1:])
			return
		case "ingest":
			handleTraceIngest(profile, args[1:])
			return
		case "hook":
			if helpRequested(args[1:]) {
				fmt.Fprintln(os.Stdout, traceUsage)
				return
			}
			handleTraceHook()
			return
		}
	}
	handleTraceShow(profile, args)
}

// handleTraceHook is a trace-only hook receiver: it records the payload and
// touches no status file, so it can be subscribed to edges the status hook
// must not see (Claude PostToolUse) without changing session status.
func handleTraceHook() {
	instanceID := os.Getenv("AGENTDECK_INSTANCE_ID")
	if instanceID == "" || !validInstanceID.MatchString(instanceID) || strings.Contains(instanceID, "..") {
		return
	}
	at := time.Now()
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxHookPayloadSize))
	if err != nil || len(data) == 0 {
		return
	}
	spoolTraceFromHook(instanceID, data, at)
}

// openTraceStoreIngested opens trace.db and first ingests the spool so the
// view is current.
func openTraceStoreIngested(profile string) (*trace.Store, error) {
	_, dir, err := traceProfileDir(profile)
	if err != nil {
		return nil, err
	}
	st, err := trace.Open(trace.DBPath(dir))
	if err != nil {
		return nil, err
	}
	if _, err := st.Ingest(dir, trace.IngestOptions{}); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func handleTraceIngest(profile string, args []string) {
	fs := flag.NewFlagSet("trace ingest", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), traceUsage) }
	_ = parseCLIFlags(fs, args)
	_, dir, err := traceProfileDir(profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	st, err := trace.Open(trace.DBPath(dir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	defer st.Close()
	res, err := st.Ingest(dir, trace.IngestOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(res)
		return
	}
	fmt.Printf("ingested %d record(s) from %d spool file(s): %d new, %d duplicate, %d bad line(s)\n",
		res.Lines, res.Files, res.Applied, res.Duplicate, res.Bad)
}

func handleTraceShow(profile string, args []string) {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	limit := fs.Int("limit", 20, "Traces to list when no session is given")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), traceUsage) }
	_ = parseCLIFlags(fs, args)
	st, err := openTraceStoreIngested(profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	defer st.Close()
	match := strings.TrimSpace(fs.Arg(0))
	traces, err := st.Traces(match, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	if match == "" {
		if *jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(traces)
			return
		}
		if len(traces) == 0 {
			fmt.Printf("No traces (tracing level: %s).\n", traceLevel())
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "TRACE\tTOOL\tINSTANCE\tSTARTED\tSPANS")
		for _, t := range traces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", t.TraceID, t.Tool, t.Instance, fmtMillis(t.StartedAt), t.Spans)
		}
		tw.Flush()
		return
	}
	if len(traces) == 0 {
		fmt.Fprintf(os.Stderr, "No trace matches %q.\n", match)
		exitCLI(1)
	}
	type out struct {
		Trace trace.TraceRow `json:"trace"`
		Spans []*trace.Span  `json:"spans"`
	}
	var all []out
	for _, t := range traces {
		tree, err := st.Tree(t.TraceID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			exitCLI(1)
		}
		all = append(all, out{t, tree})
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(all)
		return
	}
	for _, o := range all {
		fmt.Printf("%s  (%s, instance %s, started %s)\n", o.Trace.TraceID, o.Trace.Tool, o.Trace.Instance, fmtMillis(o.Trace.StartedAt))
		printSpans(os.Stdout, o.Spans, "  ")
	}
}

func printSpans(w io.Writer, spans []*trace.Span, indent string) {
	for _, sp := range spans {
		dur := "open"
		if sp.EndedAt != 0 && sp.DurationMS >= 0 {
			dur = (time.Duration(sp.DurationMS) * time.Millisecond).String()
			if sp.Inferred {
				dur = "~" + dur
			}
		}
		name := sp.Name
		if sp.Kind != trace.SpanToolCall {
			name = sp.Kind
			if sp.Kind == trace.SpanWait && sp.Name != "" {
				name += " " + sp.Name
			}
		}
		line := fmt.Sprintf("%s%s  %s  %s  %s", indent, fmtMillis(sp.StartedAt), name, dur, sp.Status)
		if sp.Summary != "" {
			line += "  " + sp.Summary
		}
		fmt.Fprintln(w, line)
		printSpans(w, sp.Children, indent+"  ")
	}
}

func fmtMillis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

func handleTraceStats(profile string, args []string) {
	fs := flag.NewFlagSet("trace stats", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "Output as JSON")
	since := fs.Duration("since", 0, "Only spans started within this window (e.g. 24h)")
	tool := fs.String("tool", "", "Only this agent tool (claude, codex)")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), traceUsage) }
	_ = parseCLIFlags(fs, args)
	st, err := openTraceStoreIngested(profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	defer st.Close()
	opts := trace.StatsOptions{Tool: *tool}
	if *since > 0 {
		opts.Since = time.Now().Add(-*since)
	}
	stats, err := st.Stats(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		exitCLI(1)
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(stats)
		return
	}
	if len(stats) == 0 {
		fmt.Printf("No tool calls traced (tracing level: %s).\n", traceLevel())
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tCALLS\tTOTAL\tP95\tMAX\tERRORS\tERROR%\tINFERRED")
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%d\t%.1f\t%d\n", s.Tool, s.Count,
			time.Duration(s.TotalMS)*time.Millisecond, time.Duration(s.P95MS)*time.Millisecond,
			time.Duration(s.MaxMS)*time.Millisecond, s.Errors, 100*s.ErrorRate, s.Inferred)
	}
	tw.Flush()
}
