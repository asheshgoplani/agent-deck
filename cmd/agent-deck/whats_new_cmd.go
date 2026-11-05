package main

// `agent-deck whats-new` and the What's new block `agent-deck update` prints
// after an install (ASK-205): your fixed reports first, then highlights.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/issuereport"
	"github.com/asheshgoplani/agent-deck/internal/reportloop"
)

// whatsNewFetchChangelog is a seam for tests.
var whatsNewFetchChangelog = reportloop.FetchChangelog

// ── whats-new ─────────────────────────────────────────────────────────

type whatsNewJSON struct {
	OK bool `json:"ok"`
	*reportloop.WhatsNew
	Text      string `json:"text"`
	Empty     bool   `json:"empty"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func handleWhatsNew(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := runWhatsNew(ctx, args, Version, os.Stdout)
	stop()
	if code != 0 {
		exitCLI(code)
	}
}

func printWhatsNewHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck whats-new [--json] [--since V] [--mark-seen]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Show what changed since the last version you saw: your own reports fixed in")
	fmt.Fprintln(w, "this range first (\"Your report #N: fixed in vX\"), then each version's highlights.")
	fmt.Fprintln(w, "The notes come from the release CHANGELOG ("+reportloop.ChangelogEnv+" overrides it).")
	fmt.Fprintln(w, "On the first run it remembers the current version and shows nothing.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --json        JSON: from, to, fixed, versions, text, empty")
	fmt.Fprintln(w, "  --since V     show changes after V instead of the last version seen")
	fmt.Fprintln(w, "  --mark-seen   remember the current version as seen (what the TUI does on close)")
}

// runWhatsNew implements `agent-deck whats-new`.
func runWhatsNew(ctx context.Context, args []string, version string, w io.Writer) int {
	fs := flag.NewFlagSet("whats-new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "JSON output")
	since := fs.String("since", "", "show changes after this version")
	markSeen := fs.Bool("mark-seen", false, "remember the current version as seen")
	help := fs.Bool("help", false, "show help")
	fs.BoolVar(help, "h", false, "show help")
	current := strings.TrimPrefix(version, "v")

	fail := func(code, msg string) int {
		if *jsonOut {
			writeLoopJSON(w, whatsNewJSON{ErrorCode: code, Message: msg})
		} else {
			fmt.Fprintln(w, "Error: "+msg)
		}
		return 1
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		for _, a := range args {
			if a == "--json" || a == "-json" {
				*jsonOut = true
			}
		}
		return fail(string(issuereport.ErrInvalidInput), err.Error())
	}
	if *help {
		printWhatsNewHelp(w)
		return 0
	}
	if fs.NArg() > 0 {
		return fail(string(issuereport.ErrInvalidInput), "whats-new takes no arguments")
	}
	store, err := feedbackReportStore()
	var st *reportloop.State
	if err == nil {
		st, err = store.Load()
	}
	if err != nil {
		return fail("store_failed", err.Error())
	}
	emptyResult := func(from string) int {
		if from == "" {
			from = current
		}
		text := fmt.Sprintf("Nothing new since v%s.\n", from)
		if *jsonOut {
			writeLoopJSON(w, whatsNewJSON{OK: true, WhatsNew: &reportloop.WhatsNew{From: from, To: current,
				Fixed: []reportloop.FixedReport{}, Versions: []reportloop.VersionNotes{}}, Text: text, Empty: true})
		} else {
			fmt.Fprint(w, text)
		}
		return 0
	}
	if strings.TrimSpace(*since) == "" && st.LastSeenVersion == "" {
		// First run on this machine: nothing to compare with. Start here.
		if err := store.MarkSeen(current); err != nil {
			reportLoopLog.Warn("whats_new_mark_seen_failed", slog.String("error", err.Error()))
		}
		return emptyResult(current)
	}
	changelog, err := whatsNewFetchChangelog(ctx)
	if err != nil {
		return fail("changelog_failed", err.Error())
	}
	wn, err := store.WhatsNew(changelog, current, *since)
	if err != nil {
		return fail("store_failed", err.Error())
	}
	if *markSeen {
		if err := store.MarkSeen(current); err != nil {
			return fail("store_failed", err.Error())
		}
	}
	if wn.Empty() {
		return emptyResult(wn.From)
	}
	text := wn.Text()
	if *jsonOut {
		writeLoopJSON(w, whatsNewJSON{OK: true, WhatsNew: wn, Text: text})
		return 0
	}
	fmt.Fprint(w, text)
	return 0
}

// printWhatsNewAfterUpdate prints the What's new block for (from, to] after
// a successful install. A failure is one note line; it never fails the
// update. markSeen is true for the manual path (the person just read it);
// the unattended path leaves the version unseen so the TUI shows it. On a
// store that never saw a version, the unattended path records from as
// seen: otherwise the next start would count as a fresh install, mark to
// seen and never show (from, to].
func printWhatsNewAfterUpdate(ctx context.Context, w io.Writer, from, to string, markSeen bool) {
	if !markSeen && strings.TrimPrefix(from, "v") != "" {
		if store, err := feedbackReportStore(); err == nil {
			if st, err := store.Load(); err == nil && st.LastSeenVersion == "" {
				if err := store.MarkSeen(from); err != nil {
					reportLoopLog.Warn("whats_new_mark_seen_failed", slog.String("error", err.Error()))
				}
			}
		}
	}
	if !markSeen {
		// Unattended (the update timer): no network beyond the release
		// itself. (from, to] stays pending; the TUI shows it on its next
		// start, and `agent-deck whats-new` any time.
		fmt.Fprintf(w, "What's new in v%s shows on the next agent-deck start (or run `agent-deck whats-new`).\n", strings.TrimPrefix(to, "v"))
		return
	}
	changelog, err := whatsNewFetchChangelog(ctx)
	if err != nil {
		reportLoopLog.Warn("whats_new_after_update_failed", slog.String("error", err.Error()))
		fmt.Fprintf(w, "(What's new is not available right now: %v; run `agent-deck whats-new` later.)\n", err)
		return
	}
	store, err := feedbackReportStore()
	var wn *reportloop.WhatsNew
	if err == nil {
		wn, err = store.WhatsNew(changelog, to, from)
	}
	if err != nil {
		reportLoopLog.Warn("whats_new_after_update_failed", slog.String("error", err.Error()))
		fmt.Fprintf(w, "(What's new is not available right now: %v; run `agent-deck whats-new` later.)\n", err)
		return
	}
	if !wn.Empty() {
		fmt.Fprintln(w)
		fmt.Fprint(w, wn.Text())
	}
	if markSeen {
		if err := store.MarkSeen(to); err != nil {
			reportLoopLog.Warn("whats_new_mark_seen_failed", slog.String("error", err.Error()))
		}
	}
}
