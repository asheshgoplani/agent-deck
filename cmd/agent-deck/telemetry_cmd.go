package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
	Enabled        bool                 `json:"enabled"`
	Reason         string               `json:"reason,omitempty"`
	Consent        string               `json:"consent"`
	ConsentVersion string               `json:"consent_version,omitempty"`
	ConsentDay     string               `json:"consent_day,omitempty"`
	InstallID      string               `json:"install_id,omitempty"`
	Level          string               `json:"level"`
	Endpoint       string               `json:"endpoint"`
	Upload         string               `json:"upload"`
	LogMode        bool                 `json:"log_mode,omitempty"`
	Spool          telemetry.SpoolStats `json:"spool"`
	NextUpload     string               `json:"next_upload,omitempty"`
	LastUpload     string               `json:"last_upload,omitempty"`
	LastResult     string               `json:"last_result,omitempty"`
	LastErrorKind  string               `json:"last_error_kind,omitempty"`
	CapToday       string               `json:"daily_cap_today"`
	StatePath      string               `json:"state_path"`
	SchemaVersion  int                  `json:"schema_version"`
}

func runTelemetry(args []string, version string, in io.Reader, out, errOut io.Writer, interactive bool) int {
	var positional []string
	var jsonOut, markdown, yes bool
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printTelemetryHelp(out)
			return 0
		case "--json":
			jsonOut = true
		case "--markdown":
			markdown = true
		case "--yes", "-y":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(errOut, "telemetry: unknown flag %q\n", a)
				return 2
			}
			positional = append(positional, a)
		}
	}
	sub, rest := "", positional
	if len(positional) > 0 {
		sub, rest = positional[0], positional[1:]
	}
	if len(rest) > 0 && sub != "level" || len(rest) > 1 {
		fmt.Fprintf(errOut, "telemetry: unexpected argument %q\n", rest[len(rest)-1])
		return 2
	}

	switch sub {
	case "", "status":
		return telemetryStatusCmd(out, jsonOut)
	case "enable", "on":
		return telemetryEnableCmd(version, in, out, errOut, jsonOut, yes, interactive)
	case "disable", "off":
		return telemetryDisableCmd(version, out, errOut, jsonOut)
	case "preview":
		return telemetryPreviewCmd(out, errOut, jsonOut)
	case "show-last":
		return telemetryShowLastCmd(out, jsonOut)
	case "reset-id":
		return telemetryResetIDCmd(out, errOut, jsonOut)
	case "schema":
		return telemetrySchemaCmd(out, jsonOut && !markdown)
	case "level":
		return telemetryLevelCmd(rest, in, out, errOut, jsonOut, interactive)
	default:
		fmt.Fprintf(errOut, "telemetry: unknown subcommand %q\n\n", sub)
		printTelemetryHelp(errOut)
		return 2
	}
}

func buildTelemetryStatus(s *telemetry.State) telemetryStatus {
	enabled, reason := telemetry.Enabled(s)
	path, _ := telemetry.StatePath()
	st := telemetryStatus{
		Enabled:        enabled,
		Reason:         string(reason),
		Consent:        string(s.Consent),
		ConsentVersion: s.ConsentVersion,
		ConsentDay:     s.ConsentDay,
		InstallID:      s.InstallID,
		Level:          string(telemetry.EffectiveLevel(s)),
		Endpoint:       telemetry.Endpoint(),
		Upload:         "configured",
		LogMode:        telemetry.LogMode(),
		Spool:          telemetry.ReadSpoolStats(),
		LastResult:     s.Upload.LastResult,
		LastErrorKind:  s.Upload.LastErrorKind,
		StatePath:      path,
		SchemaVersion:  telemetry.SchemaVersion,
	}
	switch {
	case st.LogMode:
		st.Upload = "log mode (never sent)"
	case !telemetry.Configured():
		st.Upload = "not configured (no PostHog project key; events stay local)"
	}
	if !s.Upload.NextTry.IsZero() {
		st.NextUpload = s.Upload.NextTry.Local().Format(time.RFC3339)
	}
	if !s.Upload.LastAt.IsZero() {
		st.LastUpload = s.Upload.LastAt.Local().Format(time.RFC3339)
	}
	emitted, dropped := telemetry.CapUsage(s, time.Now())
	st.CapToday = fmt.Sprintf("%d/%d events, %d dropped", emitted, telemetry.DailyEventCap, dropped)
	return st
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
	last := orDash(st.LastUpload)
	if st.LastResult != "" {
		last += " (" + st.LastResult
		if st.LastErrorKind != "" {
			last += ", " + st.LastErrorKind
		}
		last += ")"
	}
	fmt.Fprintf(out, `Telemetry: %s
  Reason:        %s
  Consent:       %s
  Install id:    %s
  Level:         %s
  Endpoint:      %s
  Upload:        %s
  Spool:         %d event(s), %d bytes, oldest day %s
  Next upload:   %s
  Last upload:   %s
  Daily cap:     %s
  State file:    %s
  Docs:          %s
`, state, orDash(st.Reason), st.Consent, orDash(st.InstallID), st.Level, st.Endpoint, st.Upload,
		st.Spool.Events, st.Spool.Bytes, orDash(st.Spool.OldestDay), orDash(st.NextUpload), last,
		st.CapToday, st.StatePath, telemetry.DocsURL)
	return 0
}

