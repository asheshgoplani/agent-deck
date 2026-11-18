package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

func handleRecallTimeline(profile string, args []string) {
	fs := newRecallFlagSet("recall timeline")
	jsonOutput := fs.Bool("json", false, "Output canonical JSON")
	rf := registerRowsFlags(fs)
	since := fs.String("since", "", "Only what changed after this cursor: new rows in turns, changes to earlier rows in updates/removed")
	before := fs.String("before", "", "Return older rows before this before_cursor (requires --limit N)")
	limit := fs.Int("limit", 0, "Maximum older rows with --before; otherwise stop after N new rows and resume with --since")
	tail := fs.Int("tail", 0, "Only the last N rows, read from the end of the transcript (fast first paint)")
	agentID := fs.String("agent", "", "One Claude Code sub-agent sidechain by agent id")
	kinds := fs.String("kinds", "", "Comma-separated row kinds to keep (user, assistant, thinking, tool, bash, edit, read, subagent, todo, question, skill, command, system, compaction, turn_end, other); filtered here, before output, sub-agent children included. Native reads other than --agent resume with next_cursor")
	skipEmpty := fs.Bool("skip-empty-body", false, "Drop rows whose body is empty or only whitespace (a tool call still waiting for its result is kept); filtered here, before output, sub-agent children included. Native reads other than --agent resume with next_cursor")
	sessions := fs.String("sessions", "", fmt.Sprintf("Comma-separated session ids: one call answers each id's timeline (at most %d; only --tail, --kinds and --skip-empty-body combine with it)", recallTimelineBatchMax))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall timeline <session> --json [--since <cursor> | --before <cursor> --limit N] [--limit N] [--tail N] [--agent <id>] [--kinds a,b] [--skip-empty-body]")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline --json --transcript <file> --harness claude|codex|pi")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline --json [--tail N] [--kinds a,b] [--skip-empty-body] --sessions <id1>,<id2>,...   (batch: one entry per id, at most 50)")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline <session> --json --v1   (slice-6 turn shape)")
		fmt.Fprintln(fs.Output(), "<session>: deck session id, title or id prefix; a Claude, Codex or Pi conversation id; or #n / any id the index knows.")
		fmt.Fprintln(fs.Output(), "Rows are parsed from the native transcript directly, whatever the index or its deferral state. Needs [recall] enabled = true.")
		fmt.Fprintln(fs.Output(), "With --kinds or --skip-empty-body, --tail and --limit count kept rows. A native read then carries next_cursor: pass it to --since (never a cursor made from the last kept row). --agent sidechains and index-backed rows have no cursor.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	filter, err := timelineFilterFlags(fs, *kinds, *skipEmpty)
	if err != nil {
		NewCLIOutput(true, false).Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	batch := false
	fs.Visit(func(f *flag.Flag) { batch = batch || f.Name == "sessions" })
	if batch {
		handleRecallTimelineBatch(profile, fs, *sessions, *jsonOutput, *tail, filter)
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if !*jsonOutput || (fs.NArg() != 1 && *rf.transcript == "") {
		fs.Usage()
		exitCLI(2)
	}
	opts := query.RowsOptions{Since: *since, Before: *before, Limit: *limit, Tail: *tail, AgentID: *agentID, Kinds: filter.Kinds, SkipEmptyBody: filter.SkipEmptyBody}
	if err := query.ValidateRowsOptions(opts); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	if *rf.v1 && *before != "" {
		out.Error("recall timeline: --before is unsupported with --v1", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	if *rf.v1 && opts.Filtered() {
		out.Error("recall timeline: --kinds and --skip-empty-body filter rows and are unsupported with --v1", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	if !*rf.v1 {
		requireRecallEnabled(out)
		handleRecallTimelineRows(profile, fs.Arg(0), rf, opts)
		return
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	result, err := query.New(env.st, env.stateDB).Timeline(context.Background(), fs.Arg(0))
	if err != nil {
		out.Error(err.Error(), recallLookupCode(err))
		exitCLI(1)
	}
	out.printJSON(result)
}

// timelineFilterFlags reads --kinds and --skip-empty-body. A --kinds given
// empty is refused like an unknown kind, never treated as "no filter".
func timelineFilterFlags(fs *flag.FlagSet, kinds string, skipEmpty bool) (query.RowsOptions, error) {
	filter := query.RowsOptions{SkipEmptyBody: skipEmpty}
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "kinds" })
	if !set {
		return filter, nil
	}
	list, err := query.ParseRowKinds(kinds)
	if err != nil {
		return filter, fmt.Errorf("recall timeline: %s", strings.TrimPrefix(err.Error(), "recall rows: "))
	}
	filter.Kinds = list
	return filter, nil
}

func handleRecallFollow(profile string, args []string) {
	fs := newRecallFlagSet("recall follow")
	after := fs.String("after", "", "Resume cursor from timeline or a prior follow frame; 'end' starts at the current end")
	jsonl := fs.Bool("jsonl", false, "Stream newline-delimited JSON frames")
	status := fs.Bool("status", false, "Also emit status frames (verb, elapsed, tokens, current tool, footer facts) once a second while the session runs")
	rf := registerRowsFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall follow <session> --after <cursor|end> --jsonl [--status]")
		fmt.Fprintln(fs.Output(), "Frames: row, update, remove, status, delivery, resync_required (docs/recall-timeline.md). --v1 streams the slice-6 turn frames.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	if *after == "" || !*jsonl || (fs.NArg() != 1 && *rf.transcript == "") {
		fs.Usage()
		exitCLI(2)
	}
	out := NewCLIOutput(true, false)
	if !*rf.v1 {
		requireRecallEnabled(out)
		handleRecallFollowRows(profile, fs.Arg(0), *after, rf, *status)
		return
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	ctx, cancel := interruptibleContext()
	defer cancel()
	enc := json.NewEncoder(os.Stdout)
	err := query.New(env.st, env.stateDB).Follow(ctx, fs.Arg(0), *after, func(frame query.Frame) error {
		return enc.Encode(frame)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "recall follow:", err)
		exitCLI(1)
	}
}
