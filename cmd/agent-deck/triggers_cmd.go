package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/asheshgoplani/agent-deck/internal/agents"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// `agent-deck triggers` (issue #2702): everything that wakes a session.
// docs/triggers.md describes the commands; the JSON shape is the contract
// the Mac app reads.

const triggersUsage = `Usage: agent-deck triggers [list|scan|mute|unmute|run|fires] [options]

What wakes each session: launchd jobs, systemd units, cron, watchers,
conductor heartbeats, Telegram/Slack/Discord bridges and the command center.
Senders tag themselves with 'session send --source <name>'; a scan of the
user's launchd/systemd units finds the ones that have not fired yet.

Subcommands:
  list (default)        List triggers [--host H|all] [--session S] [--kind K] [--health H|problems] [--all] [--json]
  scan                  Rescan launchd/systemd user units that call agent-deck [--json]
  mute <id>             Receiver-side mute: sends are logged as muted, never typed [--for 24h] [--json]
  unmute <id>           End a mute [--json]
  run <id>              Start a scanned launchd job or systemd unit once now [--json]
  fires <id>            The recent fire log of one trigger [--limit N] [--json]

agent-deck never pauses, stops or disables a host job: a mute only drops the
sends at this end, and the job keeps running.`

// triggerScanMaxAge is how old the unit scan may get before `triggers list`
// rescans first.
const triggerScanMaxAge = 24 * time.Hour

// triggersRunExec runs the argv `triggers run` built; tests replace it.
var triggersRunExec = func(argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() //nolint:gosec // argv comes from triggers.RunCommand only
}

// triggersRemoteRunner is what the list needs from a remote: one command's stdout.
type triggersRemoteRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// newTriggersRemoteRunner builds the SSH runner; tests replace it.
var newTriggersRemoteRunner = func(name string, rc session.RemoteConfig) triggersRemoteRunner {
	return session.NewSSHRunner(name, rc)
}

func handleTriggers(profile string, args []string) {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		handleTriggersList(profile, args)
	case "scan":
		handleTriggersScan(profile, args)
	case "mute":
		handleTriggersMute(profile, args, true)
	case "unmute":
		handleTriggersMute(profile, args, false)
	case "run":
		handleTriggersRun(profile, args)
	case "fires":
		handleTriggersFires(profile, args)
	case "help":
		fmt.Println(triggersUsage)
	default:
		fmt.Fprintf(os.Stderr, "Unknown triggers subcommand: %s\n", sub)
		fmt.Fprintln(os.Stderr, triggersUsage)
		exitCLI(2)
	}
}

// triggersListJSON is `triggers --json` (CONTRACT v1).
type triggersListJSON struct {
	Triggers    []triggers.View   `json:"triggers"`
	Hosts       map[string]string `json:"hosts"`
	Count       int               `json:"count"`
	GeneratedAt time.Time         `json:"generated_at"`
}

// triggersListOptions are the list flags; filterArgs re-creates them for a
// forwarded remote list.
type triggersListOptions struct {
	host, session, kind, health string
	all                         bool
}

func (o triggersListOptions) filterArgs() []string {
	var out []string
	if o.session != "" {
		out = append(out, "--session", o.session)
	}
	if o.kind != "" {
		out = append(out, "--kind", o.kind)
	}
	if o.health != "" {
		out = append(out, "--health", o.health)
	}
	if o.all {
		out = append(out, "--all")
	}
	return out
}

var triggerHealthValues = []string{triggers.HealthOK, triggers.HealthFailing, triggers.HealthDeadTarget, triggers.HealthNeverFired, triggers.HealthUnknown, "problems"}

