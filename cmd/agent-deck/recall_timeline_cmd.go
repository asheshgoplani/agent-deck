package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

func handleRecallTimeline(profile string, args []string) {
	fs := newRecallFlagSet("recall timeline")
	jsonOutput := fs.Bool("json", false, "Output canonical JSON")
	rf := registerRowsFlags(fs)
	since := fs.String("since", "", "Only what changed after this cursor: new rows in turns, changes to earlier rows in updates/removed")
	limit := fs.Int("limit", 0, "Stop after N rows; through_cursor points there, so --since pages forward")
	tail := fs.Int("tail", 0, "Only the last N rows, read from the end of the transcript (fast first paint)")
	agentID := fs.String("agent", "", "One Claude Code sub-agent sidechain by agent id")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall timeline <session> --json [--since <cursor>] [--limit N] [--tail N] [--agent <id>]")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline --json --transcript <file> --harness claude|codex")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline <session> --json --v1   (slice-6 turn shape)")
		fmt.Fprintln(fs.Output(), "<session>: deck session id, title or id prefix; a Claude/Codex conversation id; or #n / any id the index knows.")
		fmt.Fprintln(fs.Output(), "Rows are parsed from the native transcript directly, whatever the index or its deferral state. Needs [recall] enabled = true.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if !*jsonOutput || (fs.NArg() != 1 && *rf.transcript == "") {
		fs.Usage()
		os.Exit(2)
	}
	if !*rf.v1 {
		requireRecallEnabled(out)
		handleRecallTimelineRows(profile, fs.Arg(0), rf, query.RowsOptions{Since: *since, Limit: *limit, Tail: *tail, AgentID: *agentID})
		return
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	result, err := query.New(env.st, env.stateDB).Timeline(context.Background(), fs.Arg(0))
	if err != nil {
		out.Error(err.Error(), recallLookupCode(err))
		os.Exit(1)
	}
	out.printJSON(result)
}

func handleRecallFollow(profile string, args []string) {
	fs := newRecallFlagSet("recall follow")
	after := fs.String("after", "", "Resume cursor from timeline or a prior follow frame; 'end' starts at the current end")
	jsonl := fs.Bool("jsonl", false, "Stream newline-delimited JSON frames")
	status := fs.Bool("status", false, "Also emit status frames (verb, elapsed, tokens, current tool, footer facts) once a second while the session runs")
	rf := registerRowsFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall follow <session> --after <cursor|end> --jsonl [--status]")
		fmt.Fprintln(fs.Output(), "Frames: row, update, remove, status, resync_required (docs/recall-timeline.md). --v1 streams the slice-6 turn frames.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	if *after == "" || !*jsonl || (fs.NArg() != 1 && *rf.transcript == "") {
		fs.Usage()
		os.Exit(2)
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
		os.Exit(1)
	}
}
