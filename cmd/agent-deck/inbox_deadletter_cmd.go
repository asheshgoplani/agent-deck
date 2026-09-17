package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func runInboxDeadLetter(stdout io.Writer, args []string) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "show") {
		return fmt.Errorf("usage: inbox dead-letter list [--store all|dead-letter|unowned] [--json], or show <ref> [--json]")
	}
	action := args[0]
	fs := flag.NewFlagSet("inbox dead-letter "+action, flag.ContinueOnError)
	fs.SetOutput(stdout)
	asJSON := fs.Bool("json", false, "output JSON")
	store := fs.String("store", "all", "host store: all, dead-letter, or unowned")
	if err := fs.Parse(normalizeArgs(fs, args[1:])); err != nil {
		return err
	}
	if action == "list" && fs.NArg() != 0 || action == "show" && fs.NArg() != 1 {
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
			fmt.Fprintf(stdout, "%s store=%s source=%q child=%q profile=%q problem=%q\n", rec.Ref, rec.Store, rec.Source, rec.ChildSessionID, rec.Profile, rec.Problem)
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
		fmt.Fprintf(stdout, "%s store=%s source=%q offset=%d problem=%q\nraw=%q\n", rec.Ref, rec.Store, rec.Source, rec.Offset, rec.Problem, rec.Raw)
		return nil
	}
	return fmt.Errorf("record reference is stale or absent; list records again")
}
