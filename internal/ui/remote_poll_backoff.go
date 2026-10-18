package ui

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Remote poll backoff (docs/remote-polling-contract.md).
//
// Every remote session poll runs `agent-deck list --json --stats` on the
// remote host. A host that is already struggling answers slowly or not at
// all, and polling it again on the normal cadence only adds load: on
// 2026-10-18 a throttled host collected ~105 concurrent listings from its
// pollers and could not recover until they were cleared by hand.
//
// The per-host single-flight (beginRemotePoll / remotePollActive) already
// keeps a second poll from starting while one runs. Backoff adds the other
// half: a poll that FAILED (error, timeout) pushes the host's next automatic
// poll out exponentially — 2x, 4x, 8x ... the interval, capped at
// remotePollBackoffMax. A poll that SUCCEEDED but was SLOW (longer than half
// the poll interval) rests the host one interval and never escalates: the
// host answers, so its rows must not go stale for minutes. The first fast
// success clears it. The host's last-good rows stay on screen meanwhile (a
// skipped poll is merged like a failed one). Ctrl+R, the explicit retry and
// `session focus --remote` clear the backoff, so a user asking for a
// refresh gets one.

// remotePollBackoffMax caps how long a slow or failing host waits between
// automatic polls.
const remotePollBackoffMax = 5 * time.Minute

// remotePollBackoff is one host's backoff: how many polls in a row failed,
// and the earliest time the next automatic poll may start. A slow success
// sets until with no strikes. saved records whether the poll that set it
// reached the poll-state cache (seedRemotePolls reads a missing saved record
// as an explicit reset).
type remotePollBackoff struct {
	strikes int
	until   time.Time
	saved   bool
}

// active reports whether an automatic poll must still wait at now.
func (b remotePollBackoff) active(now time.Time) bool {
	return now.Before(b.until)
}

// empty reports whether the backoff holds nothing.
func (b remotePollBackoff) empty() bool {
	return b.strikes == 0 && b.until.IsZero()
}

// remotePollSlow reports whether a poll that took elapsed counts as slow for
// a poll interval: longer than half of it.
func remotePollSlow(elapsed, interval time.Duration) bool {
	return interval > 0 && elapsed > interval/2
}

// nextRemotePollBackoff is the backoff after one poll that ended at now. A
// fast success resets it; a slow success clears the strikes and holds the
// host one interval; a failed poll adds a strike and holds the host for
// interval * 2^strikes, capped at remotePollBackoffMax.
func nextRemotePollBackoff(prev remotePollBackoff, now time.Time, elapsed, interval time.Duration, err error) remotePollBackoff {
	if err == nil {
		if !remotePollSlow(elapsed, interval) {
			return remotePollBackoff{}
		}
		return remotePollBackoff{until: now.Add(remotePollBackoffDelay(0, interval))}
	}
	strikes := prev.strikes + 1
	return remotePollBackoff{strikes: strikes, until: now.Add(remotePollBackoffDelay(strikes, interval))}
}

// remotePollBackoffDelay is interval * 2^strikes, capped.
func remotePollBackoffDelay(strikes int, interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = time.Duration(session.DefaultRemoteSessionRefreshSecs) * time.Second
	}
	delay := interval
	for i := 0; i < strikes; i++ {
		delay *= 2
		if delay >= remotePollBackoffMax {
			return remotePollBackoffMax
		}
	}
	return delay
}

// remotePollInterval is the configured automatic poll cadence.
func (h *Home) remotePollInterval() time.Duration {
	interval := h.remoteSessionRefreshSec
	if interval <= 0 {
		interval = session.DefaultRemoteSessionRefreshSecs
	}
	return time.Duration(interval) * time.Second
}

// remoteClock is the clock poll timing and backoff read; tests inject one.
func (h *Home) remoteClock() time.Time {
	if h.remoteNow != nil {
		return h.remoteNow()
	}
	return time.Now()
}

// recordRemotePollBackoffLocked updates name's backoff after a poll; saved
// says whether the poll reached the poll-state cache. A shutdown
// cancellation says nothing about the host and leaves it as is. Caller holds
// remoteSessionsMu.
func (h *Home) recordRemotePollBackoffLocked(name string, elapsed time.Duration, err error, saved bool) {
	if errors.Is(err, context.Canceled) {
		return
	}
	now := h.remoteClock()
	interval := h.remotePollInterval()
	prev := h.remotePollBackoffs[name]
	next := nextRemotePollBackoff(prev, now, elapsed, interval, err)
	if next.empty() {
		if !prev.empty() {
			uiLog.Info("remote_poll_backoff_reset", slog.String("remote", name), slog.Duration("elapsed", elapsed))
		}
		delete(h.remotePollBackoffs, name)
		return
	}
	if h.remotePollBackoffs == nil {
		h.remotePollBackoffs = make(map[string]remotePollBackoff)
	}
	next.saved = saved
	h.remotePollBackoffs[name] = next
	reason := "slow"
	if err != nil {
		reason = "failed"
	}
	uiLog.Info("remote_poll_backoff",
		slog.String("remote", name),
		slog.String("reason", reason),
		slog.Duration("elapsed", elapsed),
		slog.Int("strikes", next.strikes),
		slog.Duration("next_poll_in", next.until.Sub(now)))
}

// clearRemotePollBackoff lets the next poll of name (every remote when name
// is empty) start at once: the user asked for it.
func (h *Home) clearRemotePollBackoff(name string) {
	h.remoteSessionsMu.Lock()
	defer h.remoteSessionsMu.Unlock()
	if name == "" {
		h.remotePollBackoffs = nil
		return
	}
	delete(h.remotePollBackoffs, name)
}
