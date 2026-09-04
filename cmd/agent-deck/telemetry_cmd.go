package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"golang.org/x/term"
)

func handleTelemetry(args []string) {
	interactive := telemetry.Interactive()
	for _, arg := range args {
		if arg == "--json" {
			interactive = term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
		}
	}
	code := runTelemetry(args, Version, os.Stdin, os.Stdout, os.Stderr, interactive)
	if code != 0 {
		os.Exit(code)
	}
}

// telemetryStatus is the --json shape of `telemetry status`.
type telemetryStatus struct {
	Enabled        bool           `json:"enabled"`
	Reason         string         `json:"reason,omitempty"`
	Consent        string         `json:"consent"`
	ConsentVersion string         `json:"consent_version,omitempty"`
	ConsentDay     string         `json:"consent_day,omitempty"`
	InstallID      string         `json:"install_id,omitempty"`
	Endpoint       string         `json:"endpoint"`
	LastSentDay    string         `json:"last_sent_day,omitempty"`
	LastAttemptDay string         `json:"last_attempt_day,omitempty"`
	PendingCounts  map[string]int `json:"pending_counters"`
	StatePath      string         `json:"state_path"`
	SchemaVersion  int            `json:"schema_version"`
}

func runTelemetry(args []string, version string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	var sub string
	var jsonOut, yes bool
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printTelemetryHelp(out)
			return 0
		case "--json":
			jsonOut = true
		case "--yes", "-y":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(errOut, "telemetry: unknown flag %q\n", a)
				return 2
			}
			if sub != "" {
				fmt.Fprintf(errOut, "telemetry: unexpected argument %q\n", a)
				return 2
			}
			sub = a
		}
	}

	switch sub {
	case "", "status":
		return telemetryStatusCmd(out, jsonOut)
	case "enable":
		return telemetryEnableCmd(version, in, out, errOut, jsonOut, yes, interactive)
	case "disable":
		return telemetryDisableCmd(version, out, errOut, jsonOut)
	case "preview":
		return telemetryPreviewCmd(version, out, jsonOut)
	case "show-last":
		return telemetryShowLastCmd(out, jsonOut)
	case "reset-id":
		return telemetryResetIDCmd(out, errOut, jsonOut)
	default:
		fmt.Fprintf(errOut, "telemetry: unknown subcommand %q\n\n", sub)
		printTelemetryHelp(errOut)
		return 2
	}
}

func buildTelemetryStatus(s *telemetry.State) telemetryStatus {
	enabled, reason := telemetry.Enabled(s)
	path, _ := telemetry.StatePath()
	counts := s.Counters
	if counts == nil {
		counts = map[string]int{}
	}
	return telemetryStatus{
		Enabled:        enabled,
		Reason:         string(reason),
		Consent:        string(s.Consent),
		ConsentVersion: s.ConsentVersion,
		ConsentDay:     s.ConsentDay,
		InstallID:      s.InstallID,
		Endpoint:       telemetry.Endpoint(),
		LastSentDay:    s.LastSentDay,
		LastAttemptDay: s.LastAttemptDay,
		PendingCounts:  counts,
		StatePath:      path,
		SchemaVersion:  telemetry.SchemaVersion,
	}
}

func writeJSON(out io.Writer, v any) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

func telemetryStatusCmd(out io.Writer, jsonOut bool) int {
	st := buildTelemetryStatus(telemetry.LoadState())
	if jsonOut {
		return writeJSON(out, st)
	}
	state := "OFF"
	if st.Enabled {
		state = "ON"
	}
	if st.LastSentDay == "" {
		st.LastSentDay = "never"
	}
	fmt.Fprintf(out, `Telemetry: %s
  Reason:        %s
  Consent:       %s
  Install id:    %s
  Endpoint:      %s
  Last sent:     %s
  Pending:       %d counter(s)
  State file:    %s
  Docs:          %s
`, state, st.Reason, st.Consent, st.InstallID, st.Endpoint, st.LastSentDay, len(st.PendingCounts), st.StatePath, telemetry.DocsURL)
	return 0
}

