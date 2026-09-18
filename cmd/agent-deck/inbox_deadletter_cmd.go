package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// runInboxDeadLetter dispatches the full `inbox dead-letter` family:
// list/show (#2111, this file — read-only inspection of every physical
// record, keyed by Ref) and retry/purge (#2062, inbox_cmd.go — content-hash
// ID identifiers; see DeadLetterRecord's doc comment in
// internal/session/deadletter_inspection.go for why the two identifier
// schemes coexist on one shared type).
func runInboxDeadLetter(stdout io.Writer, args []string) error {
	usage := "usage: inbox dead-letter list|show|retry|purge (list [--store all|dead-letter|unowned] [--json], show <ref> [--json], retry [--json] <id>, purge [--json] --older-than <duration>|--yes)"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, usage)
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Commands:")
		fmt.Fprintln(stdout, "  list [--store all|dead-letter|unowned] [--json]  List every physical record")
		fmt.Fprintln(stdout, "  show [--json] <ref>                              Show one record without raw content")
		fmt.Fprintln(stdout, "  retry [--json] <id>                              Re-resolve and redeliver one record")
		fmt.Fprintln(stdout, "  purge [--json] --older-than <duration>           Purge only records older than a bound")
		fmt.Fprintln(stdout, "  purge [--json] --yes                             Purge every record with explicit consent")
		fmt.Fprintln(stdout, "  purge never removes an _unowned record; only the TTL sweep may reclaim one.")
		return nil
	case "retry":
		return runInboxDeadLetterRetry(stdout, args[1:])
	case "purge":
		return runInboxDeadLetterPurge(stdout, args[1:])
	case "list", "show":
		return runInboxDeadLetterInspect(stdout, args[0], args[1:])
	default:
		return errors.New(usage)
	}
}

// runInboxDeadLetterInspect serves the read-only `list` and `show` actions.
func runInboxDeadLetterInspect(stdout io.Writer, action string, args []string) error {
	fs := flag.NewFlagSet("inbox dead-letter "+action, flag.ContinueOnError)
	fs.SetOutput(stdout)
	asJSON := fs.Bool("json", false, "output JSON")
	store := fs.String("store", "all", "host store: all, dead-letter, or unowned")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if (action == "list" && fs.NArg() != 0) || (action == "show" && fs.NArg() != 1) {
		return fmt.Errorf("list takes no record reference; show requires exactly one reference")
	}
	if action == "show" {
		decoded, err := hex.DecodeString(fs.Arg(0))
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("invalid record reference; use inbox dead-letter list")
		}
	}
	records, err := session.InspectDeadLetters(*store)
	if err != nil {
		return fmt.Errorf("inspect dead letters: %w", err)
	}
	if action == "list" {
		if *asJSON {
			return json.NewEncoder(stdout).Encode(records)
		}
		fmt.Fprintf(stdout, "%d host-level record(s); inspection does not consume records\n", len(records))
		for _, rec := range records {
			fmt.Fprintf(stdout, "%s store=%s source=%q child=%q profile=%q problem=%q id=%s\n", rec.Ref, rec.Store, rec.Source, rec.ChildSessionID, rec.Profile, rec.Problem, rec.ID)
		}
		return nil
	}
	for _, rec := range records {
		if rec.Ref != fs.Arg(0) {
			continue
		}
		// Base64 is lossless for malformed bytes, line endings and unknown fields.
		raw := base64.StdEncoding.EncodeToString(rec.Raw)
		if *asJSON {
			var event json.RawMessage
			if utf8.Valid(rec.Raw) && json.Valid(rec.Raw) {
				event = rec.Raw
			}
			return json.NewEncoder(stdout).Encode(struct {
				session.DeadLetterRecord
				Event     json.RawMessage `json:"event,omitempty"`
				RawBase64 string          `json:"raw_base64"`
			}{rec, event, raw})
		}
		fmt.Fprintf(stdout, "%s store=%s source=%q offset=%d problem=%q id=%s\nraw=%q\n", rec.Ref, rec.Store, rec.Source, rec.Offset, rec.Problem, rec.ID, rec.Raw)
		return nil
	}
	return fmt.Errorf("record reference is stale or absent; list records again")
}
