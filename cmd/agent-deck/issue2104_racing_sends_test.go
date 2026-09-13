package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Issue #2104: two `session send` processes racing for one Claude pane must
// never emit an interrupt sequence into the composer.
//
// There is no cross-process lock on sends, so sender B's composer guard can
// capture the pane while sender A's body is still landing and classify A's
// half-typed text as a foreign operator draft. The old guard answered that
// with Ctrl-C, and pressed Ctrl-C a second time when the composer did not
// read back as empty within ClearWait (it cannot: A is still typing). Two
// Ctrl-C presses in quick succession is Claude Code's exit gesture, so the
// race killed the conductor session four times in three days.
//
// racingClaudePane is a shared fake of that pane. It models the three facts
// the race depends on and nothing else:
//
//   - a body is typed in chunks and only submitted by a LATER Enter, exactly
//     as the tmux transport does (chunked paste, 100ms settle, Enter);
//   - a capture shows what Claude has DRAWN, which lags what it holds by a
//     render frame (redrawDelay), so a just-cleared composer keeps reading
//     as occupied for a while: the shape of death #4 in the issue, where the
//     redraw after /clear outlasted the guard's ClearWait;
//   - a single Ctrl-C clears the input and arms the exit window;
//   - a second Ctrl-C inside that window exits the session: every later
//     capture fails and the status is "error".
// ---------------------------------------------------------------------------

type racingClaudePane struct {
	mu sync.Mutex

	composer  string
	frames    []composerFrame
	submitted []string
	exited    bool
	exitArmed time.Time

	// ctrlCTimes records every Ctrl-C the pane received, in order.
	ctrlCTimes []time.Time

	// chunkSize / chunkDelay pace the fake typing so a concurrent capture can
	// observe a partially landed body, as it can against a real pane.
	chunkSize  int
	chunkDelay time.Duration
	// exitWindow is Claude Code's "Press Ctrl-C again to exit" window.
	exitWindow time.Duration
	// redrawDelay is how long a change to the composer takes to reach the
	// screen a capture reads.
	redrawDelay time.Duration
}

// composerFrame is the composer content Claude held from at onwards.
type composerFrame struct {
	at      time.Time
	content string
}

// setComposer records a change to what the composer holds; captures see it
// once redrawDelay has passed. Caller holds p.mu.
func (p *racingClaudePane) setComposer(content string) {
	p.composer = content
	p.frames = append(p.frames, composerFrame{at: time.Now(), content: content})
}

// drawn is the composer content a capture taken now would show. Caller
// holds p.mu.
func (p *racingClaudePane) drawn() string {
	visibleBy := time.Now().Add(-p.redrawDelay)
	shown := ""
	for _, f := range p.frames {
		if f.at.After(visibleBy) {
			break
		}
		shown = f.content
	}
	return shown
}

func (p *racingClaudePane) typeBody(body string) {
	for len(body) > 0 {
		n := p.chunkSize
		if n <= 0 || n > len(body) {
			n = len(body)
		}
		p.mu.Lock()
		p.setComposer(p.composer + body[:n])
		p.mu.Unlock()
		body = body[n:]
		time.Sleep(p.chunkDelay)
	}
}

func (p *racingClaudePane) SendKeysChunked(body string) error {
	p.typeBody(body)
	return nil
}

func (p *racingClaudePane) SendKeysAndEnter(body string) error {
	p.typeBody(body)
	time.Sleep(p.chunkDelay)
	return p.SendEnter()
}

func (p *racingClaudePane) SendEnter() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return errors.New("pane is gone")
	}
	if p.composer != "" {
		// Claude submits whatever the composer holds, intact or not.
		p.submitted = append(p.submitted, p.composer)
		p.setComposer("")
	}
	return nil
}

func (p *racingClaudePane) SendCtrlC() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.ctrlCTimes = append(p.ctrlCTimes, now)
	if p.exited {
		return errors.New("pane is gone")
	}
	p.setComposer("")
	if !p.exitArmed.IsZero() && now.Sub(p.exitArmed) < p.exitWindow {
		p.exited = true
		return nil
	}
	p.exitArmed = now
	return nil
}

func (p *racingClaudePane) GetStatus() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return "error", nil
	}
	if len(p.submitted) > 0 {
		return "active", nil
	}
	return "waiting", nil
}

func (p *racingClaudePane) CapturePaneFresh() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		return "", errors.New("can't find pane")
	}
	return claudeComposer(p.drawn()), nil
}

