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
	tailBytes := fs.Int64("tail-bytes", 0, "With --rows: parse only the last N bytes (first line boundary after it) for a fast first paint")
	agentID := fs.String("agent", "", "With --rows: return one Claude Code sub-agent sidechain by agent id")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall timeline <session> --json [--rows [--tail-bytes N] [--agent <id>]]")
		fmt.Fprintln(fs.Output(), "       agent-deck recall timeline --rows --json --transcript <file> --harness claude|codex")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	if *rf.rows {
		if !*jsonOutput || (fs.NArg() != 1 && *rf.transcript == "") {
			fs.Usage()
			os.Exit(2)
		}
		handleRecallTimelineRows(profile, fs.Arg(0), rf, *tailBytes, *agentID)
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 || !*jsonOutput {
		fs.Usage()
		os.Exit(2)
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
	after := fs.String("after", "", "Resume cursor from timeline or a prior follow frame")
	jsonl := fs.Bool("jsonl", false, "Stream newline-delimited JSON frames")
	rf := registerRowsFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall follow <session> --after <cursor> --jsonl [--rows]")
		fmt.Fprintln(fs.Output(), "       with --rows, --after also accepts 'end'; frames are row, update, remove, status, resync_required")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	if *rf.rows {
		if *after == "" || !*jsonl || (fs.NArg() != 1 && *rf.transcript == "") {
			fs.Usage()
			os.Exit(2)
		}
		handleRecallFollowRows(profile, fs.Arg(0), *after, rf)
		return
	}
	if fs.NArg() != 1 || *after == "" || !*jsonl {
		fs.Usage()
		os.Exit(2)
	}
	out := NewCLIOutput(true, false)
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
