package main

import (
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// Issue #2077 (a re-report of #1793, after the #1831 fix): on an idle Claude
// target, `session send` (with and without --no-wait) typed the message into
// the composer, the submitting Enter never landed, and the CLI still reported
// a send. Timer-driven callers then sent again, and each new body was typed
// onto the stranded one, so the composer filled with coalesced messages until
// a human pressed Enter.
//
// The existing #1793 tests pin the verifier (sendWithRetryTarget) in
// isolation. These drive the whole executeSend pipeline, guard included,
// against one stateful pane, in both send modes, and pin the two halves of
// the report:
//
//   - a send whose Enter is swallowed exits with an error and
//     typed_not_submitted, typing the body exactly once;
//   - the next send into that stranded composer is refused before it types
//     anything, so messages can never coalesce.
// ---------------------------------------------------------------------------

// strandingClaudePane is an idle Claude pane that accepts typed input but
// swallows every Enter, the #2077 shape. Captures show what the composer
// holds.
type strandingClaudePane struct {
	mu       sync.Mutex
	composer string
	typed    int
	enters   int
	ctrlC    int
}

func (p *strandingClaudePane) typeBody(body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.typed++
	p.composer += body
}

func (p *strandingClaudePane) SendKeysAndEnter(body string) error {
	p.typeBody(body)
	return p.SendEnter()
}

func (p *strandingClaudePane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	p.typeBody(body)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		return err
	}
	return p.SendEnter()
}

func (p *strandingClaudePane) SendKeysChunked(body string) error {
	p.typeBody(body)
	return nil
}

// SendEnter is counted but never submits: the Enter does not land.
func (p *strandingClaudePane) SendEnter() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters++
	return nil
}

func (p *strandingClaudePane) SendCtrlC() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctrlC++
	return nil
}

func (p *strandingClaudePane) GetStatus() (string, error) { return "waiting", nil }

func (p *strandingClaudePane) CapturePaneFresh() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return claudeComposer(p.composer), nil
}

func (p *strandingClaudePane) snapshot() (composer string, typed, enters, ctrlC int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.composer, p.typed, p.enters, p.ctrlC
}

// issue2077Tuning shrinks the real per-mode budgets so the test is fast while
// keeping each mode's retry shape (no-wait vs default).
func issue2077Tuning(noWait bool) sendExecTuning {
	retry := defaultSendOptions()
	if noWait {
		retry = noWaitSendOptions()
	}
	retry.checkDelay = 0
	return testGuardTuning(retry)
}

func issue2077Modes() []struct {
	name   string
	noWait bool
} {
	return []struct {
		name   string
		noWait bool
	}{{"default", false}, {"no-wait", true}}
}

const issue2077Message = "[lane timer] wake: check the inbox and report"

func TestIssue2077_SwallowedEnterOnIdleClaudeIsReportedNotSubmitted(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &strandingClaudePane{}
			res, err := executeSend(pane, "claude", issue2077Message, mode.noWait, issue2077Tuning(mode.noWait))
			if err == nil {
				t.Fatalf("issue #2077: a message stranded in the composer must not report a send (delivery %q)", res.delivery)
			}
			if res.delivery != deliveryTypedNotSubmitted {
				t.Fatalf("delivery = %q, want %q", res.delivery, deliveryTypedNotSubmitted)
			}
			fields := res.jsonFields()
			if fields["submitted"] != false || fields["confirmation"] != "failed" {
				t.Fatalf("--json fields = %v, want submitted=false confirmation=failed", fields)
			}
			composer, typed, _, ctrlC := pane.snapshot()
			if typed != 1 {
				t.Fatalf("body typed %d times, want exactly 1 (no full resend onto the stranded copy)", typed)
			}
			if composer != issue2077Message {
				t.Fatalf("composer = %q, want exactly one copy of the message", composer)
			}
			if ctrlC != 0 {
				t.Fatalf("SendCtrlC called %d times on a Claude pane, want 0", ctrlC)
			}
		})
	}
}

func TestIssue2077_RepeatSendIntoStrandedComposerIsRefusedNotCoalesced(t *testing.T) {
	for _, mode := range issue2077Modes() {
		for _, next := range []struct{ name, msg string }{
			{"same message", issue2077Message},
			{"different message", "[inbox] a new transition notification is waiting"},
		} {
			t.Run(mode.name+"/"+next.name, func(t *testing.T) {
				pane := &strandingClaudePane{}
				tun := issue2077Tuning(mode.noWait)
				if _, err := executeSend(pane, "claude", issue2077Message, mode.noWait, tun); err == nil {
					t.Fatal("setup: the first send should have been reported as stranded")
				}
				_, _, entersBefore, _ := pane.snapshot()

				res, err := executeSend(pane, "claude", next.msg, mode.noWait, tun)
				if err == nil {
					t.Fatalf("issue #2077: a send into a composer holding a stranded message must be refused (delivery %q)", res.delivery)
				}
				if res.delivery != deliveryComposerBlocked {
					t.Fatalf("delivery = %q, want %q", res.delivery, deliveryComposerBlocked)
				}
				if res.jsonFields()["submitted"] != false {
					t.Fatalf("--json submitted = %v, want false", res.jsonFields()["submitted"])
				}
				composer, typed, enters, ctrlC := pane.snapshot()
				if typed != 1 || strings.Count(composer, issue2077Message) != 1 || composer != issue2077Message {
					t.Fatalf("issue #2077: messages coalesced in the composer: typed=%d composer=%q", typed, composer)
				}
				if enters != entersBefore {
					t.Fatalf("refused send pressed Enter %d times on the stranded composer", enters-entersBefore)
				}
				if ctrlC != 0 {
					t.Fatalf("SendCtrlC called %d times on a Claude pane, want 0", ctrlC)
				}
			})
		}
	}
}
