package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const usageUsage = `Usage: agent-deck usage [--json]
       agent-deck usage ingest claude [-- <command> [args...]]

Show how much of each provider's subscription quota is left, from the
provider's own numbers.

Options:
  --json      Print the report as JSON

Subcommands:
  ingest claude   Read a Claude Code statusLine payload on stdin and cache the
                  rate_limits it carries. With a trailing "-- <command>", the
                  same bytes are passed to that command and its output and exit
                  status are forwarded, so an existing statusLine keeps working.`

// handleUsage is the `agent-deck usage` entry point.
func handleUsage(profile string, args []string) {
	if len(args) > 0 && args[0] == "ingest" {
		handleUsageIngest(profile, args[1:])
		return
	}

	flags := flag.NewFlagSet("usage", flag.ContinueOnError)
	flags.SetOutput(os.Stdout)
	flags.Usage = func() { fmt.Println(usageUsage) }
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if len(args) > 0 && args[0] == "help" {
		flags.Usage()
		return
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Unknown usage argument: %s\n", flags.Arg(0))
		fmt.Fprintln(os.Stderr, usageUsage)
		os.Exit(2)
	}

	store := openQuotaStore(profile)
	report := quota.Report{Providers: collectQuota(store)}

	if *asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: encoding usage report: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(encoded))
		return
	}
	fmt.Print(renderUsage(report, time.Now()))
	// Exit 0 even when a provider reported an error: a provider outage is data
	// about that provider, not a failure of the command, and a script gating on
	// the exit status must not read one as the other.
}

// openQuotaStore resolves the profile the same way every other on-disk-state
// command does. ResolveProfileForStorage carries the #1790 guard that stops a
// CLAUDE_CONFIG_DIR-inferred profile from materialising a phantom directory.
func openQuotaStore(profile string) *quota.Store {
	resolved, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolving profile: %v\n", err)
		os.Exit(1)
	}
	store, err := quota.NewStore(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: opening quota cache: %v\n", err)
		os.Exit(1)
	}
	return store
}

// collectQuota reads the cached snapshots.
//
// Claude is PUSH-only: its snapshot arrives from whatever statusLine invocation
// last ran, and there is no endpoint to ask. So this reads the cache and
// nothing else — `agent-deck usage` makes no network request. A pull-based
// provider would fetch here, on this user-triggered path, and never on a TUI
// render path.
//
// A cache that cannot be read at all is a warning on stderr, not a failure: the
// providers that did load still print.
func collectQuota(store *quota.Store) []quota.Snapshot {
	cached, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: reading quota cache: %v\n", err)
	}
	// Never nil: `--json` must emit "providers": [] rather than null so a
	// consumer can range over it without a nil check.
	if cached == nil {
		return []quota.Snapshot{}
	}
	return cached
}

// renderUsage is the human rendering. It is a pure function of the report and a
// clock so the shape can be pinned by a test without a subprocess.
func renderUsage(report quota.Report, now time.Time) string {
	if len(report.Providers) == 0 {
		return "No provider quota cached yet.\n" +
			"Claude: wire `agent-deck usage ingest claude` into your statusLine.\n"
	}

	var out strings.Builder
	for _, provider := range report.Providers {
		header := provider.Label
		if provider.Error != "" {
			// The failing provider names itself, so a reader can tell which one
			// is out without inferring it from which line went missing.
			fmt.Fprintf(&out, "%s  unavailable: %s\n", header, provider.Error)
			if len(provider.Windows) == 0 {
				continue
			}
			// Last known numbers are still worth having, clearly marked.
			fmt.Fprintf(&out, "%s  last known %s\n", header, describeAge(provider, now))
		} else {
			fmt.Fprintf(&out, "%s  %s\n", header, describeAge(provider, now))
		}
		for _, window := range provider.Windows {
			fmt.Fprintf(&out, "  %-6s %6.1f%%%s\n", window.Label, window.UsedPercentage, describeReset(window.ResetsAt, now))
		}
	}
	return out.String()
}

// describeAge says when the snapshot was taken, and says "stale" when the store
// judged it past its freshness bound. A stale number is shown rather than
// hidden — dropping it would leave the user with less than they had — but it is
// never presented as current.
func describeAge(provider quota.Snapshot, now time.Time) string {
	marker := ""
	if provider.Stale {
		marker = " (stale)"
	}
	if provider.UpdatedAt <= 0 {
		return "updated at an unknown time (stale)"
	}
	age := now.Sub(time.Unix(provider.UpdatedAt, 0))
	if age < 0 {
		age = 0
	}
	return "updated " + shortDuration(age) + " ago" + marker
}

