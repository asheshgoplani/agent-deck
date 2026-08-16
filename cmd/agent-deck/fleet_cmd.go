package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/fleet"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleFleet dispatches `agent-deck fleet <status|recover>`.
//
// The command productizes the hand-rolled recovery sweep the maintainer ran
// twice on 2026-07-26 after the whole ~65-session fleet died: find the sessions
// whose panes are gone, restart them ONE AT A TIME with a few seconds between
// boots, check each came up before starting the next, and stop early if the
// failures look systemic. See internal/fleet for the mechanics.
func handleFleet(profile string, args []string) {
	if len(args) == 0 {
		printFleetHelp()
		os.Exit(1)
	}
	switch args[0] {
	case "status":
		handleFleetStatus(profile, args[1:])
	case "recover":
		handleFleetRecover(profile, args[1:])
	case "help", "--help", "-h":
		printFleetHelp()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown fleet command: %s\n", args[0])
		printFleetHelp()
		os.Exit(1)
	}
}

func printFleetHelp() {
	fmt.Println("Usage: agent-deck fleet <command> [options]")
	fmt.Println()
	fmt.Println("Detect and recover from a fleet-wide session death (every managed")
	fmt.Println("pane gone at once: a killed tmux server, a host reboot, or an auth")
	fmt.Println("cascade that made the agents exit).")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  status     Report which sessions are down (read-only, never writes)")
	fmt.Println("  recover    Restart the down sessions sequentially, verifying each boot")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  agent-deck fleet status")
	fmt.Println("  agent-deck fleet recover                 # plan only (dry run)")
	fmt.Println("  agent-deck fleet recover --yes           # actually recover")
	fmt.Println("  agent-deck fleet recover --yes --spacing 8s --limit 10")
	fmt.Println("  agent-deck fleet recover --yes --group agent-deck")
}

// fleetDetectorFlags are the scan knobs shared by `status` and `recover`, so a
// plan and the recovery it authorizes always classify sessions identically.
type fleetDetectorFlags struct {
	group         *string
	includeIdle   *bool
	confirmProbes *int
	confirmDelay  *time.Duration
	minDead       *int
	deadFraction  *float64
}

func registerFleetDetectorFlags(fs *flag.FlagSet) fleetDetectorFlags {
	return fleetDetectorFlags{
		group:       fs.String("group", "", "Only consider sessions in this group path (and its descendants)"),
		includeIdle: fs.Bool("include-idle", false, "Also treat status=idle sessions as down (off by default: idle is also the status of a session that was never started)"),
		confirmProbes: fs.Int("confirm-probes", fleet.DefaultConfirmProbes,
			"Independent tmux probes that must agree a session is gone before it counts as down"),
		confirmDelay: fs.Duration("confirm-delay", fleet.DefaultConfirmDelay, "Delay between confirming probes"),
		minDead:      fs.Int("min-dead", fleet.DefaultMinDead, "Minimum down sessions for the shape to be reported as a mass death"),
		deadFraction: fs.Float64("dead-fraction", fleet.DefaultDeadFraction,
			"Share of should-be-alive sessions that must be down for a mass-death verdict"),
	}
}

func (f fleetDetectorFlags) detector() *fleet.Detector {
	d := fleet.NewDetector()
	d.Group = *f.group
	d.IncludeIdle = *f.includeIdle
	d.ConfirmProbes = *f.confirmProbes
	d.ConfirmDelay = *f.confirmDelay
	d.MinDead = *f.minDead
	d.DeadFraction = *f.deadFraction
	return d
}

