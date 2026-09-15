package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// healthLogDir resolves without creating profile state, including for remote exec.
func healthLogDir(profile string) (string, error) {
	profile, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return "", err
	}
	dir, err := session.GetProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "health"), nil
}

func startRuntimeHealth(profile, role string) func() {
	config, err := session.LoadUserConfig()
	if err != nil || !config.Health.IsEnabled() {
		return func() {}
	}
	dir, err := healthLogDir(profile)
	if err != nil {
		return func() {}
	}
	return health.Start(dir, role, session.GetHooksDir())
}

func readRuntimeHealth(profile string, since time.Duration) (health.Summary, error) {
	dir, err := healthLogDir(profile)
	if err != nil {
		return health.Summary{}, err
	}
	return health.Report(dir, since)
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
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(health.Format(report))
}
