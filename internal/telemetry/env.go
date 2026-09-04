package telemetry

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// Environment variables that hard-disable telemetry regardless of stored
// consent. Both are re-read on every call; nothing is cached.
const (
	// EnvTelemetry, when set to anything other than 1/true/yes/on, disables
	// telemetry (so "0", "false", "off", "disabled", a typo, or an empty
	// value all mean off). A truthy value is a no-op: an env var can turn
	// telemetry off, never on.
	EnvTelemetry = "AGENTDECK_TELEMETRY"
	// EnvDoNotTrack follows https://consoledonottrack.com: any truthy value
	// disables telemetry.
	EnvDoNotTrack = "DO_NOT_TRACK"
)

// sessionMarkers are set by agent-deck inside every session it launches (the
// tmux environment carries AGENTDECK_INSTANCE_ID; AGENT_DECK_SESSION_ID is
// the older spelling still honoured by the CLI). A process running there is
// driven by an agent, not by the person at the keyboard, so it must neither
// prompt nor send.
var sessionMarkers = []string{"AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID"}

// ciMarkers are env vars whose presence means a CI or automation context.
var ciMarkers = []string{
	"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE",
	"CIRCLECI", "TRAVIS", "JENKINS_URL", "TEAMCITY_VERSION", "TF_BUILD",
	"DRONE", "APPVEYOR", "CODEBUILD_BUILD_ID", "AGENTDECK_NONINTERACTIVE",
}

func isFalsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && !isFalsy(v)
}

// isExplicitOn reports the only values of AGENTDECK_TELEMETRY that do NOT disable telemetry (they do not enable it either).
func isExplicitOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// DisableReason explains why telemetry is off, for `telemetry status`.
type DisableReason string

const (
	ReasonNone         DisableReason = ""
	ReasonEnvTelemetry DisableReason = "AGENTDECK_TELEMETRY is set (to a value other than 1/true/yes/on)"
	ReasonEnvDNT       DisableReason = "DO_NOT_TRACK is set"
	ReasonConfig       DisableReason = "[telemetry].disabled = true in config.toml"
	ReasonConfigError  DisableReason = "config.toml could not be read, so telemetry is treated as disabled"
	ReasonUndecided    DisableReason = "consent has not been given (default off)"
	ReasonDeclined     DisableReason = "consent was declined"
)

// configDisabled / configUnreadable are set by the CLI layer from config.toml
// [telemetry].disabled. They can only turn telemetry off; the config file
// cannot grant consent. An unreadable config fails closed.
var (
	configDisabled   bool
	configUnreadable bool
)

// SetConfigDisabled records the config.toml [telemetry].disabled value.
func SetConfigDisabled(disabled bool) {
	configDisabled = disabled
	configUnreadable = false
}

// SetConfigUnreadable marks config.toml as unparseable: the user may have written [telemetry].disabled = true there, so telemetry is treated as off.
func SetConfigUnreadable() { configUnreadable = true }

// HardDisableReason returns the first hard-disable that applies, or ReasonNone.
func HardDisableReason() DisableReason {
	if v, ok := os.LookupEnv(EnvTelemetry); ok && !isExplicitOn(v) {
		return ReasonEnvTelemetry
	}
	if isTruthy(os.Getenv(EnvDoNotTrack)) {
		return ReasonEnvDNT
	}
	if configDisabled {
		return ReasonConfig
	}
	if configUnreadable {
		return ReasonConfigError
	}
	return ReasonNone
}

// HardDisabled reports whether an env var or config switch forces telemetry
// off regardless of consent.
func HardDisabled() bool { return HardDisableReason() != ReasonNone }

// IsCI reports whether any CI marker env var is set to a non-false value.
func IsCI() bool {
	for _, k := range ciMarkers {
		if v, ok := os.LookupEnv(k); ok && !isFalsy(v) {
			return true
		}
	}
	return false
}

// InsideSession reports whether this process runs inside an agent-deck session, where the caller is an agent rather than the person.
func InsideSession() bool {
	for _, k := range sessionMarkers {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

// isTerminalFn is a test seam for TTY detection.
var isTerminalFn = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Interactive reports whether a human is plausibly at this terminal: stdin and stdout are TTYs, no CI marker is set, and the process is not inside an agent-deck session.
func Interactive() bool {
	return isTerminalFn() && !IsCI() && !InsideSession()
}
