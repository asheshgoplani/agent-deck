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

// disableEnvVar lets an operator or a test explicitly force the bus on or
// off without touching config.toml: "0"/"false"/"no"/"off" (case-
// insensitive) disables it. It is enabled by default in every process.
const disableEnvVar = "AGENTDECK_EVENTS_BUS"

// envOverride reports an explicit operator/test choice, if any. ok is false
// when the variable is unset, in which case the caller falls back to its
// own default.
func envOverride() (disabled bool, ok bool) {
	v := strings.TrimSpace(os.Getenv(disableEnvVar))
	if v == "" {
		return false, false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return !b, true
	}
	if strings.EqualFold(v, "no") || strings.EqualFold(v, "off") {
		return true, true
	}
	return false, true
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
	if disabled, explicit := envOverride(); explicit {
		if disabled {
			warnDisabled("AGENTDECK_EVENTS_BUS disabled", nil)
			return disabledBus()
		}
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

// OpenProfile creates a bus owned by a component with its own lifecycle.
// The caller must Close it after its producers stop.
func OpenProfile(profile string) *Bus {
	if disabled, explicit := envOverride(); explicit && disabled {
		return disabledBus()
	}
	dir, err := busDirFor(profile)
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

// CloseDefault is the process owner's shutdown hook. It drains and fsyncs
// accepted taps before a one-shot command or the TUI exits.
func CloseDefault() error {
	if defaultBus == nil {
		return nil
	}
	return defaultBus.Close()
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
