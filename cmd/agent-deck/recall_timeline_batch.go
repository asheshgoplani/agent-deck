package main

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
)

// recallTimelineBatchSchema names the `recall timeline --sessions` shape.
const recallTimelineBatchSchema = "agent-deck.recall.timeline-batch/v1"

// recallTimelineBatchMax bounds one --sessions call. A 278-session box is
// read in about a dozen calls of 25, not 278 single ones.
const recallTimelineBatchMax = 50

// recallTimelineBatchWorkers is how many transcripts a batch tails at once.
const recallTimelineBatchWorkers = 4

// recallTimelineBatchFlags are the only flags --sessions may be combined with.
var recallTimelineBatchFlags = map[string]bool{"json": true, "tail": true, "sessions": true}

// timelineBatchEntry is one id's answer: the exact object the single-id
// call prints, or its error and CLI code.
type timelineBatchEntry struct {
	Session  string              `json:"session"`
	OK       bool                `json:"ok"`
	Timeline *query.RowsTimeline `json:"timeline,omitempty"`
	Error    string              `json:"error,omitempty"`
	Code     string              `json:"code,omitempty"`
}

type timelineBatchResult struct {
	Schema   string               `json:"schema"`
	Sessions []timelineBatchEntry `json:"sessions"`
}

// parseTimelineBatchIDs splits the --sessions list. Empty entries are an
// error rather than skipped, so a caller's join bug is loud.
func parseTimelineBatchIDs(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, fmt.Errorf("recall timeline: --sessions needs a comma-separated list of session ids")
	}
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
		if parts[i] == "" {
			return nil, fmt.Errorf("recall timeline: --sessions has an empty id at position %d", i+1)
		}
	}
	if len(parts) > recallTimelineBatchMax {
		return nil, fmt.Errorf("recall timeline: --sessions takes at most %d ids per call, got %d", recallTimelineBatchMax, len(parts))
	}
	return parts, nil
}

// validateTimelineBatchFlags refuses every flag but --json, --tail and
// --sessions, and any positional <session>, next to --sessions.
func validateTimelineBatchFlags(fs *flag.FlagSet) error {
	var bad []string
	fs.Visit(func(f *flag.Flag) {
		if !recallTimelineBatchFlags[f.Name] {
			bad = append(bad, "--"+f.Name)
		}
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("recall timeline: %s cannot be combined with --sessions; only --tail is allowed", strings.Join(bad, ", "))
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("recall timeline: pass ids either as <session> or in --sessions, not both")
	}
	return nil
}

// handleRecallTimelineBatch answers `recall timeline --json [--tail N]
// --sessions a,b,c`: one object, one entry per id in input order. A failed
// id never fails the batch; the exit code is 0 once the request is valid.
// The deck's sessions and the recall index are opened once per call, and
// transcripts are tailed by a small pool.
func handleRecallTimelineBatch(profile string, fs *flag.FlagSet, list string, jsonOutput bool, tail int) {
	out := NewCLIOutput(true, false)
	if !jsonOutput {
		fs.Usage()
		exitCLI(2)
	}
	if err := validateTimelineBatchFlags(fs); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	ids, err := parseTimelineBatchIDs(list)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	opts := query.RowsOptions{Tail: tail}
	if err := query.ValidateRowsOptions(opts); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	requireRecallEnabled(out)

	lk, err := newRowsLookup(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	lk.inBatch = true
	defer lk.close()

	entries := make([]timelineBatchEntry, len(ids))
	targets := make([]*rowsTarget, len(ids))
	// Resolve in order on one goroutine: the lookup opens the index lazily
	// and is not shared across goroutines.
	for i, id := range ids {
		entries[i].Session = id
		t, err := lk.resolve(id)
		if err != nil {
			entries[i].Error, entries[i].Code = err.Error(), ErrCodeNotFound
			if errors.Is(err, errRecallIndexUnavailable) {
				entries[i].Code = ErrCodeInvalidOperation
			}
			continue
		}
		targets[i] = t
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < recallTimelineBatchWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				result, code, err := readRowsTimeline(targets[i], opts)
				if err != nil {
					entries[i].Error, entries[i].Code = err.Error(), code
					continue
				}
				entries[i].OK, entries[i].Timeline = true, &result
			}
		}()
	}
	for i, t := range targets {
		if t != nil {
			jobs <- i
		}
	}
	close(jobs)
	wg.Wait()

	out.printJSON(timelineBatchResult{Schema: recallTimelineBatchSchema, Sessions: entries})
}
