package events

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	defaultMu     sync.Mutex
	defaultBus    *Bus
	defaultClosed bool
	tapMu         sync.Mutex
	tapQueue      chan queuedFrame
	tapDone       chan struct{}
	tapClosed     bool
	tapDropped    atomic.Uint64
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
// on first use. Readers and explicit Bus users call this. A disabled or
// unwritable bus degrades to an inert, always-no-op Bus with a single
// logged warning (see warnDisabled) — callers never need to nil-check it.
func Default() *Bus {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClosed {
		return disabledBus()
	}
	if defaultBus == nil {
		defaultBus = openDefault()
	}
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

// PublishDefault admits a producer tap without touching disk. The first
// open and all appends happen on the background writer. A full queue drops
// the tap so even a stalled disk cannot delay a producer.
func PublishDefault(kind, sessionID string, data any) {
	if disabled, explicit := envOverride(); explicit && disabled {
		warnDisabled("AGENTDECK_EVENTS_BUS disabled", nil)
		return
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	qf := queuedFrame{kind: kind, sessionID: sessionID, data: raw, ts: time.Now()}
	tapMu.Lock()
	if tapClosed {
		tapMu.Unlock()
		return
	}
	if tapQueue == nil {
		tapQueue = make(chan queuedFrame, defaultQueueCap)
		tapDone = make(chan struct{})
		go defaultTapLoop(tapQueue, tapDone)
	}
	select {
	case tapQueue <- qf:
	default:
		tapDropped.Add(1)
	}
	tapMu.Unlock()
}

func defaultTapLoop(queue <-chan queuedFrame, done chan<- struct{}) {
	defer close(done)
	b := Default()
	ticker := time.NewTicker(defaultFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case qf, ok := <-queue:
			if !ok {
				b.dropped.Add(tapDropped.Swap(0))
				return
			}
			b.enqueue(qf)
		case <-ticker.C:
			b.dropped.Add(tapDropped.Swap(0))
		}
	}
}

// CloseDefault is the process owner's shutdown hook. It drains and fsyncs
// accepted taps before a one-shot command or the TUI exits.
func CloseDefault() error {
	tapMu.Lock()
	if !tapClosed {
		tapClosed = true
		if tapQueue != nil {
			close(tapQueue)
		}
	}
	done := tapDone
	tapMu.Unlock()
	if done != nil {
		<-done
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultClosed = true
	if defaultBus == nil {
		return nil
	}
	return defaultBus.Close()
}

// resetDefaultForTest lets tests re-run openDefault() under a fresh
// HOME/XDG sandbox (testutil.IsolateHome pattern). Test-only.
func resetDefaultForTest() {
	_ = CloseDefault()
	defaultMu.Lock()
	defaultBus = nil
	defaultClosed = false
	defaultMu.Unlock()
	tapMu.Lock()
	tapQueue = nil
	tapDone = nil
	tapClosed = false
	tapDropped.Store(0)
	tapMu.Unlock()
}
