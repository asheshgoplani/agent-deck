package send

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// No-delete guards (PR 3123, second review: ~/agent-deck-work/review-3123-20261112/RESULTS.md).
//
// The review proved four ways a Backspace "withdraw" of a send's own leftover
// lost a person's text or reported a submitted message as withdrawn (so the
// queue delivered it twice). PR 3123 now only REPORTS what a failed send left
// in the input box. These tests keep the review's four scenarios as guards
// against the public send entry: a future withdraw must first pass them (and
// a design review) before it can come back.
//
// scriptedPane keeps the review's shape: the input box is a plain string,
// and a person's event lands at a scripted moment: right after our
// pre-Enter check refused Enter, or around the first delete key a withdraw
// would send (the review's beforeKeys/afterKeys). With no delete key ever
// sent, the event lands when the test settles the pane after the send
// returns, so the expected box is the same either way. The pane implements
// every key that could delete (Backspace, Ctrl-U, Ctrl-C) and records each
// call: any call fails the guard. Like Claude, one Backspace removes a whole
// paste chip or image placeholder.

type eventMoment int

const (
	atRefusal      eventMoment = iota // right after the pre-Enter refusal
	beforeFirstKey                    // between a sizing capture and the first delete key
	afterFirstKey                     // right after the first delete key landed
)

type scriptedPane struct {
	mu    sync.Mutex
	text  string
	when  eventMoment
	event func(p *scriptedPane)
	fired bool

	enters     int
	submitted  []string
	backspaces []int
	ctrlU      int
	ctrlC      int
}

// noDeleteScrollback is a truncated paste marker in the pane above the
// input box: PasteTruncationCheck reads it and withholds Enter, while the
// input box itself holds our typed text.
const noDeleteScrollback = "[Pasted text #1 +1 lines]\n"

func (p *scriptedPane) CapturePaneFresh() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return noDeleteScrollback + renderComposer(p.text), nil
}

// fire runs the person's event once. Callers hold p.mu.
func (p *scriptedPane) fire() {
	if !p.fired && p.event != nil {
		p.fired = true
		p.event(p)
	}
}

// settle lands the person's event if no scripted moment came.
func (p *scriptedPane) settle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fire()
}

func (p *scriptedPane) SendEnter() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters++
	return nil
}

func (p *scriptedPane) SendKeysAndEnter(body string) error {
	p.mu.Lock()
	p.text += body
	p.mu.Unlock()
	return p.SendEnter()
}

func (p *scriptedPane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	p.mu.Lock()
	p.text += body
	p.mu.Unlock()
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		p.mu.Lock()
		if p.when == atRefusal {
			p.fire()
		}
		p.mu.Unlock()
		return err
	}
	return p.SendEnter()
}

var chipTailRe = regexp.MustCompile(`(\[Pasted (text|Content) [^\]]*\]|\[Image #\d+\])$`)

// The delete keys a withdraw would use. None may ever be called.
func (p *scriptedPane) SendBackspaces(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backspaces = append(p.backspaces, n)
	if p.when == beforeFirstKey {
		p.fire()
	}
	for range n {
		if loc := chipTailRe.FindStringIndex(p.text); loc != nil {
			p.text = p.text[:loc[0]]
			continue
		}
		if r := []rune(p.text); len(r) > 0 {
			p.text = string(r[:len(r)-1])
		}
	}
	if p.when == afterFirstKey {
		p.fire()
	}
	return nil
}

func (p *scriptedPane) SendCtrlU() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctrlU++
	p.fire()
	p.text = ""
	return nil
}

func (p *scriptedPane) SendCtrlC() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ctrlC++
	p.fire()
	p.text = ""
	return nil
}

// noDeleteMessage has 2 hard line breaks (75 runes, more than one 32-key
// batch); the pane's marker declares 1, so Enter is withheld.
const noDeleteMessage = "Please review PR 3123 now\nthen rerun the flaky integration shard\nand report"

// sendRefused drives the public entry with a pre-Enter refusal (truncated
// paste) on an input box that was empty before the send, then settles the
// pane.
func sendRefused(t *testing.T, p *scriptedPane) ComposerObservation {
	t.Helper()
	_, err := TypeAndSubmitReport(p, noDeleteMessage, ExpectedPasteMarkerLineBreaks(noDeleteMessage), false, true, ClaudeComposerFullBody)
	p.settle()
	var ns *NotSubmittedError
	if err == nil || !errors.As(err, &ns) || !errors.Is(err, ErrPasteTruncated) {
		t.Fatalf("err = %v, want a *NotSubmittedError for a truncated paste (pre-Enter refusal)", err)
	}
	return ns.Observed
}

