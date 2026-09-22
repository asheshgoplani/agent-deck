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
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall timeline <session> --json")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
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
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall follow <session> --after <cursor> --jsonl")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
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
