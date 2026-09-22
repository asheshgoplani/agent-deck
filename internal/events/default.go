package events

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	defaultOnce sync.Once
	defaultBus  *Bus
)

// disableEnvVar lets an operator or a test turn the bus off without touching
// config.toml. Any of "0", "false", "no" (case-insensitive) disables it;
// anything else (including unset) leaves it enabled.
const disableEnvVar = "AGENTDECK_EVENTS_BUS"

func envDisabled() bool {
	v := strings.TrimSpace(os.Getenv(disableEnvVar))
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return !b
	}
	return strings.EqualFold(v, "no") || strings.EqualFold(v, "off")
}

// Default returns the process-wide bus for the current profile, opening it
// on first use. Every producer publishes through this. A disabled or
// unwritable bus degrades to an inert, always-no-op Bus with a single
// logged warning (see warnDisabled) — callers never need to nil-check it.
func Default() *Bus {
	defaultOnce.Do(func() {
		defaultBus = openDefault()
	})
	return defaultBus
}

func openDefault() *Bus {
	if envDisabled() {
		warnDisabled("AGENTDECK_EVENTS_BUS disabled", nil)
		return disabledBus()
	}
	dir, err := busDir()
	if err != nil {
		warnDisabled("resolve bus dir", err)
		return disabledBus()
	}
	b, err := Open(dir)
	if err != nil {
		warnDisabled("open bus at "+dir, err)
		return disabledBus()
	}
	return b
}

// resetDefaultForTest lets tests re-run openDefault() under a fresh
// HOME/XDG sandbox (testutil.IsolateHome pattern). Test-only.
func resetDefaultForTest() {
	if defaultBus != nil {
		_ = defaultBus.Close()
	}
	defaultOnce = sync.Once{}
	defaultBus = nil
}
