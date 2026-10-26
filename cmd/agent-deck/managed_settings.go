package main

import (
	"os"

	"github.com/asheshgoplani/agent-deck/internal/crashreport"
	"github.com/asheshgoplani/agent-deck/internal/managed"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// managedConfig is what initTelemetrySettings read from the managed file
// (internal/managed), kept for `telemetry status`.
var managedConfig managed.Result

// crashDecision says whether crash reports are on, from which DSN source,
// or why not. The DSN itself is never printed.
type crashDecision struct {
	dsn    string
	Source string // "managed" or "config" when on
	Reason string // why off; "" when on
}

func (d crashDecision) On() bool { return d.dsn != "" }

// String is the status line: "on (managed DSN)" or "off (<reason>)".
func (d crashDecision) String() string {
	if d.On() {
		return "on (" + d.Source + " DSN)"
	}
	return "off (" + d.Reason + ")"
}

var crashState = crashDecision{Reason: "no DSN configured"}

// decideCrashReporting: crash reports need a DSN from the managed file or
// config.toml (the managed one wins) and no off switch:
//   - every telemetry hard-off (DO_NOT_TRACK, AGENTDECK_TELEMETRY=0 or log,
//     [telemetry].disabled, a managed telemetry_level = "off", an
//     unreadable config or managed file),
//   - a declined usage-data consent (`agent-deck telemetry off`, a "no" at
//     the prompt): the person's no covers crash reports too,
//   - CI.
//
// An undecided consent does not block them: the company that set the DSN
// turned them on for its machines and each person can turn them off
// (TELEMETRY.md "Crash reports").
func decideCrashReporting(m managed.Result, configDSN string) crashDecision {
	d := crashDecision{dsn: m.CrashDSN, Source: "managed"}
	switch {
	case m.CrashDSN != "":
	case configDSN == "":
		return crashDecision{Reason: "no DSN configured"}
	case managed.ValidateDSN(configDSN) != nil:
		return crashDecision{Reason: "[telemetry].crash_dsn in config.toml is not a valid DSN"}
	default:
		d = crashDecision{dsn: configDSN, Source: "config"}
	}
	if r := telemetry.HardDisableReason(); r != telemetry.ReasonNone {
		return crashDecision{Reason: string(r)}
	}
	if telemetry.LogMode() {
		return crashDecision{Reason: "AGENTDECK_TELEMETRY=log never sends"}
	}
	if telemetry.LoadState().Consent == telemetry.ConsentDeclined {
		return crashDecision{Reason: string(telemetry.ReasonDeclined)}
	}
	if telemetry.IsCI() {
		return crashDecision{Reason: "running in CI"}
	}
	return d
}

// crashReportsOffAfterDecline stops crash reporting in this process once
// the person declines usage data, so the no applies at once, not only from
// the next run.
func crashReportsOffAfterDecline() {
	crashreport.Close()
	crashState = crashDecision{Reason: string(telemetry.ReasonDeclined)}
}

// initCrashReporting starts crash reporting when crashState is on. With no
// DSN it does nothing at all (no client, goroutine or network).
func initCrashReporting() {
	if !crashState.On() {
		return
	}
	home, _ := os.UserHomeDir()
	if _, err := crashreport.Init(crashreport.Config{
		DSN: crashState.dsn, Release: Version, OrgID: telemetry.OrgID(), Home: home,
	}); err != nil {
		crashState = crashDecision{Reason: "the DSN was refused"}
	}
}

// managedStatusLine summarises the managed file for `telemetry status`.
func managedStatusLine(m managed.Result) string {
	switch {
	case !m.Present:
		return "none"
	case m.Err != nil:
		return m.Path + " (unreadable or invalid; telemetry and crash reports off)"
	}
	s := m.Path + " (level ceiling " + orDash(m.TelemetryLevel)
	if m.UpdateChannel != "" {
		s += ", update channel " + m.UpdateChannel
	}
	return s + ")"
}
