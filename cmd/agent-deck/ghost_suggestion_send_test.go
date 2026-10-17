package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Unit coverage for the Claude prompt-suggestion hazard on the `session send`
// path (the tmux-level twin is ghost_suggestion_enter_tmux_test.go). The
// composer rows are what tmux capture-pane -e returns for Claude Code
// 2.1.295's placeholder renderer.
var ghostRows = map[string]string{
	"focused":   "❯ \x1b[7mR\x1b[0;2mun the release checklist and tag the build now\x1b[0m",
	"unfocused": "❯ \x1b[2mRun the release checklist and tag the build now\x1b[0m",
	"native":    "❯ R\x1b[2mun the release checklist and tag the build now\x1b[0m",
}

func ghostPane(row string) string {
	div := strings.Repeat("─", 40)
	return "previous turn output\n" + div + "\n" + row + "\n" + div + "\n  ? for shortcuts\n"
}

func ghostSendTuning() sendExecTuning {
	tun := defaultSendTuning()
	tun.guardHold = 50 * time.Millisecond
	tun.guardPoll = 10 * time.Millisecond
	tun.retry = sendRetryOptions{maxRetries: 8, checkDelay: 0, maxFullResends: -1}
	return tun
}

// An idle Claude session showing a suggestion is an EMPTY composer: the send
// must go through (not composer_blocked), and once our message is submitted
// and the next suggestion is painted, the verify loop must not press a bare
// Enter at it.
func TestExecuteSend_SuggestionComposerIsEmptyAndNeverNudged(t *testing.T) {
	const msg = "AUTOMATED_GHOST_PROBE_MESSAGE"
	for name, row := range ghostRows {
		t.Run(name, func(t *testing.T) {
			typed := ghostPane("❯ " + msg)
			mock := &mockSendRetryTarget{
				statuses: []string{"waiting"},
				// guard capture, post-typing capture, then the verify loop
				// sees the next suggestion on an idle composer.
				panes: []string{ghostPane(row), typed, ghostPane(row)},
			}
			res, err := executeSend(mock, "claude", msg, false, ghostSendTuning())
			if res.delivery == deliveryComposerBlocked {
				t.Fatalf("a prompt suggestion blocked the send as if it were a draft: %v", err)
			}
			if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
				t.Fatalf("message must be typed exactly once, got %d", got)
			}
			if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
				t.Fatalf("bare Enter pressed %d time(s) while a prompt suggestion was shown", got)
			}
		})
	}
}

// ghostComposer models a Claude 2.1.295 composer for the send path: it shows
// a prompt suggestion while empty, paints typed text only after paintDelay
// captures (a slow, cold-start repaint), submits whatever was typed on Enter
// (keystrokes are processed in order, whatever the screen shows), and
// records any Enter that reaches it with nothing typed while the suggestion
// is shown.
type ghostComposer struct {
	paintDelay int

	typed          string
	sinceTyped     int
	submitted      []string
	ghostEnters    int
	activeLeft     int
	captureCalls   int
	keysTypedCount int
}

func (g *ghostComposer) pane() string {
	if g.typed != "" && g.sinceTyped >= g.paintDelay {
		return ghostPane("❯ " + g.typed)
	}
	return ghostPane(ghostRows["focused"])
}

func (g *ghostComposer) CapturePaneFresh() (string, error) {
	g.captureCalls++
	p := g.pane()
	g.sinceTyped++
	return p, nil
}

func (g *ghostComposer) typeKeys(keys string) {
	g.keysTypedCount++
	g.typed += keys
	g.sinceTyped = 0
}

func (g *ghostComposer) SendEnter() error {
	if g.typed == "" {
		g.ghostEnters++
		return nil
	}
	g.submitted = append(g.submitted, g.typed)
	g.typed = ""
	g.activeLeft = 2
	return nil
}

func (g *ghostComposer) SendKeysAndEnter(keys string) error {
	g.typeKeys(keys)
	return g.SendEnter()
}

func (g *ghostComposer) SendKeysAndEnterChecked(keys string, capture func() (string, error), check tmux.PostPasteCheck) error {
	g.typeKeys(keys)
	if check != nil {
		pane, capErr := capture()
		if ok, err := check(pane, capErr); !ok {
			if err == nil {
				err = errors.New("withheld")
			}
			return err
		}
	}
	return g.SendEnter()
}

func (g *ghostComposer) GetStatus() (string, error) {
	if g.activeLeft > 0 {
		g.activeLeft--
		return "active", nil
	}
	return "waiting", nil
}

func (g *ghostComposer) SendCtrlC() error               { return nil }
func (g *ghostComposer) SendKeysChunked(k string) error { g.typeKeys(k); return nil }

// A slow repaint keeps the suggestion (or the "Try ..." placeholder) painted
// for longer than the pre-Enter check waits. The send must not fail and
// strand the typed message: it defers the Enter, presses it once the
// composer shows our message, and submits exactly once, never with a bare
// Enter at the suggestion.
func TestExecuteSend_SlowRepaintAfterSuggestionSubmitsExactlyOnce(t *testing.T) {
	const msg = "AUTOMATED_GHOST_PROBE_MESSAGE"
	for _, noWait := range []bool{false, true} {
		t.Run(fmt.Sprintf("noWait=%v", noWait), func(t *testing.T) {
			g := &ghostComposer{paintDelay: 20} // > 100ms + 10 x 50ms of the pre-Enter check
			res, err := executeSend(g, "claude", msg, noWait, ghostSendTuning())
			if err != nil || res.delivery == deliverySendFailed || res.delivery == deliveryComposerBlocked {
				t.Fatalf("slow repaint failed the send: delivery=%v err=%v", res.delivery, err)
			}
			if len(g.submitted) != 1 || g.submitted[0] != msg {
				t.Fatalf("want exactly one submission of our message, got %q", g.submitted)
			}
			if g.keysTypedCount != 1 {
				t.Fatalf("message must be typed exactly once, got %d", g.keysTypedCount)
			}
			if g.ghostEnters != 0 {
				t.Fatalf("bare Enter pressed %d time(s) while only the suggestion was shown", g.ghostEnters)
			}
		})
	}
}

// The pre-Enter check still hard-stops at text that is not ours.
func TestSendInitialKeysChecked_OtherTextStillWithholdsEnter(t *testing.T) {
	const msg = "AUTOMATED_GHOST_PROBE_MESSAGE"
	mock := &mockSendRetryTarget{panes: []string{ghostPane("❯ something the operator typed")}}
	err := sendInitialKeysChecked(mock, msg, 0, true)
	if err == nil || !strings.Contains(err.Error(), "other text") {
		t.Fatalf("want Enter withheld at foreign text, got %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 0 {
		t.Fatalf("Enter pressed %d time(s) at foreign text", got)
	}
}
