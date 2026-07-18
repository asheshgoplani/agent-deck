package send

import (
	"fmt"
	"time"
)

// DeferPollInterval is the fixed cadence at which `session send --defer-if-busy`
// re-checks the target's hook-driven status while holding delivery.
const DeferPollInterval = 2 * time.Second

// maxConsecutiveFetchErrors bounds transient status-fetch failures before
// WaitUntilNotBusy gives up. Without this, a removed or renamed target would
// spin returning an error on every poll for the entire defer timeout.
const maxConsecutiveFetchErrors = 3

// StatusIsBusy reports whether a hook-driven status string represents a target
// that is mid-turn (actively generating) or still booting. These are the only
// states `--defer-if-busy` holds for; every other state
// (waiting/idle/error/stopped/queued/unknown) means the composer is free and
// delivery may proceed.
//
// This keys off the SAME hook-driven signal `agent-deck list --json` reports
// (Claude's UserPromptSubmit hook writes "running", the Stop hook writes
// "waiting"), which is a true turn-finished edge — unlike WaitForAgentReady's
// pane-content-diff heuristic that false-positives to idle during tool calls
// and thinking pauses (issue #1578).
func StatusIsBusy(status string) bool {
	switch status {
	case "running", "starting":
		return true
	default:
		return false
	}
}

// WaitUntilNotBusy polls fetchStatus until the target is no longer busy (see
// StatusIsBusy), the timeout elapses, or the fetch fails persistently.
//
// It is the core of `session send --defer-if-busy`: rather than interrupting a
// generating target with the composer-draft Ctrl+C guard (GuardComposerDraft,
// issue #1409), the sender holds until the target's hook-driven status shows
// the turn is over, then lets the normal readiness/guard/send pipeline run.
//
// sleep is injected so tests run without real delay; production passes
// time.Sleep. A nil sleep defaults to time.Sleep; a non-positive poll defaults
// to DeferPollInterval. On timeout the message is NOT delivered and a non-zero
// error is returned (drop-on-timeout semantics).
func WaitUntilNotBusy(fetchStatus func() (string, error), timeout, poll time.Duration, sleep func(time.Duration)) error {
	if poll <= 0 {
		poll = DeferPollInterval
	}
	if sleep == nil {
		sleep = time.Sleep
	}

	deadline := time.Now().Add(timeout)
	consecutiveErrs := 0
	lastStatus := ""

	for {
		status, err := fetchStatus()
		if err != nil {
			consecutiveErrs++
			if consecutiveErrs >= maxConsecutiveFetchErrors {
				return fmt.Errorf("defer-if-busy: giving up after %d consecutive status-fetch errors: %w", consecutiveErrs, err)
			}
		} else {
			consecutiveErrs = 0
			lastStatus = status
			if !StatusIsBusy(status) {
				return nil
			}
		}

		if timeout > 0 && !time.Now().Before(deadline) {
			if lastStatus == "" {
				lastStatus = "unknown"
			}
			return fmt.Errorf("defer-if-busy: target still busy (%s) after %s", lastStatus, timeout)
		}

		sleep(poll)
	}
}