// telemetryConsentBlocked returns why this process cannot take consent, or "".
func telemetryConsentBlocked(yes, interactive bool) string {
	if r := telemetry.HardDisableReason(); r != telemetry.ReasonNone {
		return fmt.Sprintf("cannot enable: %s\nUnset the variable (or config key) first, then run this command again.", r)
	}
	if telemetry.LogMode() {
		return "cannot enable: AGENTDECK_TELEMETRY=log never grants consent. Unset it first."
	}
	if yes || !interactive || telemetry.AgentActor() || telemetry.IsCI() {
		return "consent must be given by a person at an interactive terminal; --yes is not supported (not a terminal or no explicit answer). Nothing changed."
	}
	return ""
}

func telemetryEnableCmd(version string, in io.Reader, out, errOut io.Writer, jsonOut, yes, interactive bool) int {
	if msg := telemetryConsentBlocked(yes, interactive); msg != "" {
		fmt.Fprintln(errOut, "telemetry: "+msg)
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
	// The CLI keeps v1 strictness: there is no highlighted button in a plain
	// shell prompt, so only an explicit y enables; Enter or EOF is no.
	if _, err := fmt.Fprintf(disclosure, "%s\n\nShare anonymous usage data? [y/N]: ", telemetry.PromptText(shownEndpoint)); err != nil {
		return 1
	}
	line, readErr := bufio.NewReader(in).ReadString('\n')
	if readErr != nil || !isYesConfirmation(line) {
		if err := telemetry.Disable(version, time.Now()); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if jsonOut {
			return writeJSON(out, buildTelemetryStatus(telemetry.LoadState()))
		}
		fmt.Fprintln(out, telemetry.DeclinedLine)
		return 0
	}
	if telemetry.HardDisabled() || telemetry.Endpoint() != shownEndpoint || telemetry.IsCI() || telemetry.AgentActor() {
		fmt.Fprintln(errOut, "telemetry: consent conditions changed; nothing enabled")
		return 1
	}
	previous := s.Previous()
	if err := telemetry.Grant(s, version, time.Now()); err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if err := telemetry.SaveState(s); err != nil {
		fmt.Fprintf(errOut, "telemetry: save state: %v\n", err)
		return 1
	}
	// Recorded only after the grant is durably on disk.
	telemetry.AfterConsent(telemetry.SourceCLIOn, previous,
		session.TelemetryBaseline(session.TelemetrySummaryFromStorage()), nil)
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(telemetry.LoadState()))
	}
	fmt.Fprintln(out, telemetry.GrantedLine)
	fmt.Fprintf(out, "Install id: %s. Preview: agent-deck telemetry preview\n", s.InstallID)
	if !telemetry.Configured() {
		fmt.Fprintln(out, "No upload destination is configured in this build yet; events stay in the local spool.")
	}
	return 0
}