func handleTriggersList(profile string, args []string) {
	fs := flag.NewFlagSet("triggers list", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	host := fs.String("host", "", "This host's name, a remote name, or all (this host plus every configured remote)")
	sessionRef := fs.String("session", "", "What wakes this session (id or title)")
	kind := fs.String("kind", "", "Only this kind (launchd, systemd, cron, watcher, heartbeat, channel, interval, session, ssh, cli, job)")
	healthFlag := fs.String("health", "", "Only this health (ok, failing, dead-target, never-fired, unknown) or problems")
	all := fs.Bool("all", false, "Also show session-to-session and shell (cli) senders")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck triggers [list] [--host H|all] [--session S] [--kind K] [--health H|problems] [--all] [--json]")
		fmt.Println()
		fmt.Println("List everything that wakes a session on this host (and remotes with --host).")
		fmt.Println("A remote that is too old or unreachable shows as \"unknown\" in hosts, never guessed.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Other subcommands: scan, mute <id>, unmute <id>, run <id>, fires <id> (see agent-deck triggers <sub> --help)")
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() > 0 {
		out.Error(fmt.Sprintf("unexpected argument %q", fs.Arg(0)), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	if *healthFlag != "" && !containsString(triggerHealthValues, *healthFlag) {
		out.Error(fmt.Sprintf("unknown --health %q (one of %s)", *healthFlag, strings.Join(triggerHealthValues, ", ")), ErrCodeInvalidOperation)
		exitCLI(2)
	}
	opts := triggersListOptions{host: *host, session: *sessionRef, kind: *kind, health: *healthFlag, all: *all}

	localHost := triggers.Hostname()
	includeLocal, remotes, err := triggersHostPlan(opts.host, localHost)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(2)
	}

	doc := triggersListJSON{Triggers: []triggers.View{}, Hosts: map[string]string{}, GeneratedAt: time.Now().UTC().Truncate(time.Second)}
	header := ""
	if includeLocal {
		storage, instances, _, err := loadSessionData(profile)
		if err != nil {
			out.Error(err.Error(), ErrCodeNotFound)
			exitCLI(1)
		}
		filter := triggers.Filter{Kind: opts.kind, Health: opts.health, All: opts.all}
		skipLocal := false
		if opts.session != "" {
			inst, errMsg, errCode := ResolveSession(opts.session, instances)
			switch {
			case inst != nil:
				filter.SessionID = inst.ID
				header = fmt.Sprintf("What wakes %s (%s)", inst.Title, shortTriggerID(inst.ID))
			case len(remotes) == 0:
				out.Error(errMsg, errCode)
				if errCode == ErrCodeNotFound {
					exitCLI(2)
				}
				exitCLI(1)
			default:
				// --host all: the session may live on a remote only.
				skipLocal = true
				header = "What wakes " + opts.session
			}
		}
		db := storage.GetDB()
		maybeRescanTriggers(db, profile)
		views, err := triggers.Load(db, session.TriggerResolver(session.GetEffectiveProfile(profile), instances), localHost, time.Now())
		if err != nil {
			out.Error(fmt.Sprintf("read triggers: %v", err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		if !skipLocal {
			doc.Triggers = append(doc.Triggers, filter.Apply(views)...)
		}
		doc.Hosts[localHost] = "ok"
	} else if opts.session != "" {
		header = "What wakes " + opts.session
	}
	if len(remotes) > 0 {
		rows, hosts := collectRemoteTriggers(remotes, opts.filterArgs())
		doc.Triggers = append(doc.Triggers, rows...)
		for name, state := range hosts {
			doc.Hosts[name] = state
		}
	}
	doc.Count = len(doc.Triggers)
	out.Print(renderTriggersTable(doc, header, time.Now()), doc)
}

// triggersHostPlan turns --host into "list this host" plus the remotes to ask.
func triggersHostPlan(host, localHost string) (bool, map[string]session.RemoteConfig, error) {
	if host == "" || host == localHost || host == "local" {
		return true, nil, nil
	}
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return false, nil, err
	}
	if host == "all" {
		return true, cfg.Remotes, nil
	}
	if rc, ok := cfg.Remotes[host]; ok {
		return false, map[string]session.RemoteConfig{host: rc}, nil
	}
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	return false, nil, fmt.Errorf("unknown host %q: this host is %q; remotes: %s (or all)", host, localHost, strings.Join(names, ", "))
}

// collectRemoteTriggers forwards `triggers --json` to every remote at once.
// A remote that fails, refuses the command (too old) or answers something
// that is not the triggers JSON is "unknown" with no rows: never guessed.
func collectRemoteTriggers(remotes map[string]session.RemoteConfig, filterArgs []string) ([]triggers.View, map[string]string) {
	type answer struct {
		name  string
		rows  []triggers.View
		state string
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	results := make(chan answer, len(remotes))
	var wg sync.WaitGroup
	for name, rc := range remotes {
		wg.Add(1)
		go func(name string, rc session.RemoteConfig) {
			defer wg.Done()
			rows, state := fetchRemoteTriggers(ctx, newTriggersRemoteRunner(name, rc), name, filterArgs)
			results <- answer{name, rows, state}
		}(name, rc)
	}
	wg.Wait()
	close(results)
	var rows []triggers.View
	hosts := map[string]string{}
	var answers []answer
	for a := range results {
		answers = append(answers, a)
	}
	sort.Slice(answers, func(i, j int) bool { return answers[i].name < answers[j].name })
	for _, a := range answers {
		hosts[a.name] = a.state
		rows = append(rows, a.rows...)
	}
	return rows, hosts
}

func fetchRemoteTriggers(ctx context.Context, runner triggersRemoteRunner, name string, filterArgs []string) ([]triggers.View, string) {
	out, err := runner.Run(ctx, append([]string{"triggers", "--json"}, filterArgs...)...)
	if err != nil {
		// With --session, a remote that has no such session says so
		// (NOT_FOUND): it answered, and nothing there wakes it.
		var refusal struct {
			Code string `json:"code"`
		}
		if containsString(filterArgs, "--session") && json.Unmarshal(out, &refusal) == nil && refusal.Code == ErrCodeNotFound {
			return nil, "ok"
		}
		return nil, triggers.HealthUnknown
	}
	var doc struct {
		Triggers *[]triggers.View `json:"triggers"`
	}
	if json.Unmarshal(out, &doc) != nil || doc.Triggers == nil {
		return nil, triggers.HealthUnknown
	}
	rows := *doc.Triggers
	for i := range rows {
		// Rows are keyed by the remote's name here, so --host and "hosts" agree.
		rows[i].Host = name
	}
	return rows, "ok"
}

// maybeRescanTriggers runs the bounded unit scan when the last one is older
// than a day. Best-effort: a failed scan leaves the previous rows.
func maybeRescanTriggers(db *statedb.StateDB, profile string) {
	if db == nil || !triggers.ScanStale(db, triggerScanMaxAge, time.Now()) {
		return
	}
	_, _ = runTriggerScan(db, profile)
}

func runTriggerScan(db *statedb.StateDB, profile string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	found := triggers.Scan(triggers.DefaultScanDirs(home))
	return len(found), triggers.ApplyScan(db, events.CurrentProfile(), triggers.Hostname(), found)
}

func handleTriggersScan(profile string, args []string) {
	fs := flag.NewFlagSet("triggers scan", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck triggers scan [--json]")
		fmt.Println()
		fmt.Println("Rescan ~/Library/LaunchAgents and ~/.config/systemd/user for units whose")
		fmt.Println("command (or the script it runs) calls agent-deck, then print the list.")
		fmt.Println("At most 500 files and 256 KB per script are read. Nothing is changed on the host.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	db := storage.GetDB()
	if _, err := runTriggerScan(db, profile); err != nil {
		out.Error(fmt.Sprintf("scan: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	localHost := triggers.Hostname()
	views, err := triggers.Load(db, session.TriggerResolver(session.GetEffectiveProfile(profile), instances), localHost, time.Now())
	if err != nil {
		out.Error(fmt.Sprintf("read triggers: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	doc := triggersListJSON{Triggers: triggers.Filter{}.Apply(views), Hosts: map[string]string{localHost: "ok"}, GeneratedAt: time.Now().UTC().Truncate(time.Second)}
	if doc.Triggers == nil {
		doc.Triggers = []triggers.View{}
	}
	doc.Count = len(doc.Triggers)
	out.Print(renderTriggersTable(doc, "", time.Now()), doc)
}

// openTriggerDB loads the profile's state DB for a single-trigger command
// and checks the trigger exists (NOT_FOUND, exit 2, otherwise).
func openTriggerDB(profile, id string, out *CLIOutput) (*statedb.StateDB, []*session.Instance) {
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	db := storage.GetDB()
	row, err := db.GetTrigger(id)
	if err != nil {
		out.Error(fmt.Sprintf("read trigger: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if row == nil {
		out.Error(fmt.Sprintf("no trigger %q (see agent-deck triggers --all)", id), ErrCodeNotFound)
		exitCLI(2)
	}
	return db, instances
}

func triggerIDArg(fs *flag.FlagSet, out *CLIOutput) string {
	if fs.NArg() != 1 {
		fs.Usage()
		out.Error("one trigger id is required (see agent-deck triggers --all)", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	return fs.Arg(0)
}

func handleTriggersMute(profile string, args []string, mute bool) {
	name := "unmute"
	if mute {
		name = "mute"
	}
	fs := flag.NewFlagSet("triggers "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	var forFlag *time.Duration
	if mute {
		forFlag = fs.Duration("for", triggers.DefaultMute, "How long the mute lasts")
	}
	fs.Usage = func() {
		if mute {
			fmt.Println("Usage: agent-deck triggers mute <id> [--for 24h] [--json]")
			fmt.Println()
			fmt.Println("Mute a trigger at the receiving end: its sends are accepted, logged with")
			fmt.Println("outcome muted and never typed into the pane. The host job keeps running;")
			fmt.Println("agent-deck never pauses, stops or disables it. The mute expires on its own.")
		} else {
			fmt.Println("Usage: agent-deck triggers unmute <id> [--json]")
			fmt.Println()
			fmt.Println("End a mute: the trigger's next send is delivered again.")
		}
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	id := triggerIDArg(fs, out)
	var until time.Time
	if mute {
		if *forFlag <= 0 {
			out.Error("--for must be positive", ErrCodeInvalidOperation)
			exitCLI(2)
		}
		until = time.Now().Add(*forFlag).UTC().Truncate(time.Second)
	}
	db, _ := openTriggerDB(profile, id, out)
	if err := triggers.SetMute(db, events.CurrentProfile(), id, until); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	data := map[string]interface{}{"success": true, "trigger_id": id, "muted": mute}
	if mute {
		data["muted_until"] = until
		out.Success(fmt.Sprintf("Muted %s until %s (its sends are logged, not typed; the host job keeps running)", id, until.Local().Format("2006-01-02 15:04")), data)
		return
	}
	out.Success(fmt.Sprintf("Unmuted %s", id), data)
}

// runTrigger starts a scanned unit once through runExec. It never pauses,
// stops or disables anything (triggers.RunCommand only builds start argv).
func runTrigger(db *statedb.StateDB, resolve triggers.Resolver, id string, uid int, runExec func([]string) ([]byte, error)) (map[string]interface{}, error) {
	views, err := triggers.Load(db, resolve, triggers.Hostname(), time.Now())
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		if v.ID != id {
			continue
		}
		argv, err := triggers.RunCommand(v, uid)
		if err != nil {
			return nil, err
		}
		output, runErr := runExec(argv)
		data := map[string]interface{}{"success": runErr == nil, "trigger_id": id, "command": argv, "output": strings.TrimSpace(string(output))}
		if runErr != nil {
			return data, fmt.Errorf("%s: %v", strings.Join(argv, " "), runErr)
		}
		return data, nil
	}
	return nil, fmt.Errorf("no trigger %q", id)
}

func handleTriggersRun(profile string, args []string) {
	fs := flag.NewFlagSet("triggers run", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck triggers run <id> [--json]")
		fmt.Println()
		fmt.Println("Start a scanned launchd job or systemd user unit once now")
		fmt.Println("(systemctl --user start <unit>.service / launchctl kickstart gui/<uid>/<label>).")
		fmt.Println("Only scanned launchd/systemd triggers can run. Nothing is paused, stopped or disabled.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	id := triggerIDArg(fs, out)
	db, instances := openTriggerDB(profile, id, out)
	data, err := runTrigger(db, session.TriggerResolver(session.GetEffectiveProfile(profile), instances), id, os.Getuid(), triggersRunExec)
	if err != nil {
		out.ErrorWithData(err.Error(), ErrCodeInvalidOperation, data)
		exitCLI(1)
	}
	out.Success(fmt.Sprintf("Started %s once (%s)", id, strings.Join(data["command"].([]string), " ")), data)
}

// triggerFireJSON is one entry of `triggers fires --json`.
type triggerFireJSON struct {
	TS        time.Time `json:"ts"`
	Target    string    `json:"target"`
	SessionID string    `json:"session_id"`
	Outcome   string    `json:"outcome"`
	Line      string    `json:"line"`
	Noop      bool      `json:"noop"`
}

func handleTriggersFires(profile string, args []string) {
	fs := flag.NewFlagSet("triggers fires", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	limit := fs.Int("limit", 50, "Newest fires to show")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck triggers fires <id> [--limit N] [--json]")
		fmt.Println()
		fmt.Println("The recent fire log of one trigger, newest first: when, at which session, the")
		fmt.Println("outcome, and its first line (sanitized, secrets redacted, 80 cells). Kept 7 days,")
		fmt.Println("at most 2000 fires per trigger.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, false)
	id := triggerIDArg(fs, out)
	if *limit <= 0 {
		out.Error("--limit must be positive", ErrCodeInvalidOperation)
		exitCLI(2)
	}
	db, _ := openTriggerDB(profile, id, out)
	rows, err := db.ListTriggerFires(id, *limit)
	if err != nil {
		out.Error(fmt.Sprintf("read fires: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	fires := make([]triggerFireJSON, 0, len(rows))
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("No fires in the last 7 days.\n")
	} else {
		fmt.Fprintf(&b, "%s%s%s%s\n", padCells("TIME", 20), padCells("OUTCOME", 13), padCells("TARGET", 24), "LINE")
	}
	for _, r := range rows {
		fires = append(fires, triggerFireJSON{TS: r.TS.UTC(), Target: r.Target, SessionID: r.TargetSessionID, Outcome: r.Outcome, Line: r.Line, Noop: r.Noop})
		line := r.Line
		if r.Noop {
			line = "(same as before) " + line
		}
		fmt.Fprintf(&b, "%s%s%s%s\n", padCells(r.TS.Local().Format("2006-01-02 15:04:05"), 20), padCells(r.Outcome, 13), padCells(r.Target, 24), line)
	}
	out.Print(b.String(), map[string]interface{}{"trigger_id": id, "fires": fires, "count": len(fires)})
}

// --- human table ---

var triggerHealthLabels = map[string]string{
	triggers.HealthOK:         "ok",
	triggers.HealthFailing:    "failing",
	triggers.HealthDeadTarget: "dead target",
	triggers.HealthNeverFired: "never fired",
	triggers.HealthUnknown:    "unknown",
}

// padCells cuts s to n-1 display cells (marking the cut with …) and pads it
// to n, so a column always keeps one space before the next.
func padCells(s string, n int) string {
	s = agents.SanitizeForDisplay(s)
	if runewidth.StringWidth(s) > n-1 {
		s = runewidth.Truncate(s, n-1, "…")
	}
	return s + strings.Repeat(" ", n-runewidth.StringWidth(s))
}

func triggerAgo(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	d := now.Sub(*t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func shortTriggerID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func renderTriggersTable(doc triggersListJSON, header string, now time.Time) string {
	var b strings.Builder
	if header != "" {
		b.WriteString(agents.SanitizeForDisplay(header) + "\n\n")
	}
	if len(doc.Triggers) == 0 {
		if header != "" {
			b.WriteString("Nothing wakes this session.\n")
		} else {
			b.WriteString("No triggers.\n")
		}
	} else {
		fmt.Fprintf(&b, "%s%s%s%s%s%s%s%s\n", padCells("HOST", 9), padCells("KIND", 10), padCells("NAME", 20), padCells("WAKES", 26),
			padCells("EVERY", 14), padCells("LAST", 11), padCells("HEALTH", 13), "NOISE")
		for _, v := range doc.Triggers {
			wakes := v.Target.AsWritten
			if v.Target.Resolved && v.Target.Title != "" {
				wakes = v.Target.Title
			}
			switch {
			case wakes == "":
				wakes = "–"
			case v.Health == triggers.HealthDeadTarget:
				wakes = agents.SanitizeForDisplay(wakes)
				if runewidth.StringWidth(wakes) > 23 {
					wakes = runewidth.Truncate(wakes, 23, "…")
				}
				wakes += " ✗"
			}
			every := v.Schedule
			if every == "" {
				every = "–"
			}
			health := triggerHealthLabels[v.Health]
			if health == "" {
				health = v.Health
			}
			noise := "–"
			if v.NoopShare24h != nil {
				noise = fmt.Sprintf("%d%%", int(*v.NoopShare24h*100+0.5))
			}
			if v.Muted && v.MutedUntil != nil {
				noise += "  muted until " + v.MutedUntil.Local().Format("Jan 2 15:04")
			}
			fmt.Fprintf(&b, "%s%s%s%s%s%s%s%s\n", padCells(v.Host, 9), padCells(v.Kind, 10), padCells(v.Source, 20), padCells(wakes, 26),
				padCells(every, 14), padCells(triggerAgo(v.LastFire, now), 11), padCells(health, 13), noise)
		}
	}
	s := triggers.Summarize(doc.Triggers)
	b.WriteString("\n")
	footer := fmt.Sprintf("%d triggers · %d dead targets · %d failing", s.Total, s.DeadTargets, s.Failing)
	if header == "" {
		footer += ` · run "agent-deck triggers --session NAME" for one session`
	}
	b.WriteString(footer + "\n")
	var unknown []string
	for name, state := range doc.Hosts {
		if state != "ok" {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		fmt.Fprintf(&b, "host %s: unknown (its agent-deck is too old for triggers, or it did not answer)\n", agents.SanitizeForDisplay(name))
	}
	return b.String()
}
