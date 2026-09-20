package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// healthLogDir resolves without creating profile state, including for remote exec.
func healthLogDir(profile string) (string, error) { return session.HealthLogDir(profile) }

func startRuntimeHealth(profile, role string) func() {
	config, err := session.LoadUserConfig()
	if err != nil || !config.Health.IsEnabled() {
		return func() {}
	}
	dir, err := healthLogDir(profile)
	if err != nil {
		return func() {}
	}
	return health.Start(dir, role, session.GetHooksDir(), Version)
}

func readRuntimeHealth(profile string, since time.Duration) (health.Summary, error) {
	dir, err := healthLogDir(profile)
	if err != nil {
		return health.Summary{}, err
	}
	return health.Report(dir, since)
}

// untrackedTmuxSessionsForHealth loads this profile's tracked instances and
// reports live agentdeck_-prefixed tmux sessions outside that set (see
// session.UntrackedTmuxSessions). Read-only by construction: it resolves the
// profile directory first and returns empty rather than touching storage at
// all when that directory does not exist yet, matching doctor's "never
// writes to HOME" contract — NewStorageWithProfile's MkdirAll would
// otherwise create it as a side effect of a diagnostic read. Used only by
// `doctor` (see doctor_cmd.go): `health --json`'s output is forwarded
// byte-for-byte over `remote exec` and compared for parity, and this field's
// live-computed ages would make two back-to-back calls disagree.
func untrackedTmuxSessionsForHealth(profile string) []health.UntrackedTmuxSession {
	effectiveProfile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return nil
	}
	profileDir, err := session.GetProfileDir(effectiveProfile)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(profileDir); err != nil {
		return nil
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil
	}
	untracked, err := session.UntrackedTmuxSessions(instances)
	if err != nil {
		return nil
	}
	out := make([]health.UntrackedTmuxSession, 0, len(untracked))
	for _, u := range untracked {
		out = append(out, health.UntrackedTmuxSession{
			Name:        u.Name,
			AgeSeconds:  u.Age.Seconds(),
			PaneCommand: u.PaneCommand,
		})
	}
	return out
}

func handleHealth(profile string, args []string) {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output runtime health as JSON")
	since := fs.Duration("since", time.Hour, "History window (positive Go duration, e.g. 30m or 1h)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck health [--json] [--since 1h]\n\nRead local runtime health for the selected profile. No data leaves this host.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if fs.NArg() != 0 || *since <= 0 {
		fmt.Fprintln(os.Stderr, "health requires a positive --since duration and no positional arguments")
		os.Exit(2)
	}
	report, err := readRuntimeHealth(profile, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: runtime health: %v\n", err)
		os.Exit(1)
	}
	aggregate := sessionAggregateForHealth(profile, *since)
	report.Sessions = &aggregate
	if sel, selErr := session.SelectStoreRoot(); selErr == nil {
		if warning := sel.Warning(); warning != "" {
			report.Flags = append(report.Flags, "profile store divergence: "+warning)
		}
	}
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(health.Format(report))
}