func telemetryDisableCmd(version string, out, errOut io.Writer, jsonOut bool) int {
	if err := telemetry.Disable(version, time.Now()); err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(telemetry.LoadState()))
	}
	fmt.Fprintln(out, "Telemetry disabled. Install id, local spool and counters removed; nothing will be sent.")
	return 0
}

func telemetryPreviewCmd(out, errOut io.Writer, jsonOut bool) int {
	bodies, err := telemetry.PreviewBatch()
	if err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if jsonOut {
		raw := make([]json.RawMessage, len(bodies))
		for i, b := range bodies {
			raw[i] = b
		}
		return writeJSON(out, map[string]any{"requests": raw})
	}
	if len(bodies) == 0 {
		s := telemetry.LoadState()
		if s.Consent != telemetry.ConsentGranted {
			fmt.Fprintln(out, "Nothing is recorded or sent while telemetry is off. The schema: agent-deck telemetry schema")
		} else {
			fmt.Fprintln(out, "Nothing is waiting to be sent: only completed hours (and completed days for daily rollups) are uploaded.")
		}
		return 0
	}
	fmt.Fprintf(out, "The next upload would POST %d request(s) to %s/batch/ (exact bodies):\n", len(bodies), telemetry.Endpoint())
	for _, b := range bodies {
		printIndentedJSON(out, b)
	}
	return 0
}

// printIndentedJSON prints a JSON body indented, or as-is if it does not parse.
func printIndentedJSON(out io.Writer, body []byte) {
	var pretty bytes.Buffer
	if json.Indent(&pretty, body, "", "  ") != nil {
		fmt.Fprintln(out, string(body))
		return
	}
	fmt.Fprintln(out, pretty.String())
}

func telemetryShowLastCmd(out io.Writer, jsonOut bool) int {
	s := telemetry.LoadState()
	if len(s.LastPayload) == 0 {
		if jsonOut {
			return writeJSON(out, map[string]any{"sent": false, "last_sent_day": "", "payload": nil})
		}
		fmt.Fprintln(out, "Nothing has ever been sent from this install. See what would be: agent-deck telemetry preview")
		return 0
	}
	if jsonOut {
		return writeJSON(out, map[string]any{"sent": true, "last_sent_day": s.LastSentDay, "payload": s.LastPayload})
	}
	fmt.Fprintf(out, "Last request acknowledged on %s (exact body):\n", s.LastSentDay)
	printIndentedJSON(out, s.LastPayload)
	return 0
}

func telemetryResetIDCmd(out, errOut io.Writer, jsonOut bool) int {
	s, err := telemetry.ResetID()
	if err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(s))
	}
	fmt.Fprintf(out, "Install id rotated; local spool and counters removed. New id: %s\n", s.InstallID)
	return 0
}

func telemetrySchemaCmd(out io.Writer, jsonOut bool) int {
	if jsonOut {
		data, err := telemetry.SchemaJSON()
		if err != nil {
			return 1
		}
		_, err = fmt.Fprintln(out, string(data))
		if err != nil {
			return 1
		}
		return 0
	}
	_, err := fmt.Fprint(out, telemetry.SchemaMarkdown())
	if err != nil {
		return 1
	}
	return 0
}

func telemetryLevelCmd(rest []string, in io.Reader, out, errOut io.Writer, jsonOut, interactive bool) int {
	if len(rest) != 1 || (rest[0] != string(telemetry.LevelFull) && rest[0] != string(telemetry.LevelBasic)) {
		fmt.Fprintln(errOut, "Usage: agent-deck telemetry level full|basic")
		return 2
	}
	want := telemetry.Level(rest[0])
	s := telemetry.LoadState()
	if want == telemetry.LevelFull && s.Level == telemetry.LevelBasic && s.Consent == telemetry.ConsentGranted {
		// Raising the level widens what is recorded: ask like consent does.
		if msg := telemetryConsentBlocked(false, interactive); msg != "" {
			fmt.Fprintln(errOut, "telemetry: "+msg)
			return 1
		}
		fmt.Fprint(errOut, "Record the full event set (hour and weekday, sessions, features, errors)? [y/N]: ")
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil || !isYesConfirmation(line) {
			fmt.Fprintln(out, "Level unchanged: basic.")
			return 0
		}
	}
	s, err := telemetry.SetLevel(want)
	if err != nil {
		fmt.Fprintf(errOut, "telemetry: %v\n", err)
		return 1
	}
	if jsonOut {
		return writeJSON(out, buildTelemetryStatus(s))
	}
	fmt.Fprintf(out, "Telemetry level: %s\n", telemetry.EffectiveLevel(s))
	return 0
}

