package main

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// stalledQueueGrace is how long a Claude composer must keep showing the
// queued-messages hint, with the hook reporting the session idle the whole
// time, before the sender reads Claude's queue as stalled. Claude drains its
// queue itself the moment a turn ends, so a hint that outlives an idle hook
// for this long is not waiting on any turn.
const stalledQueueGrace = 1500 * time.Millisecond

// stalledQueueFlush presses the one Enter that submits a stalled Claude
// queue (ASK-200).
//
// Claude Code can keep submitted prompts in its queued-messages list after
// its turn has ended: the composer is empty and shows "Press up to edit
// queued messages", the Stop hook has written "waiting", and nothing is ever
// taken up until an Enter is pressed on that empty composer. The attribution
// gate (send.EnterAttribution.BareEnterBlocked) refuses every bare Enter at
// that hint, which is right for a busy target (ASK-95: the queue waits on a
// live turn) and wrong for an idle one, where it left a conductor's alerts
// queued for hours while every send exited 0.
//
// The press is made only when all of these hold:
//   - the target is Claude-compatible;
//   - the hook status is fresh and idle (known, not busy) on every frame;
//   - the composer shows the queued hint (so it holds no draft) and no
//     prompt suggestion;
//   - that has held for stalledQueueGrace;
//   - this send has not pressed it yet (one press per send).
//
// The queue holds only prompts somebody already submitted with Enter, so the
// press submits nothing that was not already sent; it never touches a draft.
type stalledQueueFlush struct {
	since   time.Time
	pressed bool
	// heldIdle is true while the last frame observed showed the stalled
	// shape (idle hook, queued hint), pressed or not.
	heldIdle bool
}

// observe folds one frame into the flush state and presses Enter once the
// queue reads as stalled. It reports whether it pressed on this frame.
func (f *stalledQueueFlush) observe(target send.EnterPresser, c send.PaneCapture, opts sendRetryOptions, now time.Time) bool {
	if !session.IsClaudeCompatible(opts.tool) || !c.OK || !opts.hookIdleNow() ||
		send.ComposerBodyIsSuggestion(c.Raw) || !send.ComposerShowsQueuedHint(c.Raw, tmux.StripANSI) {
		f.since, f.heldIdle = time.Time{}, false
		return false
	}
	f.heldIdle = true
	if f.since.IsZero() {
		f.since = now
	}
	if f.pressed || now.Sub(f.since) < stalledQueueGrace || target == nil {
		return false
	}
	f.pressed = target.SendEnter() == nil
	return f.pressed
}
