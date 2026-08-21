package verify

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PaneCapturer reads the live, uncached contents of a session's pane.
//
// It is deliberately CapturePaneFresh and not CapturePane: the cached variant
// answers from a 500 ms-old snapshot, which during a poll loop means reading
// the pane as it was *before* the command was sent and concluding the panel had
// already rendered. *tmux.Session satisfies this interface.
type PaneCapturer interface {
	CapturePaneFresh() (string, error)
}

// ScrollbackCapturer reads pane history. A panel taller than the visible pane
// is truncated by a plain capture, and a truncated panel parses into a smaller
// set of rows — which would silently narrow the comparison. When the capturer
// offers history, the final read uses it. *tmux.Session satisfies this too.
type ScrollbackCapturer interface {
	CaptureHistoryLines(lines int) (string, error)
}

// KeySender types into a session's pane. This is the mutation: running a live
// comparison sends a slash command to the user's agent.
type KeySender interface {
	SendKeysAndEnter(keys string) error
}

// ErrPanelTimeout is returned when the harness's panel never became readable
// within the budget.
var ErrPanelTimeout = errors.New("ctxinspect/verify: the harness's own accounting panel did not render in time")

// scrollbackLines is how much history the final capture reads. It is generous
// because the panel is what we came for and a partial one is worse than none;
// the read happens once per verification, never on a render path.
const scrollbackLines = 2000

// LiveOptions tunes the live capture.
type LiveOptions struct {
	// PollInterval is how often the pane is re-read while waiting.
	PollInterval time.Duration
	// Timeout bounds the wait for the panel to render.
	Timeout time.Duration
	// Settle is how long to keep reading after the panel first parses, so a
	// panel that draws in two frames is captured whole rather than half.
	Settle time.Duration
	// Sleep is the delay function; nil uses time.Sleep. Injected by tests so a
	// poll loop is exercised without real time passing.
	Sleep func(time.Duration)
}

// withDefaults fills unset fields with the shipped values.
func (o LiveOptions) withDefaults() LiveOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = 250 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Settle <= 0 {
		o.Settle = 750 * time.Millisecond
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	return o
}

// RunLive sends the harness's own accounting command to a live session and
// reads the resulting panel back.
//
// This mutates the user's session: it types a slash command into the agent's
// composer and presses Enter. Callers must have obtained explicit consent
// first; nothing in this package asks for it, and nothing on a TUI render path
// may call it.
//
// Callers must also have made the pane safe to type into before calling —
// waited for the agent to go idle and run the composer-draft guard in
// internal/send (issues #1409, #966) — because this function types the moment
// it is entered. It does not do that itself: the readiness rules are
// tool-shaped and live a layer up, next to the tool name. See
// awaitContextVerifySendable in cmd/agent-deck.
//
// The wait is driven by the spec's own parser rather than by a marker string,
// so the loop cannot decide the panel is ready and then fail to read it. After
// the panel first parses, capturing continues for the settle window and the
// last readable capture wins, which is what stops a two-frame draw from being
// parsed as a one-frame one.
func RunLive(ctx context.Context, spec Spec, pane PaneCapturer, keys KeySender, opts LiveOptions) (*HarnessReport, error) {
	if spec.Parse == nil || spec.Ready == nil {
		return nil, ErrUnverifiable{Adapter: spec.Harness}
	}
	if pane == nil || keys == nil {
		return nil, errors.New("ctxinspect/verify: a live capture needs both a pane capturer and a key sender")
	}
	opts = opts.withDefaults()

	// Check the deadline before typing, not after: an already-cancelled run
	// must not leave a stray slash command in the user's conversation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := keys.SendKeysAndEnter(spec.Command); err != nil {
		return nil, fmt.Errorf("sending %s to the session: %w", spec.Command, err)
	}

	deadline := time.Now().Add(opts.Timeout)
	var (
		best      *HarnessReport
		firstSeen time.Time
	)
	for {
		if err := ctx.Err(); err != nil {
			if best != nil {
				return best, nil
			}
			return nil, err
		}

		text, err := pane.CapturePaneFresh()
		if err != nil {
			return nil, fmt.Errorf("reading the session pane: %w", err)
		}
		if spec.Ready(text) {
			if rep, perr := spec.Parse(text); perr == nil {
				best = rep
				if firstSeen.IsZero() {
					firstSeen = time.Now()
				}
			}
		}

		switch {
		case best != nil && time.Since(firstSeen) >= opts.Settle:
			return finalCapture(spec, pane, best), nil
		case time.Now().After(deadline):
			if best != nil {
				return finalCapture(spec, pane, best), nil
			}
			return nil, fmt.Errorf("%w: %s produced nothing readable within %s", ErrPanelTimeout, spec.Command, opts.Timeout)
		}
		opts.Sleep(opts.PollInterval)
	}
}

// finalCapture re-reads the panel from scrollback when the capturer offers it,
// so a panel taller than the visible pane is not silently truncated. The
// scrollback read is best-effort: if it fails or parses to fewer rows than the
// pane read, the pane read stands.
func finalCapture(spec Spec, pane PaneCapturer, fallback *HarnessReport) *HarnessReport {
	sc, ok := pane.(ScrollbackCapturer)
	if !ok {
		return fallback
	}
	text, err := sc.CaptureHistoryLines(scrollbackLines)
	if err != nil {
		return fallback
	}
	rep, err := spec.Parse(text)
	if err != nil || len(rep.Figures) < len(fallback.Figures) {
		return fallback
	}
	return rep
}
