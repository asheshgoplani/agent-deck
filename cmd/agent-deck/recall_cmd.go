package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/recall"
	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Recall phase 2 (docs/recall.md): the transcript index. Every subcommand
// is gated on [recall] enabled = true; hints and `session annotate` are
// not. The index is machine-global (one recall.db for every profile) with
// profile as a filter; hint and tag filters join the active profile's
// state.db live; cost events for linked sessions are written from the same
// pass that indexes the text.

func printRecallHelp() {
	fmt.Println(`Usage: agent-deck recall <command> [options]

Search and inspect every conversation on this machine, across harnesses
(Claude, Codex, pi, Gemini, OpenCode, Hermes; docs/recall.md). Requires
[recall] enabled = true in config.toml. The index (recall.db) is disposable;
hints and tags live in state.db and survive a rebuild.

Commands:
  search "<q>"     Full-text search over messages, titles and hints
  sessions         List indexed sessions (newest first)
  show <session>   One session: card, tools, files, messages
  open <session>   Relaunch a session (start its deck session, or resume a Claude conversation)
  status           Index size, sources by state, what is pending
  backfill         Index all history (resumable; refuses while sessions are busy)
  sweep            Index what changed since the last sweep
  gc               Reclaim space from vanished transcripts
  rebuild          Delete the index and backfill again

Every command accepts --json and --help. <session> is a #number from a
listing, a harness conversation id (or unique prefix), or an agent-deck
session id. The TUI's G key is the same search over the same index.

Examples:
  agent-deck recall search "clock skew" --since 30d --profile work
  agent-deck recall search SB-412 --hint ticket=SB-412 --phrase
  agent-deck recall show 91fd7978 --turns 20
  agent-deck recall backfill --budget 5m`)
}