// requireNothingDeleted: no delete key, no Enter of ours (the pre-PR code
// pressed none on a pre-Enter refusal), the input box byte-identical to
// want, and no report that claims anything was removed.
func requireNothingDeleted(t *testing.T, p *scriptedPane, o ComposerObservation, want string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.backspaces) != 0 || p.ctrlU != 0 || p.ctrlC != 0 {
		t.Errorf("delete keys sent into another session's input box: backspaces=%v ctrl-u=%d ctrl-c=%d", p.backspaces, p.ctrlU, p.ctrlC)
	}
	if p.enters != 0 {
		t.Errorf("pressed Enter %d times after a pre-Enter refusal, want 0", p.enters)
	}
	if p.text != want {
		t.Errorf("input box = %q, want byte-identical %q", p.text, want)
	}
	if d := strings.ToLower(o.Describe()); strings.Contains(d, "removed") || strings.Contains(d, "withdrawn") {
		t.Errorf("report %q claims the text was removed", o.Describe())
	}
}

// Review finding 1: an Enter (a person's, or a late nudge) submits what is
// in the box after the first delete batch. The report must never say
// "removed" (a queue would retry it: double delivery).
func TestNoDelete_EnterMidCleanupNeverReportsRemoved(t *testing.T) {
	p := &scriptedPane{when: afterFirstKey, event: func(p *scriptedPane) {
		p.submitted = append(p.submitted, p.text)
		p.text = ""
	}}
	o := sendRefused(t, p)
	requireNothingDeleted(t, p, o, "")
	if len(p.submitted) != 1 || p.submitted[0] != noDeleteMessage {
		t.Fatalf("submitted = %q, want the person's own submit of the whole message", p.submitted)
	}
}

// Review finding 3: whitespace a person types after our text (Shift+Enter)
// is invisible to the reader. It must survive.
func TestNoDelete_HumanTrailingNewlinesSurvive(t *testing.T) {
	p := &scriptedPane{when: atRefusal, event: func(p *scriptedPane) { p.text += "\n\n" }}
	o := sendRefused(t, p)
	requireNothingDeleted(t, p, o, noDeleteMessage+"\n\n")
	if o.State != ComposerLeft || !strings.Contains(o.Describe(), StillTypedNotice) {
		t.Fatalf("observation = %+v, want left with the still-typed notice", o)
	}
}

// Review finding 3: after the box emptied, a person types the first letters
// of their own message, which match ours. They must survive.
func TestNoDelete_HumanPrefixAfterEmptySurvives(t *testing.T) {
	p := &scriptedPane{when: afterFirstKey, event: func(p *scriptedPane) { p.text = "Ple" }}
	o := sendRefused(t, p)
	requireNothingDeleted(t, p, o, "Ple")
}

// Review finding 3 (TOCTOU): keys a person types between a check and a
// delete batch are never eaten.
func TestNoDelete_HumanKeysInWindowSurvive(t *testing.T) {
	p := &scriptedPane{when: beforeFirstKey, event: func(p *scriptedPane) { p.text += "abc" }}
	o := sendRefused(t, p)
	requireNothingDeleted(t, p, o, noDeleteMessage+"abc")
}

// Review finding 2: a person's paste chip landing in the window is one key
// for Backspace and any number of characters. It must survive.
func TestNoDelete_HumanPasteChipSurvives(t *testing.T) {
	const human = "[Pasted text #2 +40 lines]"
	p := &scriptedPane{when: beforeFirstKey, event: func(p *scriptedPane) { p.text += human }}
	o := sendRefused(t, p)
	requireNothingDeleted(t, p, o, noDeleteMessage+human)
}

// noDeleteTokens are the transport calls that delete text in an input box.
// No send-path source may contain them.
var noDeleteTokens = []string{"BSpace", "SendBackspaces", `"C-u"`, "SendCtrlU", `"C-h"`, `"C-w"`, `\x7f`, `\x08`}

// TestNoDelete_SendPathSourcesHaveNoDeleteKeys is a source guard: no
// non-test .go file in internal/send/, cmd/agent-deck/session_send*.go or the
// launch paths (cmd/agent-deck/launch_verify_prompt.go,
// internal/session/instance_start.go) may name a delete key. `session send-keys` (session_send_keys_cmd.go) is
// exempt: it types exactly the keys a person asks for.
func TestNoDelete_SendPathSourcesHaveNoDeleteKeys(t *testing.T) {
	var files []string
	sendFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, sendFiles...)
	cmdFiles, err := filepath.Glob(filepath.Join("..", "..", "cmd", "agent-deck", "session_send*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cmdFiles) == 0 {
		t.Fatal("found no cmd/agent-deck/session_send*.go files: the guard would pass vacuously")
	}
	files = append(files, cmdFiles...)
	// The launch paths report a refused prompt through the same call; a
	// withdraw added there would reach a target's input box too.
	files = append(files,
		filepath.Join("..", "..", "cmd", "agent-deck", "launch_verify_prompt.go"),
		filepath.Join("..", "session", "instance_start.go"))
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || filepath.Base(f) == "session_send_keys_cmd.go" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, tok := range noDeleteTokens {
			if strings.Contains(string(data), tok) {
				t.Errorf("%s contains %s: agent-deck must not delete anything in another session's input box. "+
					"A future withdraw must first pass the TestNoDelete_* guards and a design review "+
					"(PR 3123 second review, ~/agent-deck-work/review-3123-20261112/RESULTS.md)", f, tok)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("checked only %d source files", checked)
	}
}
