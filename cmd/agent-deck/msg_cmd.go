package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck msg`: the consumer face of the Comms Ledger (docs/comms.md,
// "Reading by consumer"). read, peek and ack work on a consumer's pending
// records (watermark plus sparse acknowledgements, under the consumer's
// lock); export prints any range of the ledger without consuming anything
// (what a test rig or another host reads); stats measures the #2482
// targets from the records themselves. Every verb has --json, and its text
// form prints the same fields.

// msgDefaultMaxBytes keeps one read under Claude Code's additionalContext
// limit, like the prompt-time inbox drain.
const msgDefaultMaxBytes = 9000

// msgExportDefaultLimit bounds one export call; "more" says to continue
// from "through".
const msgExportDefaultLimit = 1000

func handleMsg(profile string, args []string) {
	if len(args) == 0 || helpRequested(args) {
		printMsgUsage(os.Stdout)
		return
	}
	if err := runMsg(os.Stdout, args, profile); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func printMsgUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck msg read   [--for <session|self|name>] [--last N] [--max-bytes B] [--json]")
	fmt.Fprintln(w, "       agent-deck msg peek   [--for <session|self|name>] [--last N] [--max-bytes B] [--json]")
	fmt.Fprintln(w, "       agent-deck msg ack    [--for <session|self|name>] [--json] (<id>|<cursor>... | --all)")
	fmt.Fprintln(w, "       agent-deck msg export [--after <cursor>] [--limit N] [--for <session>] [--json]")
	fmt.Fprintln(w, "       agent-deck msg stats  [--since 24h] [--parent <session>] [--json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Read the Comms Ledger ([comms] ledger = true; docs/comms.md).")
	fmt.Fprintln(w, "read   prints a consumer's pending records (urgent first, within --max-bytes)")
	fmt.Fprintln(w, "       and acknowledges what it printed; the rest stays pending.")
	fmt.Fprintln(w, "peek   the same, acknowledging nothing.")
	fmt.Fprintln(w, "ack    acknowledges records by id (or its last 6 characters) or cursor.")
	fmt.Fprintln(w, "export prints ledger records after a cursor, consuming nothing.")
	fmt.Fprintln(w, "stats  measures the issue #2482 targets over a time window.")
	fmt.Fprintln(w, "--for defaults to the calling session (AGENTDECK_INSTANCE_ID).")
}

func runMsg(stdout io.Writer, args []string, explicitProfile string) error {
	verb, rest := args[0], args[1:]
	switch verb {
	case "read":
		return runMsgRead(stdout, rest, explicitProfile, true)
	case "peek":
		return runMsgRead(stdout, rest, explicitProfile, false)
	case "ack":
		return runMsgAck(stdout, rest, explicitProfile)
	case "export":
		return runMsgExport(stdout, rest)
	case "stats":
		return runMsgStats(stdout, rest)
	default:
		printMsgUsage(stdout)
		return fmt.Errorf("unknown msg verb %q", verb)
	}
}

// msgLedgerDir is the ledger directory of the process profile (the same
// one `events follow --bus comms` reads).
func msgLedgerDir() (string, string, error) {
	profile := events.CurrentProfile()
	dir, err := comms.Dir(profile)
	return profile, dir, err
}

// callerSessionID is the session this CLI runs in, "" at a plain shell.
func callerSessionID() string {
	return strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID"))
}

// resolveMsgConsumer turns --for into a consumer name: empty or "self" is
// the calling session; a name with a ':' (human:<conductor>) is taken as
// is; anything else is resolved as a session id or title in the profile.
func resolveMsgConsumer(value, explicitProfile string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "self") {
		id, err := resolveSelfSessionID()
		if err != nil {
			return "", fmt.Errorf("no consumer: pass --for <session> (outside a session there is no self): %w", err)
		}
		return id, nil
	}
	if strings.Contains(value, ":") {
		return value, comms.ValidConsumer(value)
	}
	id, err := resolveInboxDrainSessionInProfile(value, explicitProfile)
	if err != nil {
		return "", err
	}
	return id, nil
}

// msgNames maps session ids to titles for the text forms (best effort).
func msgNames() comms.Names {
	_, instances, _, err := loadSessionData(events.CurrentProfile())
	if err != nil {
		return nil
	}
	names := comms.Names{}
	for _, inst := range instances {
		if inst != nil && inst.Title != "" {
			names[inst.ID] = inst.Title
		}
	}
	return names
}

// msgReadOutput is the read/peek --json document.
type msgReadOutput struct {
	Consumer  string           `json:"consumer"`
	Profile   string           `json:"profile"`
	Store     string           `json:"store"`
	Epoch     int64            `json:"epoch"`
	Watermark events.Cursor    `json:"watermark"`
	Through   events.Cursor    `json:"through"`
	Acked     bool             `json:"acked"` // true for read: the records below are acknowledged
	Records   []comms.Exported `json:"records"`
	Left      int              `json:"left"` // pending records not shown (budget or --last)
	Gap       *comms.GapNote   `json:"gap,omitempty"`
}

// selectForBudget picks pending records for one read: urgent first, then
// the rest, each oldest first, while the rendered lines fit maxBytes and
// at most last records (0 = no count bound). The result is in ledger order.
func selectForBudget(pending []comms.Exported, last, maxBytes int, names comms.Names) []comms.Exported {
	if maxBytes <= 0 {
		maxBytes = msgDefaultMaxBytes
	}
	used := 120 // header
	var take []comms.Exported
	for _, wantUrgent := range []bool{true, false} {
		for _, e := range pending {
			if e.Record.IsUrgent() != wantUrgent {
				continue
			}
			if last > 0 && len(take) >= last {
				break
			}
			n := len(comms.Line(e.Record, names)) + 1
			if used+n > maxBytes && len(take) > 0 {
				continue
			}
			used += n
			take = append(take, e)
		}
	}
	sort.Slice(take, func(i, j int) bool { return take[i].Cursor < take[j].Cursor })
	return take
}

func runMsgRead(stdout io.Writer, args []string, explicitProfile string, consume bool) error {
	name := "msg peek"
	if consume {
		name = "msg read"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	forFlag := fs.String("for", "", "consumer: a session id or title, self, or a name like human:<conductor>")
	last := fs.Int("last", 0, "at most this many records (0 = as many as fit --max-bytes)")
	maxBytes := fs.Int("max-bytes", msgDefaultMaxBytes, "byte budget of the rendered records")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	consumer, err := resolveMsgConsumer(*forFlag, explicitProfile)
	if err != nil {
		return err
	}
	profile, dir, err := msgLedgerDir()
	if err != nil {
		return err
	}
	reader, err := comms.OpenReaderAt(dir)
	if err != nil {
		return err
	}
	defer reader.Close()
	var names comms.Names
	if !*asJSON {
		names = msgNames()
	}
	var out msgReadOutput
	_, err = reader.Do(consumer, func(p comms.Pass) ([]events.Cursor, error) {
		take := selectForBudget(p.Pending, *last, *maxBytes, names)
		out = msgReadOutput{Consumer: consumer, Profile: profile, Store: reader.Store.ID, Epoch: reader.Store.Epoch,
			Watermark: p.Watermark, Through: p.Through, Acked: consume, Records: take, Left: len(p.Pending) - len(take), Gap: p.Gap}
		if out.Records == nil {
			out.Records = []comms.Exported{}
		}
		// Print first; acknowledge only what reached stdout.
		if err := printMsgRead(stdout, out, names, *asJSON); err != nil {
			return nil, err
		}
		if !consume {
			return nil, nil
		}
		acks := make([]events.Cursor, 0, len(take))
		for _, e := range take {
			acks = append(acks, e.Cursor)
		}
		return acks, nil
	})
	if err != nil {
		return err
	}
	if caller := callerSessionID(); caller != "" && consume {
		session.SpoolCommsCall(caller, comms.CallMsgRead, consumer)
	}
	return nil
}

func printMsgRead(w io.Writer, out msgReadOutput, names comms.Names, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(out)
	}
	var b strings.Builder
	if out.Gap != nil {
		if out.Gap.Reason == "restored" {
			fmt.Fprintf(&b, "[agent-deck msg] the ledger was restored: records %d..%d may repeat or may be missing for %s; re-reading from record %d\n",
				out.Gap.From, out.Gap.To, out.Consumer, out.Gap.From)
		} else {
			fmt.Fprintf(&b, "[agent-deck msg] gap: records %d..%d were never shown to %s (%s); resumed at %d\n",
				out.Gap.From, out.Gap.To, out.Consumer, out.Gap.Reason, out.Gap.Resumed)
		}
	}
	if len(out.Records) == 0 {
		fmt.Fprintf(&b, "[agent-deck msg] nothing pending for %s\n", out.Consumer)
	} else {
		records := make([]comms.Record, 0, len(out.Records))
		for _, e := range out.Records {
			records = append(records, e.Record)
		}
		b.WriteString(comms.Digest(records, names))
		b.WriteByte('\n')
		if out.Left > 0 {
			fmt.Fprintf(&b, "%d more pending (agent-deck msg read --for %s)\n", out.Left, out.Consumer)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// msgAckOutput is the ack --json document.
type msgAckOutput struct {
	Consumer  string          `json:"consumer"`
	Acked     []events.Cursor `json:"acked"`
	Watermark events.Cursor   `json:"watermark"`
	Left      int             `json:"left"`
}

func runMsgAck(stdout io.Writer, args []string, explicitProfile string) error {
	fs := flag.NewFlagSet("msg ack", flag.ContinueOnError)
	forFlag := fs.String("for", "", "consumer: a session id or title, self, or a name like human:<conductor>")
	all := fs.Bool("all", false, "acknowledge every pending record")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	refs := fs.Args()
	if !*all && len(refs) == 0 {
		return errors.New("msg ack: name records by id or cursor, or pass --all")
	}
	consumer, err := resolveMsgConsumer(*forFlag, explicitProfile)
	if err != nil {
		return err
	}
	_, dir, err := msgLedgerDir()
	if err != nil {
		return err
	}
	reader, err := comms.OpenReaderAt(dir)
	if err != nil {
		return err
	}
	defer reader.Close()
	var acks []events.Cursor
	state, err := reader.Do(consumer, func(p comms.Pass) ([]events.Cursor, error) {
		if *all {
			for _, e := range p.Pending {
				acks = append(acks, e.Cursor)
			}
			return acks, nil
		}
		var missing []string
		for _, ref := range refs {
			c, ok := matchPending(p.Pending, ref)
			if !ok {
				missing = append(missing, ref)
				continue
			}
			acks = append(acks, c)
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("not pending for %s: %s (nothing was acknowledged)", consumer, strings.Join(missing, ", "))
		}
		return acks, nil
	})
	if err != nil {
		return err
	}
	out := msgAckOutput{Consumer: consumer, Acked: acks, Watermark: state.Watermark, Left: state.Pending}
	if out.Acked == nil {
		out.Acked = []events.Cursor{}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(out)
	}
	fmt.Fprintf(stdout, "acknowledged %d record(s) for %s; %d still pending\n", len(out.Acked), consumer, out.Left)
	return nil
}

// matchPending finds a pending record by cursor (a number), full id, or
// id tail (the last 6+ characters a rendered line shows, case insensitive,
// unique). A number that names no pending cursor is tried as an id tail.
func matchPending(pending []comms.Exported, ref string) (events.Cursor, bool) {
	ref = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(ref), "#"))
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		for _, e := range pending {
			if uint64(e.Cursor) == n {
				return e.Cursor, true
			}
		}
	}
	if len(ref) < 6 {
		return 0, false
	}
	var hit []events.Cursor
	for _, e := range pending {
		if strings.HasSuffix(e.Record.ID, ref) {
			hit = append(hit, e.Cursor)
		}
	}
	if len(hit) != 1 {
		return 0, false
	}
	return hit[0], true
}

// msgExportOutput is the export --json document: the contract a test rig
// and another host read (docs/comms.md "msg export").
type msgExportOutput struct {
	V       int              `json:"v"`
	Ledger  bool             `json:"ledger"` // false: this profile has no ledger (switch off or daemon never ran)
	Profile string           `json:"profile"`
	Store   string           `json:"store,omitempty"`
	Epoch   int64            `json:"epoch,omitempty"`
	NowMS   int64            `json:"now_ms"`
	After   events.Cursor    `json:"after"`
	Through events.Cursor    `json:"through"`
	More    bool             `json:"more"`
	Records []comms.Exported `json:"records"`
}

func runMsgExport(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("msg export", flag.ContinueOnError)
	after := fs.Uint64("after", 0, "only records after this cursor (0 = from the oldest retained)")
	limit := fs.Int("limit", msgExportDefaultLimit, "at most this many records; continue from \"through\" when \"more\" is true")
	forFlag := fs.String("for", "", "only records from or addressed to this session id")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if *limit <= 0 {
		*limit = msgExportDefaultLimit
	}
	profile, dir, err := msgLedgerDir()
	if err != nil {
		return err
	}
	out := msgExportOutput{V: 1, Profile: profile, NowMS: time.Now().UnixMilli(), After: events.Cursor(*after),
		Through: events.Cursor(*after), Records: []comms.Exported{}}
	reader, err := comms.OpenReaderAt(dir)
	switch {
	case errors.Is(err, comms.ErrNoLedger):
		return printMsgExport(stdout, out, *asJSON)
	case err != nil:
		return err
	}
	defer reader.Close()
	out.Ledger, out.Store, out.Epoch = true, reader.Store.ID, reader.Store.Epoch
	recs, through, err := comms.Export(reader.Bus, events.Cursor(*after), *limit)
	if err != nil {
		return err
	}
	end, _, err := reader.Bus.Ends()
	if err != nil {
		return err
	}
	out.Through, out.More = through, through < end
	for _, e := range recs {
		if *forFlag != "" && e.Record.From != *forFlag && !containsString(e.Record.To, *forFlag) {
			continue
		}
		out.Records = append(out.Records, e)
	}
	return printMsgExport(stdout, out, *asJSON)
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func printMsgExport(w io.Writer, out msgExportOutput, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(out)
	}
	if !out.Ledger {
		_, err := fmt.Fprintf(w, "no comms ledger for profile %s ([comms] ledger = true and a running notify daemon create it)\n", out.Profile)
		return err
	}
	var b strings.Builder
	for _, e := range out.Records {
		fmt.Fprintf(&b, "%d %s\n", e.Cursor, comms.Line(e.Record, nil))
	}
	fmt.Fprintf(&b, "through %d", out.Through)
	if out.More {
		fmt.Fprintf(&b, " (more: --after %d)", out.Through)
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// msgStatsOutput wraps comms.Stats with the ledger presence flag.
type msgStatsOutput struct {
	Ledger bool `json:"ledger"`
	comms.Stats
}

func runMsgStats(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("msg stats", flag.ContinueOnError)
	since := fs.Duration("since", 24*time.Hour, "window length, ending now")
	parent := fs.String("parent", "", "only this parent's wakes, calls and records")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	profile, dir, err := msgLedgerDir()
	if err != nil {
		return err
	}
	now := time.Now()
	out := msgStatsOutput{}
	reader, err := comms.OpenReaderAt(dir)
	switch {
	case errors.Is(err, comms.ErrNoLedger):
		out.Stats = comms.Stats{Profile: profile, SinceMS: now.Add(-*since).UnixMilli(), UntilMS: now.UnixMilli(),
			ByKind: map[string]int{}, ByTier: map[string]int{}, Parents: []comms.ParentStats{}, Targets: comms.NewTargets()}
	case err != nil:
		return err
	default:
		defer reader.Close()
		st, err := comms.ComputeStats(reader.Bus, reader.Store, comms.StatsOptions{Since: now.Add(-*since), Until: now, Parent: *parent})
		if err != nil {
			return err
		}
		st.Profile = profile
		out.Ledger, out.Stats = true, st
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(out)
	}
	printMsgStats(stdout, out)
	return nil
}

func printMsgStats(w io.Writer, out msgStatsOutput) {
	st := out.Stats
	if !out.Ledger {
		fmt.Fprintf(w, "no comms ledger for profile %s\n", st.Profile)
		return
	}
	fmt.Fprintf(w, "comms ledger %s (store %s, epoch %d): %d records over %.2f h\n", st.Profile, st.Store, st.Epoch, st.Records, st.Hours)
	t := st.Targets
	rows := []struct {
		name string
		t    comms.Target
	}{
		{"machine wakes per parent per hour", t.WakesPerParentHour},
		{"records with text %", t.TextPct},
		{"records per finished event", t.RecordsPerFinished},
		{"duplicate turn records %", t.DuplicatePct},
		{"output+drain calls per wake", t.CallsPerWake},
		{"sends with sender, text and final state %", t.SendSenderTextPct},
		{"cross-host records with latency", t.CrossHostLatency},
	}
	for _, r := range rows {
		value, verdict := "n/a", "no data"
		if r.t.Value != nil {
			value = strconv.FormatFloat(*r.t.Value, 'f', -1, 64)
		}
		switch {
		case r.t.Met == nil && r.t.Value != nil:
			verdict = "no verdict"
		case r.t.Met != nil && *r.t.Met:
			verdict = "met"
		case r.t.Met != nil:
			verdict = "MISSED"
		}
		fmt.Fprintf(w, "  %-34s %8s  (target %s %g, n=%d) %s\n", r.name, value, r.t.Op, r.t.Target, r.t.N, verdict)
	}
	for _, p := range st.Parents {
		fmt.Fprintf(w, "  parent %s: wakes=%d (%.2f/h) records=%d calls=%d", p.ID, p.Wakes, p.WakesPerHour, p.Records, p.Calls)
		if len(p.WakesBy) > 0 {
			keys := make([]string, 0, len(p.WakesBy))
			for k := range p.WakesBy {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%d", k, p.WakesBy[k]))
			}
			fmt.Fprintf(w, " [%s]", strings.Join(parts, " "))
		}
		fmt.Fprintln(w)
	}
}
