package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const usageUsage = `Usage: agent-deck usage [--json] [--refresh]
       agent-deck usage ingest claude [-- <command> [args...]]

Show how much of each provider's subscription quota is left, from the
provider's own numbers.

Options:
  --json      Print the report as JSON
  --refresh   Force a fetch for pull-based providers, ignoring the cache

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
	refresh := flags.Bool("refresh", false, "force a fetch for pull-based providers")
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
	report := quota.Report{Providers: collectQuota(store, *refresh)}

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

// collectQuota merges what is cached with a live fetch of the pull-based
// providers.
//
// Claude is PUSH-only: its snapshot arrives from whatever statusLine invocation
// last ran, and there is no endpoint to ask. Z.ai is PULL-based, so it is
// fetched when its cache is older than the store's staleness bound, or on
// --refresh. That split is what gives --refresh a real meaning instead of being
// a flag that sometimes does nothing.
func collectQuota(store *quota.Store, refresh bool) []quota.Snapshot {
	cached, err := store.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: reading quota cache: %v\n", err)
	}

	byID := make(map[string]quota.Snapshot, len(cached))
	order := make([]string, 0, len(cached)+1)
	for _, snapshot := range cached {
		byID[snapshot.ID] = snapshot
		order = append(order, snapshot.ID)
	}

	existing, haveZai := byID[quota.ProviderZai]
	if refresh || !haveZai || existing.Stale {
		if fetched, ok := fetchZaiSnapshot(existing, haveZai); ok {
			if _, known := byID[quota.ProviderZai]; !known {
				order = append(order, quota.ProviderZai)
			}
			byID[quota.ProviderZai] = fetched
			if fetched.Error == "" {
				if err := store.Save(fetched); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: caching Z.ai quota: %v\n", err)
				}
			}
		}
	}

	providers := make([]quota.Snapshot, 0, len(order))
	for _, id := range order {
		providers = append(providers, byID[id])
	}
	return providers
}

// fetchZaiSnapshot performs the one outbound request this feature makes.
//
// It is user-triggered (only from `agent-deck usage`), bounded by an explicit
// client timeout, cached, and never on a TUI render path. The destination is
// the host the user configured in ANTHROPIC_BASE_URL; there is no hardcoded
// fallback, so a machine that never configured Z.ai generates no traffic.
//
// ok is false when the provider is simply not configured — that is not a
// failure worth a line in the output, it is a provider the user does not use.
//
// A fetch failure is NOT persisted: the cache holds numbers, and overwriting a
// good snapshot with a transient timeout would turn one bad minute into a
// permanently blank provider. The last known numbers are kept and shown with
// the failure attached.
func fetchZaiSnapshot(cachedSnapshot quota.Snapshot, haveCached bool) (quota.Snapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), quota.DefaultZaiTimeout)
	defer cancel()

	client := &http.Client{Timeout: quota.DefaultZaiTimeout}
	fetched, err := quota.FetchZai(ctx, client)
	switch {
	case err == nil:
		return fetched, true
	case errors.Is(err, quota.ErrNotConfigured):
		return quota.Snapshot{}, false
	case haveCached:
		cachedSnapshot.Error = err.Error()
		return cachedSnapshot, true
	default:
		return quota.Snapshot{
			ID:        quota.ProviderZai,
			Label:     quota.ProviderLabel(quota.ProviderZai),
			Error:     err.Error(),
			UpdatedAt: time.Now().Unix(),
		}, true
	}
}

// renderUsage is the human rendering. It is a pure function of the report and a
// clock so the shape can be pinned by a test without a subprocess.
func renderUsage(report quota.Report, now time.Time) string {
	if len(report.Providers) == 0 {
		return "No provider quota cached yet.\n" +
			"Claude: wire `agent-deck usage ingest claude` into your statusLine.\n" +
			"Z.ai:   run this inside a session whose ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN are set.\n"
	}

	var out strings.Builder
	for _, provider := range report.Providers {
		header := provider.Label
		if provider.Plan != "" {
			header += " (" + provider.Plan + ")"
		}
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