// telemetryDoctorLine summarises the telemetry state in one line for doctor.
func telemetryDoctorLine() string {
	st := buildTelemetryStatus(telemetry.LoadState())
	if !st.Enabled {
		return "off (" + st.Reason + ")"
	}
	return fmt.Sprintf("on (%s); upload %s; %d event(s) spooled", st.Level, st.Upload, st.Spool.Events)
}

// maybeSendUninstallTelemetry asks one optional question and sends the
// uninstall event, only when consent was granted. It is the one synchronous
// send (2 s timeout); everything else is uploaded from the TUI.
func maybeSendUninstallTelemetry(in io.Reader, out io.Writer, askReason bool) {
	s := telemetry.LoadState()
	if ok, _ := telemetry.Enabled(s); !ok || !telemetry.Interactive() {
		return
	}
	reason := "skip"
	if askReason {
		fmt.Fprint(out, "Why are you uninstalling? (optional, anonymous) [1] not needed  [2] too complex  [3] bugs  [4] switching tool  [Enter] skip: ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		reason = map[string]string{"1": "not_needed", "2": "too_complex", "3": "bugs", "4": "switching_tool"}[strings.TrimSpace(line)]
		if reason == "" {
			reason = "skip"
		}
	}
	sum := session.TelemetrySummaryFromStorage()
	lastTool := ""
	if len(sum.Tools) > 0 {
		lastTool = sum.Tools[len(sum.Tools)-1]
	}
	telemetry.SendUninstall(context.Background(), sum.Sessions, lastTool, reason)
}

func printTelemetryHelp(w io.Writer) {
	fmt.Fprintf(w, `Usage: agent-deck telemetry <command> [--json]

Opt-in, anonymous usage data. OFF until you say yes; nothing is recorded or
sent before that. Full details and the field list: %s

Commands:
  status            Consent, install id, level, spool size, upload schedule and result
  on | enable       Show the question and require an interactive y (Enter is no)
  off | disable     Turn off; delete the install id, local spool and counters
  preview           Print the exact PostHog request bodies the next upload would send
  show-last         Print the exact body of the last acknowledged upload request
  reset-id          New random install id and salt; delete the spool and counters
  schema            Print the full allow-list (--markdown default, --json)
  level full|basic  Set the recording level (raising basic to full asks first)

Flags:
  --json      Machine-readable output for every command
  --yes, -y   unsupported: consent must come from a person at a terminal

Kill switches (win over stored consent, re-read on every run):
  DO_NOT_TRACK=1                      hard off
  AGENTDECK_TELEMETRY=0               hard off (any value but 1/true/yes/on/log; none of them enables)
  AGENTDECK_TELEMETRY=log             never sends: logs would-be events/uploads locally instead
  [telemetry] disabled = true         hard off, in config.toml
  [telemetry] level = "basic"         record only app.start, usage.daily and env.snapshot
  [telemetry] endpoint = URL          receiver base URL (default %s; re-asks consent)
  [telemetry] posthog_key / %s   PostHog project key; without one nothing is uploaded

When data is sent:
  Only from the interactive TUI, never on the day you said yes, then at most
  every 6 hours: completed local hours of events and completed days of daily
  counters, as PostHog /batch/ JSON. Never from CLI commands, CI, scripts,
  tests or inside an agent-deck session (they only record locally, if at all).
  The one exception is `+"`agent-deck uninstall`"+`, which sends one event at once.

Never sent: prompts, output, titles, paths, repo, host or user names, commands,
environment values, error messages, IP addresses (discarded by PostHog).
`, telemetry.DocsURL, telemetry.DefaultEndpoint, telemetry.EnvPostHogKey)
}
