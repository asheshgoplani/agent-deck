package events

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var (
	defaultOnce sync.Once
	defaultBus  *Bus
)

// disableEnvVar lets an operator or a test explicitly force the bus on or
// off without touching config.toml: "0"/"false"/"no"/"off" (case-
// insensitive) disables it, any other non-empty value force-enables it even
// under `go test`. Unset defers to the default for the context (see
// openDefault): enabled in production, disabled under `go test`.
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
	} else if testing.Testing() {
		// Default() is a process-wide singleton whose writer runs for the
		// life of the process — under `go test`, that process is the whole
		// package's test binary, so an always-on background goroutine here
		// trips every OTHER test's goroutine-leak checker (e.g.
		// internal/watcher's engine_test.go), not just this package's. Tests
		// that want a real bus open one directly via Open() (this package's
		// own bus_test.go does exactly that) instead of going through the
		// producer-facing singleton. AGENTDECK_EVENTS_BUS=1 overrides this.
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