// composerShowsText reports whether a capture taken now would show input.
func (p *racingClaudePane) composerShowsText() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.drawn() != ""
}

func (p *racingClaudePane) snapshot() (submitted []string, exited bool, ctrlCTimes []time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.submitted...), p.exited, append([]time.Time(nil), p.ctrlCTimes...)
}

// TestIssue2104RacingSendsNeverEmitInterrupt reproduces the collision from
// issue #2104 through the production executeSend pipeline: sender A's body
// is still landing when sender B's guard runs. Whatever the guard decides,
// it must decide it with captures only. Any Ctrl-C into a Claude pane is a
// keystroke the operator did not press, and two of them exit the session.
func TestIssue2104RacingSendsNeverEmitInterrupt(t *testing.T) {
	const (
		bodyA = "[SENDER A] lane timer: review the waiting sessions and report back"
		bodyB = "[SENDER B] inbox nudge: a new transition notification is waiting"
	)
	pane := &racingClaudePane{
		chunkSize:   4,
		chunkDelay:  10 * time.Millisecond,
		exitWindow:  time.Second,
		redrawDelay: 40 * time.Millisecond,
	}
	// Both senders run the --no-wait shape the conductor lanes use, scaled
	// down so the guard reaches its bound while A is still typing and so a
	// redraw outlasts guardClearWait, the way the post-/clear redraw
	// outlasted the production 1s ClearWait. A guard that answers an
	// occupied composer with Ctrl-C presses it twice here.
	tuning := testGuardTuning(sendRetryOptions{maxRetries: 20, checkDelay: 5 * time.Millisecond, verifyDelivery: true})
	tuning.guardHold = 20 * time.Millisecond
	tuning.guardPoll = 5 * time.Millisecond
	tuning.guardClearWait = 30 * time.Millisecond

	type outcome struct {
		res sendDeliveryResult
		err error
	}
	var wg sync.WaitGroup
	outcomes := make([]outcome, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := executeSend(pane, "claude", bodyA, true, tuning)
		outcomes[0] = outcome{res, err}
	}()

	// B arrives once A's body has started to show in the composer.
	deadline := time.Now().Add(2 * time.Second)
	for !pane.composerShowsText() {
		if time.Now().After(deadline) {
			t.Fatal("sender A never started typing")
		}
		time.Sleep(time.Millisecond)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := executeSend(pane, "claude", bodyB, true, tuning)
		outcomes[1] = outcome{res, err}
	}()
	wg.Wait()

	submitted, exited, ctrlCTimes := pane.snapshot()

	if len(ctrlCTimes) != 0 {
		t.Errorf("racing sends emitted %d Ctrl-C into the composer; the guard must only capture, never interrupt (issue #2104)", len(ctrlCTimes))
	}
	for i := 1; i < len(ctrlCTimes); i++ {
		if gap := ctrlCTimes[i].Sub(ctrlCTimes[i-1]); gap < pane.exitWindow {
			t.Errorf("Ctrl-C #%d followed #%d after %v, inside Claude's %v exit window", i+1, i, gap, pane.exitWindow)
		}
	}
	if exited {
		t.Errorf("the Claude session exited under two racing sends: A=%+v/%v B=%+v/%v", outcomes[0].res, outcomes[0].err, outcomes[1].res, outcomes[1].err)
	}

	// Whatever reached Claude must be a whole message, never a body with
	// another sender's keystrokes cut out of or merged into it.
	for _, got := range submitted {
		if got != bodyA && got != bodyB {
			t.Errorf("corrupted submission %q", got)
		}
	}
	if !submittedIntact(submitted, bodyA) {
		t.Errorf("sender A's message never reached Claude intact: submitted=%q A=%+v/%v", submitted, outcomes[0].res, outcomes[0].err)
	}
	// B is either delivered after A or refused with its keystrokes withheld;
	// both preserve the session. Only a delivery that mutated the pane is
	// wrong.
	// The refusal status is spelled out so this file also compiles against
	// the pre-fix tree, where the failing-first run has to happen.
	const composerBlocked = "composer_blocked"
	if b := outcomes[1]; b.err != nil && b.res.delivery != composerBlocked {
		t.Errorf("sender B failed with delivery=%q, want %q refusal or success: %v", b.res.delivery, composerBlocked, b.err)
	}
}

func submittedIntact(list []string, want string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}