// handleFleetStatus reports the assessment and exits. It performs no writes at
// all — safe to run on a live host, and the thing to run first when the TUI
// looks wrong.
func handleFleetStatus(profile string, args []string) {
	fs := flag.NewFlagSet("fleet status", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	groupByCredential := fs.Bool("group-by-credential", false, groupByCredentialFlagHelp())
	det := registerFleetDetectorFlags(fs)

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck fleet status [options]")
		fmt.Println()
		fmt.Println("Report sessions the registry believes are alive whose tmux session is")
		fmt.Println("gone. Read-only: no restarts, no writes.")
		fmt.Println()
		fmt.Println("With --group-by-credential the report additionally groups every")
		fmt.Println("auth-401-held session by WHICH credential is dead, so one dead token")
		fmt.Println("reads as one line instead of N per-session failures. Opt-in: without")
		fmt.Println("the flag the output is exactly the per-session view it has always been.")
		fmt.Println("The grouping is computed locally and never leaves the machine.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	// See handleFleetRecover: -q must not silence a --json payload.
	out := NewCLIOutput(*jsonOutput, (*quiet || *quietShort) && !*jsonOutput)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	as := det.detector().Assess(instances)
	if *jsonOutput {
		payload := fleetStatusJSON(as)
		// Added ONLY under the flag: a conductor polling the default payload
		// must keep seeing the exact keys it sees today.
		if *groupByCredential {
			payload["auth_credentials"] = fleetAuthCredentialsJSON(authCredentialSummary(instances, *det.group))
		}
		out.Success("", payload)
		return
	}
	fmt.Print(formatFleetStatus(as))
	// Printed after the assessment, and independent of it: an auth-held session
	// whose pane is still up showing the banner is NOT "down", so a fleet with
	// zero down sessions can still have a dead credential behind it.
	if *groupByCredential {
		fmt.Print("\n" + authCredentialSummary(instances, *det.group).Format())
	}
}

// groupByCredentialFlagHelp is shared by `fleet status` and `fleet recover` so
// the two cannot describe the same opt-in differently.
//
// A function rather than a const because gosec's G101 matches any identifier
// containing "cred" that is assigned a string literal. This one is flag help
// text, not a credential — a function return sidesteps the rule honestly
// instead of suppressing it.
func groupByCredentialFlagHelp() string {
	return "Group auth-401-held sessions by which credential is dead and report one escalation per credential (opt-in, local-only; off by default the per-session view is unchanged)"
}

// authCredentialSummary is the one place the CLI builds the credential view, so
// the human and --json paths can never group differently.
//
// group is the --group filter, threaded through so the credential view honours
// the same scoping contract as the assessment printed above it. Reporting
// fleet-wide credential state under `--group X` would be wrong in exactly the
// situation the operator reached for --group to clarify.
func authCredentialSummary(instances []*session.Instance, group string) fleet.AuthCredentialSummary {
	g := fleet.NewAuthCredentialGrouper()
	g.Group = group
	return g.Summarize(instances)
}

// fleetAuthCredentialsJSON renders the credential grouping.
//
// Only identifiers are emitted: account slot names and the resolved config
// directory. No token, no credential file content, and nothing derived from
// either — see internal/fleet/authcred.go for the boundary this holds.
func fleetAuthCredentialsJSON(sum fleet.AuthCredentialSummary) map[string]interface{} {
	groups := make([]map[string]interface{}, 0, len(sum.Groups))
	for _, g := range sum.Groups {
		sessions := make([]map[string]interface{}, 0, len(g.Sessions))
		for _, s := range g.Sessions {
			entry := map[string]interface{}{
				"id":    s.ID,
				"title": s.Title,
			}
			if s.Account != "" {
				entry["account"] = s.Account
			}
			if s.Reason != "" {
				entry["reason"] = s.Reason
			}
			sessions = append(sessions, entry)
		}
		group := map[string]interface{}{
			"key":        g.Credential.Key,
			"attributed": g.Credential.Attributed,
			"accounts":   g.Accounts,
			"held":       len(g.Sessions),
			"recovered":  g.Recovered,
			"escalation": g.Escalation(),
			"sessions":   sessions,
		}
		if g.Credential.Attributed {
			group["config_dir"] = g.Credential.ConfigDir
			// host is always present for an attributed store ("local" or the
			// SSH destination) so a machine consumer never has to infer
			// locality from a missing key.
			group["host"] = g.Credential.HostLabel()
			group["remote"] = g.Credential.IsRemote()
		}
		groups = append(groups, group)
	}
	return map[string]interface{}{
		"held":         sum.Held,
		"credentials":  sum.Credentials,
		"unattributed": sum.Unattributed,
		"recovered":    sum.Recovered,
		"groups":       groups,
	}
}

// handleFleetRecover plans (default) or runs the recovery sweep.
func handleFleetRecover(profile string, args []string) {
	fs := flag.NewFlagSet("fleet recover", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	yes := fs.Bool("yes", false, "Actually restart the down sessions (without this the command only prints the plan)")
	dryRun := fs.Bool("dry-run", false, "Force plan-only mode even with --yes")
	spacing := fs.Duration("spacing", fleet.DefaultSpacing, "Gap between consecutive boots (0 disables spacing — not recommended)")
	jitter := fs.Float64("jitter", fleet.DefaultJitter, "Random +/- fraction applied to each gap (0 disables)")
	limit := fs.Int("limit", 0, "Restart at most N sessions (0 = all)")
	verifyTimeout := fs.Duration("verify-timeout", fleet.DefaultVerifyTimeout, "How long to wait for one session to prove it booted")
	verifyPoll := fs.Duration("verify-poll", fleet.DefaultVerifyPoll, "Verification poll interval")
	maxFailures := fs.Int("max-failures", fleet.DefaultMaxFailures, "Halt after this many consecutive failed restarts")
	maxDeadBoots := fs.Int("max-dead-boots", fleet.DefaultMaxDeadBoots,
		"Halt after this many consecutive sessions restart and then die immediately (pane gone; 0 disables)")
	authHaltAfter := fs.Int("auth-halt-after", fleet.DefaultAuthHaltAfter, "Halt after this many sessions boot into an auth failure (0 disables the auth breaker)")
	groupByCredential := fs.Bool("group-by-credential", false, groupByCredentialFlagHelp())
	det := registerFleetDetectorFlags(fs)

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck fleet recover [options]")
		fmt.Println()
		fmt.Println("Restart every session whose tmux session is gone but whose registry")
		fmt.Println("status claims it should be running. Sessions are restarted ONE AT A")
		fmt.Println("TIME with --spacing between boots, and each boot is verified before the")
		fmt.Println("next one starts.")
		fmt.Println()
		fmt.Println("DRY RUN BY DEFAULT: without --yes the command prints the plan and exits")
		fmt.Println("without restarting anything.")
		fmt.Println()
		fmt.Println("The sweep halts early when the failures look systemic: N consecutive")
		fmt.Println("failed restarts (--max-failures), N consecutive sessions that restart and")
		fmt.Println("then die immediately (--max-dead-boots), or sessions booting into an auth")
		fmt.Println("failure (--auth-halt-after) — restarting the rest of the fleet against a")
		fmt.Println("broken credential only deepens the cascade.")
		fmt.Println()
		fmt.Println("With --group-by-credential the auth halt names WHICH credential is dead")
		fmt.Println("and reports one escalation per credential instead of one count for the")
		fmt.Println("sweep. Opt-in: without the flag the halt message is unchanged.")
		fmt.Println()
		fmt.Println("Sessions the operator stopped or queued, and archived sessions, are")
		fmt.Println("never touched.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	// --json is machine output and must not be silenced by -q: a quiet
	// CLIOutput drops Success entirely, so `--json -q` would print nothing at
	// all and a wrapper would read the empty payload as a clean no-op.
	out := NewCLIOutput(*jsonOutput, quietMode && !*jsonOutput)

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	as := det.detector().Assess(instances)

	cfg := fleetRecoverConfig{
		plan:              *dryRun || !*yes,
		spacing:           *spacing,
		jitter:            *jitter,
		limit:             *limit,
		maxFailures:       *maxFailures,
		maxDeadBoots:      *maxDeadBoots,
		authHaltAfter:     *authHaltAfter,
		groupByCredential: *groupByCredential,
		verifyTimeout:     *verifyTimeout,
		verifyPoll:        *verifyPoll,
	}
	plan := cfg.plan
	rec := cfg.recoverer()
	rec.Persist = storage.PersistRecoveredInstances
	// Kept so the credential grouping the gate collected during the sweep can be
	// reported structurally, instead of leaving a machine caller to parse it out
	// of halt_reason. nil when the operator disabled the breaker.
	authGate, _ := rec.AuthGate.(*fleet.SubstateAuthGate)
	if !plan && !*jsonOutput && !quietMode {
		rec.Progress = func(index, total int, c fleet.Candidate) {
			fmt.Printf("[%d/%d] restarting %s...\n", index, total, c.Title())
		}
	}

	summary := rec.Recover(as)

	// Opt-in only, and empty unless the gate actually observed auth failures.
	var credentials fleet.AuthCredentialSummary
	if *groupByCredential && authGate != nil {
		credentials = authGate.AuthFailuresByCredential()
	}

	switch {
	case *jsonOutput:
		payload := fleetRecoverJSON(summary, plan)
		if *groupByCredential && authGate != nil {
			payload["auth_credentials"] = fleetAuthCredentialsJSON(credentials)
		}
		out.Success("", payload)
	case quietMode:
		fmt.Println(summary.Format())
	default:
		fmt.Print(formatFleetRecover(summary, plan))
		// A sweep can end with auth failures that never reached the halt
		// threshold, so this must not be left to HALTED: alone.
		if credentials.Held > 0 {
			fmt.Print("\n" + credentials.Format())
		}
	}
	// A halted sweep is not a success: exit non-zero so a wrapper script or
	// conductor notices without parsing anything. This deliberately covers the
	// --json path too — a machine caller is the one most likely to check only
	// the exit status, so that path must not be the one that reports a halted
	// fleet as success.
	if summary.Halted {
		os.Exit(1)
	}
}

// fleetRecoverConfig is the parsed flag set for a sweep. It exists so the
// wiring of flags → recoverer (including the two safety-critical mappings:
// "no --yes means plan only" and "--spacing 0 is the explicit opt-out") is a
// pure function the tests can assert on.
type fleetRecoverConfig struct {
	plan              bool
	spacing           time.Duration
	jitter            float64
	limit             int
	maxFailures       int
	maxDeadBoots      int
	authHaltAfter     int
	groupByCredential bool
	verifyTimeout     time.Duration
	verifyPoll        time.Duration
}

func (c fleetRecoverConfig) recoverer() *fleet.Recoverer {
	rec := fleet.NewRecoverer()
	rec.DryRun = c.plan
	rec.Spacing = c.spacing
	// Distinguish "flag omitted" from "operator asked for no gap": the
	// recoverer treats Spacing<=0 as unset and falls back to the default, so
	// only this explicit opt-out can remove the gap.
	rec.NoSpacing = c.spacing == 0
	rec.Jitter = c.jitter
	rec.Limit = c.limit
	rec.MaxFailures = c.maxFailures
	// The recoverer reads 0 as "unset, use the default" and a negative value as
	// the explicit opt-out, so `--max-dead-boots 0` has to be translated rather
	// than passed through — otherwise asking for the brake to be off would
	// silently re-enable it at the default.
	if c.maxDeadBoots <= 0 {
		rec.MaxDeadBoots = -1
	} else {
		rec.MaxDeadBoots = c.maxDeadBoots
	}
	if c.authHaltAfter <= 0 {
		// --auth-halt-after 0 disables the breaker outright, so there is no
		// gate left to group on. Asking for grouping does NOT resurrect it:
		// re-enabling a safety brake the operator explicitly turned off would
		// be a surprising side effect of a reporting flag.
		rec.AuthGate = nil
	} else {
		rec.AuthGate = &fleet.SubstateAuthGate{
			HaltAfter:         c.authHaltAfter,
			GroupByCredential: c.groupByCredential,
		}
	}
	verifier := fleet.NewVerifier()
	verifier.Timeout = c.verifyTimeout
	verifier.Poll = c.verifyPoll
	rec.Verify = verifier.Verify
	return rec
}

// formatFleetStatus renders the human-readable assessment.
func formatFleetStatus(as fleet.Assessment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fleet: %d sessions — %d alive, %d down, %d not running\n",
		as.Total, as.Alive, as.Down, as.Skipped)
	if as.MassDeath {
		fmt.Fprintf(&b, "MASS DEATH detected (%d of %d should-be-alive sessions are gone)\n",
			as.Down, as.Down+as.Alive)
	}
	if as.Down == 0 {
		fmt.Fprintln(&b, "Nothing to recover.")
		return b.String()
	}
	fmt.Fprintln(&b, "\nDown sessions:")
	for _, c := range as.Candidates {
		fmt.Fprintf(&b, "  %-40s status=%-8s group=%s\n", truncateFleetTitle(c.Title(), 40), c.Status, groupOrRoot(c.Instance))
	}
	fmt.Fprintf(&b, "\nRun `agent-deck fleet recover` to see the recovery plan, then add --yes to run it.\n")
	return b.String()
}

// formatFleetRecover renders the human-readable sweep result.
func formatFleetRecover(s fleet.Summary, plan bool) string {
	var b strings.Builder
	as := s.Assessment
	fmt.Fprintf(&b, "Fleet: %d sessions — %d alive, %d down, %d not running\n",
		as.Total, as.Alive, as.Down, as.Skipped)
	if as.MassDeath {
		fmt.Fprintf(&b, "MASS DEATH detected (%d of %d should-be-alive sessions are gone)\n",
			as.Down, as.Down+as.Alive)
	}
	if as.Down == 0 {
		fmt.Fprintln(&b, "Nothing to recover.")
		return b.String()
	}

	if plan {
		fmt.Fprintf(&b, "\nDRY RUN — nothing was restarted. Plan (%d session(s), sequential):\n", s.Attempted)
	} else {
		fmt.Fprintf(&b, "\nRecovery results:\n")
	}
	for i, r := range s.Results {
		switch {
		case plan && r.Outcome == fleet.OutcomePlanned:
			fmt.Fprintf(&b, "  %2d. %-40s wait=%s\n", i+1, truncateFleetTitle(r.Title, 40), r.WaitedBefore)
		case plan:
			// Down, but the plan will not touch it (--limit). Numbering these
			// alongside the planned boots would misreport the sweep's scope.
			fmt.Fprintf(&b, "      %-40s not in this run: %s\n", truncateFleetTitle(r.Title, 40), r.Reason)
		case r.Outcome == fleet.OutcomeRecovered:
			fmt.Fprintf(&b, "  ok         %-40s status=%s in %s\n", truncateFleetTitle(r.Title, 40), r.Report.Status, r.Report.Elapsed.Round(time.Second))
		case r.Outcome == fleet.OutcomeUnverified:
			fmt.Fprintf(&b, "  unverified %-40s pane_alive=%t status=%s substate=%s\n",
				truncateFleetTitle(r.Title, 40), r.Report.PaneAlive, r.Report.Status, r.Report.Substate)
		case r.Outcome == fleet.OutcomeFailed:
			fmt.Fprintf(&b, "  FAILED     %-40s %v\n", truncateFleetTitle(r.Title, 40), r.Err)
		default:
			fmt.Fprintf(&b, "  skipped    %-40s %s\n", truncateFleetTitle(r.Title, 40), r.Reason)
		}
	}

	fmt.Fprintf(&b, "\n%s\n", s.Format())
	if plan {
		fmt.Fprintf(&b, "Estimated sequential runtime: %s (plus up to %s verification per session)\n",
			s.TotalWaited.Round(time.Second), fleet.DefaultVerifyTimeout)
		fmt.Fprintln(&b, "Re-run with --yes to recover.")
	}
	if s.Halted {
		fmt.Fprintf(&b, "HALTED: %s\n", s.HaltReason)
	}
	return b.String()
}

func fleetStatusJSON(as fleet.Assessment) map[string]interface{} {
	return map[string]interface{}{
		"success":    true,
		"total":      as.Total,
		"alive":      as.Alive,
		"down":       as.Down,
		"skipped":    as.Skipped,
		"mass_death": as.MassDeath,
		"probes":     as.Probes,
		"sessions":   fleetCandidatesJSON(as.Candidates),
	}
}

func fleetCandidatesJSON(candidates []fleet.Candidate) []map[string]interface{} {
	outs := make([]map[string]interface{}, 0, len(candidates))
	for _, c := range candidates {
		outs = append(outs, map[string]interface{}{
			"id":     c.ID(),
			"title":  c.Title(),
			"status": c.Status,
			"group":  groupOrRoot(c.Instance),
		})
	}
	return outs
}

func fleetRecoverJSON(s fleet.Summary, plan bool) map[string]interface{} {
	results := make([]map[string]interface{}, 0, len(s.Results))
	for _, r := range s.Results {
		entry := map[string]interface{}{
			"id":            r.ID,
			"title":         r.Title,
			"status_before": r.Status,
			"outcome":       string(r.Outcome),
			"waited_ms":     r.WaitedBefore.Milliseconds(),
		}
		if r.Err != nil {
			entry["error"] = r.Err.Error()
		}
		if r.Reason != "" {
			entry["reason"] = r.Reason
		}
		if r.Outcome == fleet.OutcomeRecovered || r.Outcome == fleet.OutcomeUnverified {
			entry["pane_alive"] = r.Report.PaneAlive
			entry["tool_started"] = r.Report.ToolStarted
			entry["status_after"] = r.Report.Status
			entry["substate"] = r.Report.Substate
			entry["verify_ms"] = r.Report.Elapsed.Milliseconds()
		}
		results = append(results, entry)
	}
	return map[string]interface{}{
		"success":     !s.Halted,
		"dry_run":     plan,
		"total":       s.Assessment.Total,
		"alive":       s.Assessment.Alive,
		"down":        s.Assessment.Down,
		"mass_death":  s.Assessment.MassDeath,
		"attempted":   s.Attempted,
		"recovered":   s.Recovered,
		"unverified":  s.Unverified,
		"failed":      s.Failed,
		"skipped":     s.Skipped,
		"halted":      s.Halted,
		"halt_reason": s.HaltReason,
		"summary":     s.Format(),
		"sessions":    results,
	}
}

func groupOrRoot(inst *session.Instance) string {
	if inst == nil || inst.GroupPath == "" {
		return session.DefaultGroupPath
	}
	return inst.GroupPath
}

// truncateFleetTitle shortens a title to max DISPLAY characters. It counts and
// cuts by rune, not byte: session titles are user-supplied and routinely contain
// non-ASCII, and a byte-slice would split a multi-byte rune into mojibake in the
// middle of a recovery report.
func truncateFleetTitle(title string, max int) string {
	runes := []rune(title)
	if len(runes) <= max {
		return title
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
