package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// runInboxCursor prints the incremental remote-talkback cursors this machine
// holds: per (remote, conductor) the newest turn-journal seq received per
// remote child, the ledger entry received per child and the _unowned
// position. Read-only.
func runInboxCursor(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("inbox cursor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the cursors as a JSON array")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox cursor [--json] [<remote>]")
		fmt.Fprintln(stdout, "Show the remote-talkback cursors `remote drain` keeps per remote and conductor.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("inbox cursor takes at most one remote name")
	}
	cursors, err := session.ListRemoteCursors(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("list remote cursors: %w", err)
	}
	if *asJSON {
		if cursors == nil {
			cursors = []session.RemoteCursorFile{}
		}
		return json.NewEncoder(stdout).Encode(cursors)
	}
	if len(cursors) == 0 {
		fmt.Fprintln(stdout, "No remote cursors. `agent-deck remote drain <remote> --into <conductor>` creates one.")
		return nil
	}
	for _, c := range cursors {
		if c.Cursor.Legacy {
			fmt.Fprintf(stdout, "%s → %s  updated %s  legacy remote (full export, no position)\n",
				c.Remote, c.Parent, c.UpdatedAt.Format(time.RFC3339))
			continue
		}
		ts := "-"
		if !c.Cursor.TS.IsZero() {
			ts = c.Cursor.TS.Format(time.RFC3339)
		}
		fmt.Fprintf(stdout, "%s → %s  updated %s  newest %s  children %d  ledger %d  unowned %d\n",
			c.Remote, c.Parent, c.UpdatedAt.Format(time.RFC3339), ts, len(c.Cursor.Seqs), len(c.Cursor.Ledger), c.Cursor.Unowned.N)
		for _, child := range slices.Sorted(maps.Keys(c.Cursor.Seqs)) {
			fmt.Fprintf(stdout, "    %s  seq %d\n", child, c.Cursor.Seqs[child])
		}
	}
	return nil
}
