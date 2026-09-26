package telemetry

import (
	"os"
	"regexp"
	"strings"
)

// EnvPostHogKey overrides the PostHog project API key.
const EnvPostHogKey = "AGENTDECK_POSTHOG_KEY"

// defaultPostHogKey is the compiled-in project API key. It stays empty until
// the maintainer's PostHog project exists (TELEMETRY.md, "Wiring the key");
// with no key the client spools locally and never uploads. It can also be set
// at build time with -ldflags "-X .../internal/telemetry.defaultPostHogKey=phc_...".
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
	k := defaultPostHogKey
	if k == "" {
		k = strings.TrimSpace(os.Getenv(EnvPostHogKey))
	}
	if k == "" {
		k = configKey
	}
	return k, postHogKeyPattern.MatchString(k)
}

// Configured reports whether an upload destination exists (a valid key).
func Configured() bool {
	_, ok := PostHogKey()
	return ok
}

// EffectiveLevel combines the stored level with the config ceiling.
func EffectiveLevel(s *State) Level {
	if configLevel == LevelBasic || s.Level == LevelBasic {
		return LevelBasic
	}
	return LevelFull
}
