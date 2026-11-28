package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// listSyncFlags are the `list --json --sync` flags (wire contract v1, see
// internal/session/list_sync.go), registered on both list implementations
// (cliList and handleList).
type listSyncFlags struct {
	sync   *bool
	scope  *string
	cursor *string
	page   *string
	limit  *int
	focus  *string
}

func registerListSyncFlags(fs *flag.FlagSet) *listSyncFlags {
	return &listSyncFlags{
		sync:   fs.Bool(strings.TrimLeft(session.RemoteSyncFlag, "-"), false, "With --json: one-line sync envelope of what changed since --cursor, for remote pollers"),
		scope:  fs.String("scope", session.RemoteSyncScopeAll, "With --sync: active, archived or all"),
		cursor: fs.String("cursor", "", "With --sync: the cursor of the previous reply (none: a full listing)"),
		page:   fs.String("page", "", "With --sync: the next token of a paged full listing"),
		limit:  fs.Int("limit", 0, "With --sync: most rows and removed ids per reply (0: no limit)"),
		focus:  fs.String("focus", "", "With --sync: refresh only these comma-separated ids plus running, waiting, starting, conductor and favourite sessions"),
	}
}

// listSyncOptions is a validated sync request.
type listSyncOptions struct {
	req   session.RemoteSyncRequest
	focus bool
	stats bool
}

// options validates the sync flags after parsing. ok is false without
// --sync; a misuse is an error.
func (f *listSyncFlags) options(fs *flag.FlagSet, jsonOutput, allProfiles, noArchived, stats bool) (listSyncOptions, bool, error) {
	syncOnly, focusGiven := false, false
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "scope", "cursor", "page", "limit", "focus":
			syncOnly = true
			focusGiven = focusGiven || fl.Name == "focus"
		}
	})
	switch {
	case !*f.sync && syncOnly:
		return listSyncOptions{}, false, fmt.Errorf("--scope, --cursor, --page, --limit and --focus need --sync")
	case !*f.sync:
		return listSyncOptions{}, false, nil
	case !jsonOutput:
		return listSyncOptions{}, false, fmt.Errorf("--sync needs --json")
	case allProfiles || noArchived:
		return listSyncOptions{}, false, fmt.Errorf("--sync cannot be combined with --all or --no-archived (use --scope)")
	case *f.limit < 0:
		return listSyncOptions{}, false, fmt.Errorf("invalid limit %d", *f.limit)
	}
	scope, err := session.ParseRemoteSyncScope(*f.scope)
	if err != nil {
		return listSyncOptions{}, false, err
	}
	return listSyncOptions{
		req: session.RemoteSyncRequest{
			Scope: scope, Cursor: *f.cursor, Page: *f.page, Limit: *f.limit,
			FocusIDs: parseListSyncFocus(*f.focus),
		},
		focus: focusGiven,
		stats: stats,
	}, true, nil
}

// parseListSyncFocus splits --focus "id,id" into ids, dropping blanks.
func parseListSyncFocus(s string) []string {
	var ids []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// listSyncHot reports whether inst belongs to the focus pass's hot set: its
// stored status is running, waiting or starting, or it is a conductor or a
// favourite.
func listSyncHot(inst *session.Instance) bool {
	switch inst.Status {
	case session.StatusRunning, session.StatusWaiting, session.StatusStarting:
		return true
	}
	return inst.IsConductor || inst.Favorite
}

// handleListSync prints the `list --json --sync` reply for profile and exits
// non-zero on failure.
func handleListSync(profile string, includeSuperseded bool, opts listSyncOptions) {
	ensureTmuxInPathOrExit()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Printf("Error: failed to initialize storage: %v\n", err)
		exitCLI(1)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Printf("Error: failed to load sessions: %v\n", err)
		exitCLI(1)
	}
	if !includeSuperseded {
		instances = defaultListInstances(instances)
	}
	output, err := runListSync(storage, instances, opts)
	if err != nil {
		fmt.Printf("Error: list sync: %v\n", err)
		exitCLI(1)
	}
	fmt.Print(string(output))
}

// runListSync answers a sync request for instances (every listed session of
// the profile) and returns the one-line envelope.
//
// A full pass runs the live status pass for the rows that are not archived
// and builds the archived rows from their stored state without one, so an
// archived row's hash only moves when its stored state does. A focus pass
// does the same for the focused rows alone.
func runListSync(storage *session.Storage, instances []*session.Instance, opts listSyncOptions) ([]byte, error) {
	scope := opts.req.Scope
	var counts session.RemoteSyncCounts
	for _, inst := range instances {
		if inst.IsArchived() {
			counts.Archived++
		} else {
			counts.Active++
		}
	}

	rowsFor := instances
	if opts.focus {
		// Chosen from the stored status, before any status pass.
		want := map[string]bool{}
		for _, id := range opts.req.FocusIDs {
			want[id] = true
		}
		rowsFor = nil
		for _, inst := range instances {
			if session.RemoteSyncScopeHas(scope, inst.IsArchived()) && (want[inst.ID] || listSyncHot(inst)) {
				rowsFor = append(rowsFor, inst)
			}
		}
	}

	statusStarted := time.Now()
	tmuxBefore := tmux.SubprocessStarts()
	skipsBefore := tmux.CaptureBatchSkips()
	live := session.FilterInstancesByArchive(rowsFor, false)
	refresh, cached := session.CLIStatusCandidates(live)
	session.RefreshInstancesForCLIStatus(refresh)
	for _, inst := range rowsFor {
		if inst.IsArchived() {
			cached[inst] = true
		}
	}
	listed := core.LiveSessionRows(context.Background(), storage.Profile(), rowsFor, instances, cached, core.ReadStatusStamps(storage))
	statusElapsed := time.Since(statusStarted)
	tmuxCalls := tmux.SubprocessStarts() - tmuxBefore
	health.RecordStatusPass(statusElapsed, len(live), tmuxCalls)
	if opts.stats {
		emitListStats(session.ListStats{
			StatusPassMS:    statusElapsed.Milliseconds(),
			TmuxCalls:       tmuxCalls,
			Sessions:        len(live),
			CapturesSkipped: tmux.CaptureBatchSkips() - skipsBefore,
		})
	}

	// Through the legacy decode, so a sync row carries exactly the values a
	// `list --json` caller decodes into session.RemoteSessionInfo.
	for i := range listed {
		listed[i].Status = StatusString(session.Status(listed[i].Status))
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		return nil, fmt.Errorf("encoding list rows: %w", err)
	}
	var infos []session.RemoteSessionInfo
	if err := json.Unmarshal(encoded, &infos); err != nil {
		return nil, fmt.Errorf("decoding list rows: %w", err)
	}
	if len(infos) != len(rowsFor) {
		return nil, fmt.Errorf("decoding list rows: got %d rows, want %d", len(infos), len(rowsFor))
	}
	rows := make([]session.RemoteSyncRow, len(infos))
	for i := range infos {
		rows[i] = session.RemoteSyncRow{RemoteSessionInfo: infos[i], Order: rowsFor[i].Order}
	}

	journal := session.RemoteSyncJournalPath(storage.Path())
	var env session.RemoteSyncEnvelope
	if opts.focus {
		env, err = session.RemoteSyncFocusAnswer(journal, rows, opts.req)
	} else {
		env, err = session.RemoteSyncAnswer(journal, rows, opts.req)
	}
	if err != nil {
		return nil, err
	}
	env.Counts = counts
	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("encoding sync reply: %w", err)
	}
	return append(out, '\n'), nil
}