func handleRecall(profile string, args []string) {
	if len(args) == 0 || helpRequested(args[:1]) || args[0] == "help" {
		printRecallHelp()
		if len(args) == 0 {
			os.Exit(1)
		}
		return
	}
	switch args[0] {
	case "search":
		handleRecallSearch(profile, args[1:])
	case "sessions":
		handleRecallSessions(profile, args[1:])
	case "show":
		handleRecallShow(profile, args[1:])
	case "open":
		handleRecallOpen(profile, args[1:])
	case "status":
		handleRecallStatus(profile, args[1:])
	case "backfill":
		handleRecallBackfill(profile, args[1:])
	case "sweep":
		handleRecallSweep(profile, args[1:])
	case "gc":
		handleRecallGC(profile, args[1:])
	case "rebuild":
		handleRecallRebuild(profile, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown recall command: %s\n\n", args[0])
		printRecallHelp()
		os.Exit(1)
	}
}

// recallEnv is everything a recall subcommand needs, opened once.
type recallEnv struct {
	profile   string
	cfg       *session.UserConfig
	st        *store.Store
	dbPath    string
	lockPath  string
	queuePath string
	stateDB   string
	storage   *session.Storage
	reg       *statedb.StateDB
	registry  *recallRegistry
	roots     []reader.Root
}

func (e *recallEnv) close() {
	if e.st != nil {
		e.st.Close()
	}
	if e.registry != nil {
		e.registry.close()
	}
	if e.storage != nil {
		e.storage.Close()
	}
}

// openRecallEnv opens recall.db and the profile's state.db. It exits with
// a clear message when the feature is off.
func openRecallEnv(profile string, out *CLIOutput) *recallEnv {
	cfg, _ := session.LoadUserConfig()
	if cfg == nil {
		cfg = &session.UserConfig{}
	}
	if !cfg.Recall.GetEnabled() {
		out.Error("recall is off: set [recall] enabled = true in config.toml (docs/recall.md); hints and 'session annotate' work without it", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	dbPath, err := recall.DBPath()
	if err != nil {
		out.Error(fmt.Sprintf("recall: resolve data dir: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	lockPath, err := recall.LockPath()
	if err != nil {
		out.Error(fmt.Sprintf("recall: resolve lock: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	st, err := store.OpenCurrent(dbPath)
	if errors.Is(err, store.ErrSchema) {
		// Another schema version: recreate, but only under the sweep lock
		// so a running backfill is never pulled out from under.
		release := (&recallEnv{lockPath: lockPath}).lock(out)
		st, err = store.Open(dbPath)
		release()
	}
	if err != nil {
		out.Error(fmt.Sprintf("recall: open %s: %v", dbPath, err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	env := &recallEnv{profile: profile, cfg: cfg, st: st, dbPath: dbPath, lockPath: lockPath, roots: session.RecallRoots()}
	if env.queuePath, err = recall.QueuePath(); err != nil {
		out.Error(fmt.Sprintf("recall: resolve queue: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if storage, err := session.NewStorageWithProfile(profile); err == nil {
		env.storage = storage
		env.reg = storage.GetDB()
		env.profile = storage.Profile() // the resolved profile, not the flag
		if p, err := session.GetDBPathForProfile(env.profile); err == nil {
			env.stateDB = p
		}
	}
	env.registry = newRecallRegistry(env.profile, env.reg)
	return env
}

// recallRegistry is session.RecallRegistry (every profile's state.db,
// shared with the hook and the TUI) plus the CLI's usage sink: cost
// events go to the state.db that holds the deck session's link.
type recallRegistry struct {
	*session.RecallRegistry
	pricer *costs.Pricer
	sinks  map[*statedb.StateDB]*costs.UsageImporter
}

func newRecallRegistry(profile string, reg *statedb.StateDB) *recallRegistry {
	return &recallRegistry{RecallRegistry: session.NewRecallRegistry(profile, reg), sinks: map[*statedb.StateDB]*costs.UsageImporter{}}
}

func (r *recallRegistry) close() { r.Close() }

// Usage implements ingest.UsageSink: cost events go to the state.db that
// holds the deck session's link, whichever profile that is.
func (r *recallRegistry) Usage(profile, deck string, events []reader.Usage) error {
	db := r.DB(r.OwnerProfile(deck))
	if db == nil {
		db = r.DB(r.Profile())
	}
	if db == nil {
		return nil
	}
	sink := r.sinks[db]
	if sink == nil {
		if r.pricer == nil {
			r.pricer = newPricerFromConfig()
		}
		sink = costs.NewUsageImporter(costs.NewStore(db.DB()), r.pricer)
		r.sinks[db] = sink
	}
	return sink.Usage(deck, events)
}

// busySessions reports the managed sessions of this profile that are
// mid-turn, from the status column state.db already keeps.
func (e *recallEnv) busySessions() (bool, string) {
	if e.reg == nil {
		return false, ""
	}
	rows, err := e.reg.LoadInstances()
	if err != nil {
		return false, ""
	}
	var busy []string
	for _, r := range rows {
		if ingest.BusyStatuses[r.Status] {
			busy = append(busy, r.Title)
		}
	}
	if len(busy) == 0 {
		return false, ""
	}
	sort.Strings(busy)
	if len(busy) > 3 {
		busy = append(busy[:3], fmt.Sprintf("and %d more", len(busy)-3))
	}
	return true, fmt.Sprintf("session %s is busy (use --force to run anyway)", strings.Join(busy, ", "))
}

func (e *recallEnv) ingestOptions(force bool) ingest.Options {
	opts := ingest.Options{
		Roots:          e.roots,
		Registry:       e.registry,
		Usage:          e.registry,
		TextTier:       e.cfg.Recall.GetTextTier(),
		PerSourceBytes: int64(e.cfg.Recall.GetPerSourceMB()) << 20,
		NewestFirst:    true,
		QueuePath:      e.queuePath,
	}
	if !force {
		opts.Gate = &ingest.Gate{Busy: e.busySessions, MaxLoadAvg: e.cfg.Recall.GetMaxLoadAvg()}
	}
	return opts
}

// lock takes the machine-global sweep lock or exits.
func (e *recallEnv) lock(out *CLIOutput) func() {
	release, err := store.Lock(e.lockPath)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			out.Error("recall: another backfill or sweep is running (lock: "+e.lockPath+")", ErrCodeInvalidOperation)
			os.Exit(3)
		}
		out.Error("recall: lock: "+err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	return release
}

func newRecallFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseRecallFlags parses args and reports whether the command should go
// on: false after --help (usage already printed); a bad flag exits 2.
func parseRecallFlags(fs *flag.FlagSet, args []string) bool {
	err := fs.Parse(normalizeArgs(fs, args))
	if err == nil {
		return true
	}
	if errors.Is(err, flag.ErrHelp) {
		return false
	}
	os.Exit(2)
	return false
}

// recallLookupCode maps a session lookup error to the CLI error code.
func recallLookupCode(err error) string {
	if errors.Is(err, query.ErrNotFound) {
		return ErrCodeNotFound
	}
	return ErrCodeInvalidOperation
}

// interruptibleContext cancels on SIGINT/SIGTERM so the current batch is
// committed and the cursor kept.
func interruptibleContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// ---- filters shared by search and sessions -------------------------------

type recallFilterFlags struct {
	harness, profile, project, since string
	hints                            map[string]string
	tags                             []string
	deck                             string
	sidechains                       bool
}

func registerRecallFilters(fs *flag.FlagSet) *recallFilterFlags {
	f := &recallFilterFlags{hints: map[string]string{}}
	fs.StringVar(&f.harness, "harness", "", "Only this harness (claude, codex, pi, gemini, opencode, hermes)")
	fs.StringVar(&f.profile, "profile", "", "Only this profile's transcripts (default: every profile)")
	fs.StringVar(&f.project, "project", "", "Only sessions whose working directory is PATH")
	fs.StringVar(&f.since, "since", "", "Only sessions active since (30d, 24h, or YYYY-MM-DD)")
	fs.StringVar(&f.deck, "session", "", "Only the conversation bound to this agent-deck session id")
	fs.BoolVar(&f.sidechains, "subagents", false, "Include subagent transcripts")
	fs.Func("hint", "Only sessions with this hint (key=value, repeatable; joins state.db live)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" || v == "" {
			return fmt.Errorf("expected key=value, got %q", s)
		}
		f.hints[k] = v
		return nil
	})
	fs.Func("tag", "Only sessions with this tag (repeatable)", func(s string) error {
		tag, err := validTag(s)
		if err != nil {
			return err
		}
		f.tags = append(f.tags, tag)
		return nil
	})
	return f
}

func (f *recallFilterFlags) filters() (query.Filters, error) {
	q := query.Filters{Harness: f.harness, Profile: f.profile, Tags: f.tags, DeckID: f.deck, IncludeSidechains: f.sidechains}
	if f.project != "" {
		q.Project = filepath.Clean(session.ExpandPath(f.project))
	}
	if len(f.hints) > 0 {
		q.Hints = f.hints
	}
	if f.since != "" {
		t, err := parseRecallSince(f.since)
		if err != nil {
			return q, err
		}
		q.Since = t
	}
	return q, nil
}

// parseRecallSince accepts 30d, 12h, a Go duration, or YYYY-MM-DD.
func parseRecallSince(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n >= 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since %q: use 30d, 12h or YYYY-MM-DD", s)
}

// ---- search --------------------------------------------------------------

type recallIndexNote struct {
	Swept         bool     `json:"swept"`
	Skipped       string   `json:"skipped,omitempty"`
	Parsed        int      `json:"parsed,omitempty"`
	Deferred      int      `json:"deferred,omitempty"`
	DeferredBytes int64    `json:"deferred_bytes,omitempty"`
	DeferredPaths []string `json:"deferred_paths,omitempty"`
	ElapsedMS     int64    `json:"elapsed_ms"`
}

// interactiveSweep runs the 150 ms / 32 MB sweep before a read and reports
// what it left for a full sweep. Nothing parses if another sweep holds the
// lock; the read proceeds on the index as-is.
func (e *recallEnv) interactiveSweep(ctx context.Context) recallIndexNote {
	note := recallIndexNote{}
	release, err := store.Lock(e.lockPath)
	if err != nil {
		note.Skipped = "another sweep is running"
		return note
	}
	defer release()
	opts := e.ingestOptions(true)
	opts.Budget = reader.NewBudget(ingest.InteractiveDeadline, ingest.InteractiveBytes)
	res, err := ingest.New(e.st, opts).Sweep(ctx)
	note.ElapsedMS = res.ElapsedMS
	if err != nil {
		note.Skipped = err.Error()
		return note
	}
	note.Swept = true
	note.Parsed, note.Deferred, note.DeferredBytes, note.DeferredPaths = res.Parsed, res.Deferred, res.DeferredBytes, res.DeferredPaths
	return note
}

func (n recallIndexNote) String() string {
	switch {
	case n.Skipped != "":
		return "index not refreshed: " + n.Skipped
	case n.Deferred > 0:
		return fmt.Sprintf("index is behind by %d source(s) / %s; run 'agent-deck recall sweep' (or backfill)", n.Deferred, humanBytes(n.DeferredBytes))
	}
	return ""
}

func handleRecallSearch(profile string, args []string) {
	fs := newRecallFlagSet("recall search")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	role := fs.String("role", "", "Only body hits in user or assistant messages")
	phrase := fs.Bool("phrase", false, "Verify the literal phrase in the ranked hits' message bodies (reports how many bodies were read)")
	phraseScan := fs.Int("phrase-scan-limit", query.DefaultPhraseScan, "Bodies to decompress in all with --phrase")
	limit := fs.Int("limit", query.DefaultLimit, "Sessions to return")
	noSweep := fs.Bool("no-sweep", false, "Skip the bounded index refresh before searching")
	filters := registerRecallFilters(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall search "<query>" [filters] [--role user|assistant] [--phrase] [--limit 20] [--json]

Terms are AND-ed; AND / OR / NOT and trailing * (prefix) work; identifiers like
SB-412 and handle_sess are single terms. Titles, hints and tags always outrank
an incidental mention in a message body. A bounded sweep (150 ms / 32 MB) runs
first and the output says what it left for 'recall sweep'.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fs.Usage()
		os.Exit(2)
	}
	f, err := filters.filters()
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	opts := query.SearchOptions{Filters: f, Query: fs.Arg(0), Phrase: *phrase, PhraseScan: *phraseScan, Limit: *limit}
	switch strings.ToLower(*role) {
	case "":
	case "user":
		opts.Role = recall.RoleUser
	case "assistant":
		opts.Role = recall.RoleAssistant
	default:
		out.Error("--role must be user or assistant", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	ctx, cancel := interruptibleContext()
	defer cancel()
	var note recallIndexNote
	if !*noSweep {
		note = env.interactiveSweep(ctx)
	}
	res, err := query.New(env.st, env.stateDB).Search(ctx, opts)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "result": res, "index": note})
		return
	}
	printRecallSearch(res, note)
}

func printRecallSearch(res query.SearchResult, note recallIndexNote) {
	head := fmt.Sprintf("%d session(s) for %q", len(res.Hits), res.Query)
	details := []string{fmt.Sprintf("%d matching messages", res.Candidates)}
	if res.CeilingHit {
		details[0] += fmt.Sprintf(" (capped at %d; narrow the query or add filters)", query.CandidateCeiling)
	}
	if res.Scanned > 0 || res.VerifiedCount > 0 {
		details = append(details, fmt.Sprintf("phrase verified in %d session(s) over %d body(ies)", res.VerifiedCount, res.Scanned))
	}
	details = append(details, fmt.Sprintf("%d ms", res.ElapsedMS))
	fmt.Printf("%s: %s\n", head, strings.Join(details, ", "))
	if s := note.String(); s != "" {
		fmt.Println("  " + s)
	}
	for _, h := range res.Hits {
		fmt.Println(formatRecallHit(h))
		if h.Snippet != "" {
			fmt.Println("      " + h.Snippet)
		}
	}
}

func formatRecallHit(h query.Hit) string {
	var marks []string
	if h.CardHit {
		marks = append(marks, "title/hint")
	}
	if h.BodyHits > 0 {
		marks = append(marks, fmt.Sprintf("%d in body", h.BodyHits))
	}
	if h.Verified != nil {
		if *h.Verified {
			marks = append(marks, "phrase verified")
		} else {
			marks = append(marks, "phrase NOT found")
		}
	} else if h.Clipped {
		marks = append(marks, "phrase unverified (clipped body)")
	} else if h.PhraseChecked {
		marks = append(marks, "phrase unverified (scan limit)")
	}
	if h.Missing {
		marks = append(marks, "source file missing")
	}
	if h.Sidechain {
		marks = append(marks, "subagent")
	}
	title := h.Title
	if title == "" {
		title = "(untitled)"
	}
	deck := ""
	if h.DeckID != "" {
		deck = " deck:" + h.DeckID
	}
	return fmt.Sprintf("  %-7s %s  %s/%s  %s%s  [%s]\n      %s  %s", query.Ref(h.SessID), recallDate(h.EndedAt, h.StartedAt), h.Harness, h.Profile,
		title, deck, strings.Join(marks, ", "), shortNative(h.NativeID), h.CWD)
}

func recallDate(ended, started int64) string {
	ts := ended
	if ts == 0 {
		ts = started
	}
	if ts == 0 {
		return "unknown   "
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02")
}

func shortNative(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 && i >= 8 {
		return id[:8] + id[i:]
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---- sessions ------------------------------------------------------------

func handleRecallSessions(profile string, args []string) {
	fs := newRecallFlagSet("recall sessions")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	limit := fs.Int("limit", query.DefaultLimit, "Sessions to list")
	noSweep := fs.Bool("no-sweep", false, "Skip the bounded index refresh")
	filters := registerRecallFilters(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall sessions [filters] [--limit 20] [--json]\n\nList indexed sessions, most recently active first.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	f, err := filters.filters()
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	ctx, cancel := interruptibleContext()
	defer cancel()
	var note recallIndexNote
	if !*noSweep {
		note = env.interactiveSweep(ctx)
	}
	rows, err := query.New(env.st, env.stateDB).Sessions(ctx, f, *limit)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "sessions": rows, "index": note})
		return
	}
	if s := note.String(); s != "" {
		fmt.Println(s)
	}
	if len(rows) == 0 {
		fmt.Println("No indexed sessions match. Run 'agent-deck recall backfill' to index history.")
		return
	}
	for _, r := range rows {
		fmt.Println(formatRecallSession(r))
	}
}

func formatRecallSession(r query.SessionRow) string {
	title := r.Title
	if title == "" {
		title = r.Preview
	}
	if title == "" {
		title = "(untitled)"
	}
	title = clipRunesString(title, 60)
	var marks []string
	if r.DeckID != "" {
		marks = append(marks, "deck:"+r.DeckID)
	}
	if r.Missing {
		marks = append(marks, "missing")
	}
	if r.Sidechain {
		marks = append(marks, "subagent")
	}
	extra := ""
	if len(marks) > 0 {
		extra = "  [" + strings.Join(marks, ", ") + "]"
	}
	return fmt.Sprintf("  %-7s %s  %s/%-9s %3d turns %4d tools %2d err  %s%s\n      %s  %s", query.Ref(r.SessID), recallDate(r.EndedAt, r.StartedAt),
		r.Harness, r.Profile, r.Turns, r.ToolCalls, r.Errors, title, extra, shortNative(r.NativeID), r.CWD)
}

func clipRunesString(s string, n int) string {
	rs := []rune(strings.Join(strings.Fields(s), " "))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n-1]) + "…"
}

// ---- show ----------------------------------------------------------------

func handleRecallShow(profile string, args []string) {
	fs := newRecallFlagSet("recall show")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	tier := fs.String("tier", "excerpt", "card (no messages), excerpt (first --turns messages) or raw (every message)")
	turns := fs.Int("turns", 40, "Messages to print for --tier excerpt")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall show <session> [--tier card|excerpt|raw] [--turns 40] [--json]\n\nOne session: its card, tool summary, touched files and decoded messages.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	n := *turns
	switch *tier {
	case "card":
		n = -1
	case "excerpt":
		if n <= 0 {
			n = 40
		}
	case "raw":
		n = 0
	default:
		out.Error("--tier must be card, excerpt or raw", ErrCodeInvalidOperation)
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	defer env.close()
	d, err := query.New(env.st, env.stateDB).Show(context.Background(), fs.Arg(0), n)
	if err != nil {
		out.Error(err.Error(), recallLookupCode(err))
		if errors.Is(err, query.ErrNotFound) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "detail": d})
		return
	}
	printRecallDetail(d)
}

func printRecallDetail(d query.Detail) {
	s := d.Session
	fmt.Println(formatRecallSession(s))
	fmt.Printf("      conversation %s  branch %s  model %s  messages %d  compacts %d  interrupts %d\n", s.NativeID, orDash(s.Branch), orDash(s.Model), s.Messages, s.Compacts, s.Interrupts)
	fmt.Printf("      tokens in %d out %d cache-read %d cache-write %d\n", s.InTok, s.OutTok, s.CacheR, s.CacheW)
	if s.Hints != "" || s.Tags != "" {
		fmt.Printf("      hints: %s  tags: %s\n", orDash(s.Hints), orDash(s.Tags))
	}
	if s.Path != "" {
		fmt.Printf("      file: %s\n", s.Path)
	}
	if len(d.Tools) > 0 {
		fmt.Println("  Tools:")
		for _, t := range d.Tools {
			fmt.Printf("      %-14s %4d calls %3d errors %8s  %s\n", t.Name, t.Calls, t.Errors, (time.Duration(t.TotalMS) * time.Millisecond).Round(time.Second), clipRunesString(t.LastDigest, 60))
		}
	}
	if len(d.Files) > 0 {
		fmt.Println("  Files:")
		for _, f := range d.Files {
			fmt.Println("      " + f)
		}
	}
	if len(d.Messages) > 0 {
		fmt.Println("  Messages:")
		for _, m := range d.Messages {
			ts := ""
			if m.TS > 0 {
				ts = time.Unix(m.TS, 0).Local().Format("15:04")
			}
			label := m.Role
			if m.Class != "prompt" && m.Class != "assist" {
				label += "/" + m.Class
			}
			if m.ToolNames != "" {
				label += " -> " + m.ToolNames
			}
			fmt.Printf("    %4d %5s %-24s %s\n", m.Seq, ts, label, clipRunesString(m.Text, 500))
		}
	}
	if d.Truncated > 0 {
		fmt.Printf("  … %d more message(s); --tier raw prints them all\n", d.Truncated)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ---- open ----------------------------------------------------------------

// handleRecallOpen relaunches a found conversation. A session that is still
// registered starts through `session start`; a transcript with no
// agent-deck record is re-registered with `add --resume-session`, so old
// history becomes a live session again.
func handleRecallOpen(profile string, args []string) {
	fs := newRecallFlagSet("recall open")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	dryRun := fs.Bool("dry-run", false, "Print what would run instead of running it")
	title := fs.String("title", "", "Title for a re-registered session (default: the conversation's title)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall open <session> [--title T] [--dry-run] [--json]\n\nRelaunch a found conversation: starts its agent-deck session if it still exists, otherwise registers a new session that resumes the Claude conversation in its original directory.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	env := openRecallEnv(profile, out)
	sess, err := query.New(env.st, env.stateDB).Resolve(context.Background(), fs.Arg(0))
	if err != nil {
		env.close()
		out.Error(err.Error(), recallLookupCode(err))
		os.Exit(2)
	}
	cmdArgs, action, err := recallOpenPlan(env, sess, *title)
	env.close()
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(2)
	}
	if *dryRun {
		if *jsonOutput {
			out.printJSON(map[string]any{"success": true, "action": action, "args": cmdArgs, "session": sess})
			return
		}
		fmt.Printf("would run: agent-deck %s\n", strings.Join(quoteArgs(cmdArgs), " "))
		return
	}
	if *jsonOutput {
		cmdArgs = append(cmdArgs, "--json")
	}
	if len(cmdArgs) > 2 && cmdArgs[0] == "-p" {
		profile, cmdArgs = cmdArgs[1], cmdArgs[2:]
	}
	switch action {
	case "start":
		handleSession(profile, cmdArgs[1:])
	default:
		handleAdd(profile, cmdArgs[1:])
	}
}

// recallOpenPlan decides between starting the bound session and registering
// a resume of the conversation.
func recallOpenPlan(env *recallEnv, sess query.SessionRow, title string) ([]string, string, error) {
	if sess.Sidechain {
		return nil, "", errors.New("a subagent transcript cannot be resumed; open its parent session")
	}
	// The live link in state.db wins over the deck_id column, which only
	// moves on a sweep; the link may sit in another profile's state.db,
	// and the session then starts under that profile.
	deck, owner := sess.DeckID, env.reg
	if env.registry != nil {
		if id, db := env.registry.Owner(sess.Profile, sess.Harness, sess.NativeID); id != "" {
			deck, owner = id, db
		}
	}
	if deck != "" && owner != nil {
		if rows, err := owner.LoadInstances(); err == nil {
			for _, r := range rows {
				if r.ID == deck {
					args := []string{"session", "start", deck}
					if p := env.registry.OwnerProfile(deck); p != "" && p != env.profile {
						args = append([]string{"-p", p}, args...)
					}
					return args, "start", nil
				}
			}
		}
	}
	if sess.Harness != reader.HarnessClaude {
		return nil, "", fmt.Errorf("%s conversations are searchable but not resumable yet (no registered session owns this one); read it with recall show", sess.Harness)
	}
	if sess.Missing {
		return nil, "", errors.New("the transcript file is gone; nothing to resume (the index still has its text: recall show)")
	}
	if sess.CWD == "" {
		return nil, "", errors.New("the conversation recorded no working directory")
	}
	if info, err := os.Stat(sess.CWD); err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("working directory %s no longer exists", sess.CWD)
	}
	if !session.IsBareClaudeSessionUUID(sess.NativeID) {
		return nil, "", fmt.Errorf("conversation id %q is not a bare Claude session uuid", sess.NativeID)
	}
	if title == "" {
		title = sess.Title
	}
	if title == "" {
		title = "recall-" + shortNative(sess.NativeID)
	}
	args := []string{"add", "-t", title, "-c", "claude", "--resume-session", sess.NativeID, "--hint", "recalled_from=" + sess.NativeID}
	for _, name := range session.ConfiguredAccountNames(env.cfg) {
		if name == sess.Profile {
			args = append(args, "--account", name)
			break
		}
	}
	args = append(args, sess.CWD)
	return args, "add", nil
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			out[i] = strconv.Quote(a)
		} else {
			out[i] = a
		}
	}
	return out
}

// ---- status --------------------------------------------------------------

func handleRecallStatus(profile string, args []string) {
	fs := newRecallFlagSet("recall status")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall status [--json]\n\nIndex size, sources by state, pending bytes, roots being watched.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	env := openRecallEnv(profile, out)
	defer env.close()
	st, err := query.New(env.st, env.stateDB).Status(context.Background())
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	roots := make([]map[string]any, 0, len(env.roots))
	scratch := 0
	for _, r := range env.roots {
		if r.Harness == reader.HarnessClaude && r.Profile == "" {
			scratch++
			continue
		}
		roots = append(roots, map[string]any{"harness": r.Harness, "profile": r.Profile, "dir": r.Dir, "retention_days": r.RetentionDays})
	}
	queued := recall.QueueLen(env.queuePath)
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "status": st, "roots": roots, "worker_scratch_roots": scratch, "queued": queued,
			"config": map[string]any{"text_tier": env.cfg.Recall.GetTextTier(), "max_loadavg": env.cfg.Recall.GetMaxLoadAvg(),
				"keep_missing_days": env.cfg.Recall.GetKeepMissingDays(), "per_source_mb": env.cfg.Recall.GetPerSourceMB(),
				"harnesses": env.cfg.Recall.GetHarnesses(), "hook_sweep": env.cfg.Recall.GetHookSweep()}})
		return
	}
	fmt.Printf("recall.db  %s  (%s, schema %s)\n", st.DBPath, humanBytes(st.DBBytes), st.SchemaVersion)
	fmt.Printf("sessions   %d   messages %d   tool calls %d   cards %d (fts %d)\n", st.Sessions, st.Messages, st.ToolCalls, st.Cards, st.CardFTSRows)
	var states []string
	for _, k := range []string{"ok", "partial", "error", "missing", "quarantined"} {
		if n := st.Sources[k]; n > 0 {
			states = append(states, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(states) == 0 {
		states = []string{"none yet: run 'agent-deck recall backfill'"}
	}
	fmt.Printf("sources    %s\n", strings.Join(states, ", "))
	ratio := ""
	if st.IndexedBytes > 0 {
		ratio = fmt.Sprintf(" (%.1f%% of input)", 100*float64(st.DBBytes)/float64(st.IndexedBytes))
	}
	fmt.Printf("indexed    %s of transcripts%s; pending %s\n", humanBytes(st.IndexedBytes), ratio, humanBytes(st.PendingBytes))
	if st.ExpiringBytes > 0 {
		fmt.Printf("expiring   %s of indexed transcripts will be deleted by Claude's retention within 30 days (the index keeps their text)\n", humanBytes(st.ExpiringBytes))
	}
	if st.LastSweep > 0 {
		fmt.Printf("last sweep %s\n", time.Unix(st.LastSweep, 0).Local().Format("2006-01-02 15:04"))
	}
	var profiles, harnesses []string
	for k, n := range st.ByProfile {
		profiles = append(profiles, fmt.Sprintf("%s %d", k, n))
	}
	for k, n := range st.ByHarness {
		harnesses = append(harnesses, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(profiles)
	sort.Strings(harnesses)
	if len(harnesses) > 0 {
		fmt.Printf("harnesses  %s\n", strings.Join(harnesses, ", "))
	}
	if len(profiles) > 0 {
		fmt.Printf("profiles   %s\n", strings.Join(profiles, ", "))
	}
	if queued > 0 {
		fmt.Printf("queued     %d hook line(s) waiting for the next sweep\n", queued)
	}
	fmt.Printf("roots      %d harness dir(s), %d worker-scratch home(s)\n", len(roots), scratch)
	for _, r := range roots {
		if r["harness"] == reader.HarnessClaude {
			fmt.Printf("           %-9s %-10s %s (retention %d d)\n", r["harness"], r["profile"], r["dir"], r["retention_days"])
		} else {
			fmt.Printf("           %-9s %-10s %s\n", r["harness"], r["profile"], r["dir"])
		}
	}
	if st.Tombstones > 0 || st.FreePages > 0 {
		fmt.Printf("gc         %d tombstone(s), %d free page(s): 'agent-deck recall gc'\n", st.Tombstones, st.FreePages)
	}
}

// ---- backfill / sweep / gc / rebuild ------------------------------------

func recallBatchFlags(fs *flag.FlagSet) (jsonOutput, force, quiet *bool) {
	jsonOutput = fs.Bool("json", false, "Output as JSON")
	force = fs.Bool("force", false, "Run even while sessions are busy or the load is high")
	quiet = fs.Bool("quiet", false, "No per-file progress")
	return
}

func handleRecallBackfill(profile string, args []string) {
	fs := newRecallFlagSet("recall backfill")
	jsonOutput, force, quiet := recallBatchFlags(fs)
	since := fs.String("since", "", "Only transcripts modified since (30d, YYYY-MM-DD)")
	budget := fs.Duration("budget", 0, "Stop after this long (resumable; e.g. 5m)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: agent-deck recall backfill [--since 90d] [--budget 5m] [--force] [--json]

Index every transcript of every harness on this machine, newest first. Resumable: every
file keeps a cursor, so Ctrl-C keeps what was done. Refuses to start while a
managed session is running or the load average is above [recall].max_loadavg
unless --force is given.`)
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	env := openRecallEnv(profile, out)
	defer env.close()
	opts := env.ingestOptions(*force)
	if *since != "" {
		t, err := parseRecallSince(*since)
		if err != nil {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(2)
		}
		opts.Since = t
	}
	if *budget > 0 {
		opts.Budget = reader.NewBudget(*budget, 0)
	}
	runRecallSweep(env, out, opts, *jsonOutput, *quiet, "backfill")
}

func handleRecallSweep(profile string, args []string) {
	fs := newRecallFlagSet("recall sweep")
	jsonOutput, force, quiet := recallBatchFlags(fs)
	full := fs.Bool("full", false, "Re-verify every source's signatures, not only files whose size or mtime moved")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall sweep [--full] [--force] [--json]\n\nIndex what changed since the last sweep (appended tails, new files, vanished files). Same load gate as backfill.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	env := openRecallEnv(profile, out)
	defer env.close()
	opts := env.ingestOptions(*force)
	opts.Verify = *full
	runRecallSweep(env, out, opts, *jsonOutput, *quiet, "sweep")
}

func runRecallSweep(env *recallEnv, out *CLIOutput, opts ingest.Options, jsonOutput, quiet bool, verb string) {
	release := env.lock(out)
	defer release()
	runRecallSweepLocked(env, out, opts, jsonOutput, quiet, verb)
}

// runRecallSweepLocked is runRecallSweep for a caller already holding the
// sweep lock (rebuild keeps one lock over the reset and the sweep).
func runRecallSweepLocked(env *recallEnv, out *CLIOutput, opts ingest.Options, jsonOutput, quiet bool, verb string) {
	ctx, cancel := interruptibleContext()
	defer cancel()
	printed := false
	if !quiet && !jsonOutput {
		opts.Progress = func(p ingest.Progress) {
			printed = true
			state := ""
			switch {
			case p.Skipped != "":
				state = "  " + p.Skipped
			case p.Deferred:
				state = "  (continues next pass)"
			}
			fmt.Fprintf(os.Stderr, "\r[%d/%d] %s  %s read, %d msgs%s\033[K", p.Done, p.Total, filepath.Base(p.Path), humanBytes(p.BytesRead), p.Messages, state)
		}
	}
	res, err := ingest.New(env.st, opts).Sweep(ctx)
	if printed {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		if errors.Is(err, ingest.ErrGated) {
			out.Error(err.Error(), ErrCodeInvalidOperation)
			os.Exit(3)
		}
		if ctx.Err() != nil {
			if jsonOutput {
				out.printJSON(map[string]any{"success": false, "interrupted": true, "result": res})
			} else {
				fmt.Printf("%s interrupted; %s\n", verb, recallResultLine(res))
			}
			os.Exit(130)
		}
		out.Error(fmt.Sprintf("recall %s: %v", verb, err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if jsonOutput {
		out.printJSON(map[string]any{"success": true, "result": res, "db_bytes": store.FileSize(env.dbPath)})
		return
	}
	fmt.Printf("%s done: %s\n", verb, recallResultLine(res))
	if res.Deferred > 0 {
		fmt.Printf("  %d source(s) / %s left for the next pass (run again to continue)\n", res.Deferred, humanBytes(res.DeferredBytes))
	}
	if res.Quarantined > 0 {
		fmt.Printf("  %d copied transcript(s) quarantined (same conversation as an indexed file)\n", res.Quarantined)
	}
	fmt.Printf("  recall.db is %s\n", humanBytes(store.FileSize(env.dbPath)))
}

func recallResultLine(res ingest.Result) string {
	return fmt.Sprintf("%d source(s) seen, %d unchanged, %d parsed (%s), %d message(s), %d session(s) updated, %d missing, %d error(s), %.1fs",
		res.Discovered, res.Unchanged, res.Parsed, humanBytes(res.BytesRead), res.Messages, res.Sessions, res.Missing, res.Errors, float64(res.ElapsedMS)/1000)
}

func handleRecallGC(profile string, args []string) {
	fs := newRecallFlagSet("recall gc")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	keep := fs.Int("keep-days", -1, "Drop ledger rows and tombstones of transcripts missing longer than this (default [recall].keep_missing_days)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall gc [--keep-days N] [--json]\n\nDrop old tombstones, optimise the FTS index and hand freed pages back to the filesystem.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, false)
	env := openRecallEnv(profile, out)
	defer env.close()
	release := env.lock(out)
	defer release()
	days := *keep
	if days < 0 {
		days = env.cfg.Recall.GetKeepMissingDays()
	}
	res, err := ingest.New(env.st, env.ingestOptions(true)).GC(time.Duration(days) * 24 * time.Hour)
	if err != nil {
		out.Error("recall gc: "+err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	if *jsonOutput {
		out.printJSON(map[string]any{"success": true, "result": res})
		return
	}
	fmt.Printf("gc done: %d tombstone(s) and %d missing source(s) older than %d day(s) dropped; %d free page(s) reclaimed; recall.db %s -> %s\n",
		res.TombstonesDropped, res.SourcesDropped, days, res.FreePagesBefore-res.FreePagesAfter, humanBytes(res.BytesBefore), humanBytes(res.BytesAfter))
}

func handleRecallRebuild(profile string, args []string) {
	fs := newRecallFlagSet("recall rebuild")
	jsonOutput, force, quiet := recallBatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck recall rebuild [--force] [--json]\n\nDelete recall.db and index everything again. Hints, tags and links live in state.db and are untouched.")
		fs.PrintDefaults()
	}
	if !parseRecallFlags(fs, args) {
		return
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	env := openRecallEnv(profile, out)
	defer env.close()
	release := env.lock(out)
	defer release()
	opts := env.ingestOptions(*force)
	if err := opts.Gate.Check(); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(3)
	}
	if err := env.st.Reset(); err != nil {
		out.Error("recall rebuild: "+err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	runRecallSweepLocked(env, out, opts, *jsonOutput, *quiet, "rebuild")
}
