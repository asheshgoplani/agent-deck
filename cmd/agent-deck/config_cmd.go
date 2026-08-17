package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/configdoctor"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func handleConfig(args []string) {
	if len(args) == 0 {
		printConfigUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "orchestrate":
		if len(args) != 1 {
			printConfigUsage()
			os.Exit(1)
		}
		handleConfigOrchestrate()
	case "doctor":
		handleConfigDoctor(args[1:])
	default:
		printConfigUsage()
		os.Exit(1)
	}
}

func printConfigUsage() {
	fmt.Fprintln(os.Stderr, "Usage: agent-deck config <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  orchestrate   Print the resolved orchestrate tool policy as JSON")
	fmt.Fprintln(os.Stderr, "  doctor        Report divergence between config.toml and the agent homes on disk")
}

func handleConfigOrchestrate() {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	policy := cfg.ResolveOrchestrateToolPolicy()
	if err := json.NewEncoder(os.Stdout).Encode(policy); err != nil {
		fmt.Fprintf(os.Stderr, "Error: encode orchestrate policy: %v\n", err)
		os.Exit(1)
	}
}

// handleConfigDoctor prints declared-vs-actual divergence. Exit code 1 on any
// error-severity finding so it can gate a sync script; warnings and info never
// fail the run, because "installed something by hand" is not a failure.
func handleConfigDoctor(args []string) {
	fs := flag.NewFlagSet("config doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit findings as JSON")
	quiet := fs.Bool("quiet", false, "suppress info-severity findings")
	checkFilter := fs.String("check", "", "only report this check slug (e.g. tool-asymmetry)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "Error: no config.toml found")
		os.Exit(1)
	}

	report := configdoctor.Diagnose(cfg)
	report.Findings = filterFindings(report.Findings, *quiet, *checkFilter)

	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode report: %v\n", err)
			os.Exit(1)
		}
	} else {
		printDoctorReport(report)
	}

	if report.Errors() > 0 {
		os.Exit(1)
	}
}

func filterFindings(findings []configdoctor.Finding, quiet bool, check string) []configdoctor.Finding {
	out := findings[:0:0]
	for _, f := range findings {
		if quiet && f.Severity == configdoctor.SeverityInfo {
			continue
		}
		if check != "" && f.Check != check {
			continue
		}
		out = append(out, f)
	}
	return out
}

func printDoctorReport(report configdoctor.Report) {
	if len(report.Findings) == 0 {
		fmt.Printf("No divergence found across %d agent home(s).\n", report.Checked)
		return
	}

	// Group by check so the reader sees one heading per kind of problem
	// rather than a flat wall sorted by severity alone.
	byCheck := map[string][]configdoctor.Finding{}
	var order []string
	for _, f := range report.Findings {
		if _, seen := byCheck[f.Check]; !seen {
			order = append(order, f.Check)
		}
		byCheck[f.Check] = append(byCheck[f.Check], f)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return worstSeverity(byCheck[order[i]]) < worstSeverity(byCheck[order[j]])
	})

	for _, check := range order {
		findings := byCheck[check]
		fmt.Printf("\n%s (%d)\n", check, len(findings))
		for _, f := range findings {
			fmt.Printf("  %s %s\n", severityMarker(f.Severity), f.Summary)
			fmt.Printf("      scope: %s\n", f.Scope)
			if f.Fix != "" {
				fmt.Printf("      fix:   %s\n", f.Fix)
			}
		}
	}

	var counts []string
	for _, sev := range []configdoctor.Severity{configdoctor.SeverityError, configdoctor.SeverityWarn, configdoctor.SeverityInfo} {
		if n := countSeverity(report.Findings, sev); n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	fmt.Printf("\n%s across %d agent home(s).\n", strings.Join(counts, ", "), report.Checked)
}

func worstSeverity(findings []configdoctor.Finding) int {
	worst := 99
	for _, f := range findings {
		switch f.Severity {
		case configdoctor.SeverityError:
			if worst > 0 {
				worst = 0
			}
		case configdoctor.SeverityWarn:
			if worst > 1 {
				worst = 1
			}
		default:
			if worst > 2 {
				worst = 2
			}
		}
	}
	return worst
}

func countSeverity(findings []configdoctor.Finding, sev configdoctor.Severity) int {
	n := 0
	for _, f := range findings {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func severityMarker(sev configdoctor.Severity) string {
	switch sev {
	case configdoctor.SeverityError:
		return "ERROR"
	case configdoctor.SeverityWarn:
		return "WARN "
	default:
		return "INFO "
	}
}