func telemetryEnableCmd(version string, in io.Reader, out, errOut io.Writer, jsonOut, yes, interactive bool) int {
	if r := telemetry.HardDisableReason(); r != telemetry.ReasonNone {
		fmt.Fprintf(errOut, "telemetry: cannot enable: %s\n", r)
		fmt.Fprintln(errOut, "Unset the variable (or config key) first, then run this command again.")
		return 1
	}
	if yes || !interactive || telemetry.InsideSession() || telemetry.IsCI() {
		fmt.Fprintln(errOut, "telemetry: consent must be given by a person at an interactive terminal; --yes is not supported (not a terminal or no explicit answer). Nothing changed.")
		return 1
	}
	s := telemetry.LoadState()
	if enabled, _ := telemetry.Enabled(s); enabled {
		return telemetryStatusCmd(out, jsonOut)
	}
	disclosure := out
	if jsonOut {
		disclosure = errOut
	}
	shownEndpoint := telemetry.Endpoint()
	if err := telemetry.ValidateEndpoint(shownEndpoint); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if _, err := fmt.Fprintf(disclosure, "%s\nSend anonymous usage reports? [y/N]: ", telemetry.PromptText(shownEndpoint)); err != nil {
		return 1
	}
	line, readErr := bufio.NewReader(in).ReadString('\n')
	// EOF or a failed read is never an affirmative answer, even after a y.
	if readErr != nil || !isYesConfirmation(line) {
		if err := telemetry.Disable(version, time.Now()); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if jsonOut {
			return writeJSON(out, buildTelemetryStatus(telemetry.LoadState()))
		}
		fmt.Fprintln(out, "Telemetry stays off. You will not be asked again; run `agent-deck telemetry enable` if you change your mind.")
		return 0
	}
	if telemetry.HardDisabled() || telemetry.Endpoint() != shownEndpoint || telemetry.IsCI() || telemetry.InsideSession() {
		fmt.Fprintln(errOut, "telemetry: consent conditions changed; nothing enabled")
		return 1
	}

	if err := telemetry.Grant(s, version, time.Now()); err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if err := telemetry.SaveState(s); err != nil {
		fmt.Fprintf(errOut, "telemetry: save state: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(s))
	}
	fmt.Fprintf(out, "Telemetry enabled. Install id: %s\n", s.InstallID)
	fmt.Fprintln(out, "One anonymous report per day will be sent the next time you open the agent-deck TUI.")
	fmt.Fprintln(out, "Inspect it with `agent-deck telemetry show-last`; preview with `agent-deck telemetry preview`; turn it off with `agent-deck telemetry disable`.")
	return 0
}

func telemetryDisableCmd(version string, out, errOut io.Writer, jsonOut bool) int {
	if err := telemetry.Disable(version, time.Now()); err != nil {
		fmt.Fprintf(errOut, "telemetry: save state: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(telemetry.LoadState()))
	}
	fmt.Fprintln(out, "Telemetry disabled. Install id and pending counters removed; nothing will be sent.")
	return 0
}

func telemetryPreviewCmd(version string, out io.Writer, jsonOut bool) int {
	s := telemetry.LoadState()
	if s.InstallID == "" {
		if jsonOut {
			return writeJSON(out, map[string]any{"payload": nil, "reason": "no install id; telemetry has not been enabled"})
		}
		fmt.Fprintln(out, "No payload exists while telemetry is off. See --help for the schema.")
		return 0
	}
	body, err := telemetry.BuildPayload(s, version, time.Now()).Marshal()
	if err != nil {
		return 1
	}
	_, err = fmt.Fprintln(out, string(body))
	if err != nil {
		return 1
	}
	return 0
}

func telemetryShowLastCmd(out io.Writer, jsonOut bool) int {
	s := telemetry.LoadState()
	if len(s.LastPayload) == 0 {
		if jsonOut {
			return writeJSON(out, map[string]any{"sent": false, "last_sent_day": "", "payload": nil})
		}
		fmt.Fprintln(out, "Nothing has ever been sent from this install.")
		if s.Consent == telemetry.ConsentGranted {
			fmt.Fprintln(out, "A report of this shape would be sent (pending counters, today's day):")
			body, _ := telemetry.BuildPayload(s, Version, time.Now()).Marshal()
			fmt.Fprintln(out, string(body))
		}
		return 0
	}
	if jsonOut {
		return writeJSON(out, map[string]any{"sent": true, "last_sent_day": s.LastSentDay, "payload": s.LastPayload})
	}
	fmt.Fprintf(out, "Last report sent on %s (exact body):\n", s.LastSentDay)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, s.LastPayload, "", "  "); err != nil {
		fmt.Fprintln(out, string(s.LastPayload))
	} else {
		fmt.Fprintln(out, pretty.String())
	}
	return 0
}

func telemetryResetIDCmd(out, errOut io.Writer, jsonOut bool) int {
	s := telemetry.LoadState()
	if s.Consent != telemetry.ConsentGranted {
		fmt.Fprintln(errOut, "telemetry: no install id exists because telemetry is not enabled.")
		return 1
	}
	if err := telemetry.RotateInstallID(s); err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if err := telemetry.SaveState(s); err != nil {
		fmt.Fprintf(errOut, "telemetry: save state: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(s))
	}
	fmt.Fprintf(out, "Install id rotated. New id: %s\n", s.InstallID)
	return 0
}

func printTelemetryHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: agent-deck telemetry <command> [--json]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Opt-in, anonymous usage telemetry. OFF by default; nothing is sent until you")
	fmt.Fprintln(w, "explicitly say yes. Full details: "+telemetry.DocsURL)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  status      Show consent, install id, endpoint, last send day, pending counters")
	fmt.Fprintln(w, "  enable      Show the disclosure and require an interactive answer (y/N).")
	fmt.Fprintln(w, "  disable     Turn telemetry off, delete the install id and pending counters")
	fmt.Fprintln(w, "  preview     Print the exact current candidate JSON body without sending")
	fmt.Fprintln(w, "  show-last   Print the exact JSON body of the last report sent")
	fmt.Fprintln(w, "  reset-id    Replace the random install id with a new one")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --json      Machine-readable output for every command")
	fmt.Fprintln(w, "  --yes, -y   unsupported: explicit interactive consent is required (refused")
	fmt.Fprintln(w, "              inside an agent-deck session or in CI: consent must come from a person)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Kill switches (win over stored consent, re-read on every run):")
	fmt.Fprintln(w, "  AGENTDECK_TELEMETRY=0       hard off (any value other than 1/true/yes/on; =1 does NOT enable)")
	fmt.Fprintln(w, "  DO_NOT_TRACK=1              hard off")
	fmt.Fprintln(w, "  [telemetry] disabled=true   hard off, in config.toml")
	fmt.Fprintln(w, "  [telemetry] endpoint=URL    self-host the receiver (https, or http to localhost)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "When a report is sent:")
	fmt.Fprintln(w, "  At most one HTTPS POST per UTC day, only from the interactive TUI (never from")
	fmt.Fprintln(w, "  CLI commands, CI, scripts, or inside an agent-deck session). CLI commands only")
	fmt.Fprintln(w, "  update local counters. Failures are silent and not retried until the next day.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Payload schema v%d (exactly these keys, nothing else):\n", telemetry.SchemaVersion)
	fmt.Fprintln(w, "  schema_version  int     "+fmt.Sprint(telemetry.SchemaVersion))
	fmt.Fprintln(w, "  install_id      string  random 32 hex chars, rotatable, deleted on disable")
	fmt.Fprintln(w, "  version         string  agent-deck version")
	fmt.Fprintln(w, "  os, arch        string  e.g. darwin / arm64")
	fmt.Fprintln(w, "  day             string  YYYY-MM-DD (UTC); no finer timestamp anywhere")
	fmt.Fprintln(w, "  counters        object  integer counts, only these keys:")
	for _, k := range telemetry.AllowedCounterKeys() {
		fmt.Fprintln(w, "                          "+k)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Never sent: session titles, prompts, paths, commands, hostnames, usernames,")
	fmt.Fprintln(w, "custom tool names, model names, MCP names, IP fields. Receiver operators must disable IP/access logging.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Default endpoint: "+telemetry.DefaultEndpoint)
	fmt.Fprintln(w, "Current endpoint: "+telemetry.Endpoint())
}