// describeReset renders the provider's own reset time. Nothing is printed when
// the provider did not report one: an invented "resets in ~5h" would be a
// promise agent-deck is in no position to make.
func describeReset(resetsAt *int64, now time.Time) string {
	if resetsAt == nil {
		return ""
	}
	remaining := time.Unix(*resetsAt, 0).Sub(now)
	if remaining <= 0 {
		return "  window has reset"
	}
	return "  resets in " + shortDuration(remaining)
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

const usageIngestUsage = `Usage: agent-deck usage ingest claude [-- <command> [args...]]

Read a Claude Code statusLine payload on stdin and cache the rate_limits it
carries. Only rate_limits is kept: the transcript path, cwd, prompt and model in
that payload are never stored or printed.

Wire it into ~/.claude/settings.json:

  "statusLine": {"type": "command", "command": "agent-deck usage ingest claude"}

If you already have a statusLine command, keep it by wrapping it:

  "statusLine": {"type": "command",
                 "command": "agent-deck usage ingest claude -- your-existing-command"}`

// handleUsageIngest implements `agent-deck usage ingest claude`.
func handleUsageIngest(profile string, args []string) {
	if len(args) == 0 || helpRequested(args) || args[0] == "help" {
		fmt.Println(usageIngestUsage)
		return
	}
	if args[0] != "claude" {
		fmt.Fprintf(os.Stderr, "Unknown ingest source: %s\n", args[0])
		fmt.Fprintln(os.Stderr, usageIngestUsage)
		os.Exit(2)
	}
	rest := args[1:]
	if helpRequested(rest) || (len(rest) > 0 && rest[0] == "help") {
		fmt.Println(usageIngestUsage)
		return
	}

	var wrapped []string
	if len(rest) > 0 {
		if rest[0] != "--" {
			fmt.Fprintf(os.Stderr, "Unknown ingest argument: %s\n", rest[0])
			fmt.Fprintln(os.Stderr, usageIngestUsage)
			os.Exit(2)
		}
		wrapped = rest[1:]
	}

	// The payload is read once and reused, because stdin cannot be replayed and
	// the wrapped command must receive exactly what Claude sent.
	payload, err := readStatusLinePayload(os.Stdin)
	if err != nil {
		// Claude Code renders a statusLine command's failure as a BLANK status
		// line, so a loud failure here would replace the user's status bar with
		// nothing. The diagnostic goes to stderr and the wrapped command still
		// runs.
		fmt.Fprintf(os.Stderr, "agent-deck: reading statusLine payload: %v\n", err)
	} else if snapshot, ok, parseErr := quota.ParseStatusLine(strings.NewReader(string(payload))); parseErr != nil {
		fmt.Fprintf(os.Stderr, "agent-deck: parsing statusLine payload: %v\n", parseErr)
	} else if ok {
		if saveErr := openQuotaStore(profile).Save(snapshot); saveErr != nil {
			fmt.Fprintf(os.Stderr, "agent-deck: caching Claude quota: %v\n", saveErr)
		}
	}

	if len(wrapped) == 0 {
		// Nothing is printed. A user with no statusLine before gets no status
		// line now, which is the only non-surprising outcome.
		return
	}
	runWrappedStatusLine(wrapped, payload)
}

// maxIngestBytes bounds the stdin read at the same size the parser accepts, so
// an oversized payload is refused rather than buffered.
const maxIngestBytes = 1 << 20

func readStatusLinePayload(stdin *os.File) ([]byte, error) {
	limited := make([]byte, 0, 4096)
	buffer := make([]byte, 4096)
	for {
		n, err := stdin.Read(buffer)
		limited = append(limited, buffer[:n]...)
		if len(limited) > maxIngestBytes {
			return limited[:maxIngestBytes], errors.New("statusLine payload exceeds size cap")
		}
		if err != nil {
			if errors.Is(err, os.ErrClosed) || err.Error() == "EOF" {
				return limited, nil
			}
			return limited, err
		}
		if n == 0 {
			return limited, nil
		}
	}
}

// runWrappedStatusLine runs the user's own statusLine command with the same
// bytes on stdin and forwards its stdout, stderr and exit status.
//
// argv comes straight from os.Args and is passed to exec.Command as separate
// arguments: there is no shell and no string interpolation anywhere on this
// path, so nothing in the payload or the settings file can become a command.
func runWrappedStatusLine(argv []string, payload []byte) {
	// #nosec G204 G702 -- argv is the user's own statusLine command, taken verbatim
	// from their settings.json via os.Args and passed as separate arguments.
	// There is no shell and no string interpolation on this path, so neither
	// the payload nor anything Claude sends can become a command.
	command := exec.Command(argv[0], argv[1:]...)
	command.Stdin = strings.NewReader(string(payload))
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "agent-deck: running statusLine command: %v\n", err)
		os.Exit(1)
	}
}
