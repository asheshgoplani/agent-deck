package telemetry

import (
	"os"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/managed"
)

// EnvPostHogKey overrides the PostHog project API key.
const EnvPostHogKey = "AGENTDECK_POSTHOG_KEY"

// defaultPostHogKey is the compiled-in project API key. Release builds set it
// with -ldflags "-X .../internal/telemetry.defaultPostHogKey=..." from the
// AGENTDECK_POSTHOG_KEY repository secret (.goreleaser.yml); dev builds leave
// it empty and, without an env or config key, spool locally and never upload.
var defaultPostHogKey = ""

var (
	configKey   string
	configLevel Level
)

var postHogKeyPattern = regexp.MustCompile(`^phc_[A-Za-z0-9_-]{16,80}$`)

// SetPostHogKey records the config.toml [telemetry].posthog_key value.
func SetPostHogKey(k string) { configKey = strings.TrimSpace(k) }

// SetConfigLevel records the config.toml [telemetry].level value. Config can
// only lower the level: "basic" forces basic, anything else defers to state.
func SetConfigLevel(l string) {
	configLevel = ""
	if strings.EqualFold(strings.TrimSpace(l), string(LevelBasic)) {
		configLevel = LevelBasic
	}
}

// PostHogKey returns the effective project API key and whether it is usable.
// A compiled-in key always wins: consent binds to the endpoint, not the key,
// so nothing in the environment or config may redirect a release build's
// uploads to another project. Builds without one (dogfooding) take
// AGENTDECK_POSTHOG_KEY, then config.
func PostHogKey() (string, bool) {
	k, _ := postHogKeyWithSource()
	return k, postHogKeyPattern.MatchString(k)
}

// PostHogKeySource names where the effective key comes from, never the key
// itself: KeySourceCompiled, KeySourceEnv, KeySourceConfig or KeySourceNone.
func PostHogKeySource() string {
	_, src := postHogKeyWithSource()
	return src
}

// Values of PostHogKeySource.
const (
	KeySourceCompiled = "compiled-in"
	KeySourceEnv      = "environment"
	KeySourceConfig   = "config"
	KeySourceNone     = "none"
)

func postHogKeyWithSource() (string, string) {
	if defaultPostHogKey != "" {
		return defaultPostHogKey, KeySourceCompiled
	}
	if k := strings.TrimSpace(os.Getenv(EnvPostHogKey)); k != "" {
		return k, KeySourceEnv
	}
	if configKey != "" {
		return configKey, KeySourceConfig
	}
	return "", KeySourceNone
}

// Configured reports whether an upload destination exists (a valid key).
func Configured() bool {
	_, ok := PostHogKey()
	return ok
}

// EffectiveLevel combines the stored level with the config and managed
// ceilings: the lowest of the three wins.
func EffectiveLevel(s *State) Level {
	if configLevel == LevelBasic || managedLevel == LevelBasic || s.Level == LevelBasic {
		return LevelBasic
	}
	return LevelFull
}

// Managed configuration (internal/managed): a company can lower the level or
// turn telemetry off for its machines, and name its org. It can never grant
// consent or raise a level above what the person chose.
var (
	managedLevel      Level // "" or LevelBasic
	managedOff        bool
	managedUnreadable bool
	orgID             string
	orgSource         = OrgSourceNone
)

// Values of OrgSource.
const (
	OrgSourceManaged = "managed"
	OrgSourceConfig  = "config"
	OrgSourceNone    = "none"
)

// SetManaged records what the managed file says. A file that exists but
// cannot be read or validated fails closed: telemetry is treated as off.
func SetManaged(r managed.Result) {
	managedLevel, managedOff, managedUnreadable = "", false, false
	if r.Err != nil {
		managedUnreadable = true
		return
	}
	switch r.TelemetryLevel {
	case managed.LevelOff:
		managedOff = true
	case managed.LevelBasic:
		managedLevel = LevelBasic
	}
}

// SetOrgID records the org id: the managed one wins over the config.toml
// [telemetry].org_id. An id that is not a valid opaque slug or UUID is
// ignored, so a typo never sends a name or an email.
func SetOrgID(managedID, configID string) {
	orgID, orgSource = "", OrgSourceNone
	for _, c := range []struct{ id, src string }{{managedID, OrgSourceManaged}, {configID, OrgSourceConfig}} {
		if id := strings.TrimSpace(c.id); id != "" {
			orgID, orgSource = id, c.src
			if !managed.ValidOrgID(id) {
				orgID, orgSource = "", c.src+" (invalid, ignored)"
			}
			return
		}
	}
}

// OrgID returns the effective org id, or "" when none is set. Detailed
// events carry it as the "org" property only when it is set.
func OrgID() string { return orgID }

// OrgSource names where OrgID comes from: OrgSourceManaged, OrgSourceConfig
// or OrgSourceNone.
func OrgSource() string { return orgSource }

// ManagedLevelCeiling returns the managed ceiling for status output: "off",
// "basic" or "" for none.
func ManagedLevelCeiling() string {
	switch {
	case managedOff || managedUnreadable:
		return managed.LevelOff
	case managedLevel == LevelBasic:
		return managed.LevelBasic
	}
	return ""
}
