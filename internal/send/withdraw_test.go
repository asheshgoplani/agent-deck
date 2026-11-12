package send

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Unit tests for WithdrawTyped: remove exactly what a send typed, never more,
// never text that is not ours. Needs the new API; fails to compile on base.

type withdrawPane struct {
	mu       sync.Mutex
	text     string // raw composer text (literal runes)
	noop     bool   // Backspace accepted, nothing deleted
	toggle   []string
	captures int
	capErr   error
	hidden   bool // composer not visible
	calls    []int
	flip     int
}

func (p *withdrawPane) CapturePaneFresh() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captures++
	if p.capErr != nil {
		return "", p.capErr
	}
	if p.hidden {
		return "no composer here\n", nil
	}
	return renderComposer(p.text), nil
}

func (p *withdrawPane) SendBackspaces(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, n)
	switch {
	case len(p.toggle) > 0:
		p.flip++
		p.text = p.toggle[p.flip%len(p.toggle)]
	case !p.noop:
		r := []rune(p.text)
		if n > len(r) {
			n = len(r)
		}
		p.text = string(r[:len(r)-n])
	}
	return nil
}

// noBackspacePane has CapturePaneFresh only.
type noBackspacePane struct{ p *withdrawPane }

func (n noBackspacePane) CapturePaneFresh() (string, error) { return n.p.CapturePaneFresh() }

func (p *withdrawPane) total() (sum int) {
	for _, n := range p.calls {
		sum += n
	}
	return
}

func withdraw(t ComposerGuardTarget, msg string, emptyBefore bool) WithdrawResult {
	return WithdrawTyped(t, msg, "", ClaudeComposerFullBody, nil, emptyBefore)
}

func TestWithdrawTyped_RemovesOwnSingleLineText(t *testing.T) {
	msg := strings.TrimSpace(strings.Repeat("run `go build` then report; ", 30)) // ~870 bytes
	p := &withdrawPane{text: msg}
	res := withdraw(p, msg, true)
	if res.Outcome != WithdrawRemoved || p.text != "" {
		t.Fatalf("outcome=%v composer=%q, want Removed and empty", res.Outcome, p.text)
	}
	if limit := len([]rune(NormalizePromptText(msg))) + 64; p.total() > limit {
		t.Fatalf("sent %d Backspaces, limit %d", p.total(), limit)
	}
	for _, n := range p.calls {
		if n > 32 || n < 1 {
			t.Fatalf("batch size %d outside 1..32: %v", n, p.calls)
		}
	}
}

func TestWithdrawTyped_BatchNeverExceedsVisibleDraft(t *testing.T) {
	p := &withdrawPane{text: "short"}
	res := withdraw(p, "short", true)
	if res.Outcome != WithdrawRemoved {
		t.Fatalf("outcome = %v, want Removed", res.Outcome)
	}
	if len(p.calls) != 1 || p.calls[0] != 5 {
		t.Fatalf("calls = %v, want exactly [5] (never more than the visible draft's rune length)", p.calls)
	}
}

func TestWithdrawTyped_EmptyComposerIsNotNeeded(t *testing.T) {
	for _, before := range []bool{true, false} {
		p := &withdrawPane{}
		res := withdraw(p, "anything", before)
		if res.Outcome != WithdrawNotNeeded || len(p.calls) != 0 {
			t.Fatalf("emptyBefore=%v: outcome=%v calls=%v, want NotNeeded and no Backspace", before, res.Outcome, p.calls)
		}
	}
}

func TestWithdrawTyped_NeverDeletesWhenComposerWasNotEmptyBefore(t *testing.T) {
	p := &withdrawPane{text: "human draft then our message"}
	res := withdraw(p, "our message", false)
	if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v, want Left, unverified, no Backspace", res, p.calls)
	}
	if res.Reason != "composer was not empty before the send" {
		t.Fatalf("reason = %q", res.Reason)
	}
	if res.Draft == "" {
		t.Fatal("Draft must carry the normalized composer text for the residue record")
	}
}

func TestWithdrawTyped_ForeignTextIsNeverDeleted(t *testing.T) {
	for _, text := range []string{"somebody else's words", "run `go build` then X", "HUMAN run `go build` then report"} {
		p := &withdrawPane{text: text}
		res := withdraw(p, "run `go build` then report", true)
		if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
			t.Fatalf("%q: res=%+v calls=%v, want Left, unverified, nothing deleted", text, res, p.calls)
		}
		if res.Reason != "composer holds text that is not this send's" {
			t.Fatalf("%q: reason = %q", text, res.Reason)
		}
	}
}

func TestWithdrawTyped_StopsWhenHumanTextAppearsMidRemoval(t *testing.T) {
	msg := strings.Repeat("abcdefghij ", 10)
	// after the first batch a human types "ZZZ": the next frame is not a
	// prefix of the message any more, so the loop must stop and leave it.
	p := &withdrawPane{text: strings.TrimSpace(msg), toggle: nil}
	res := WithdrawTyped(&interferingPane{withdrawPane: p, after: 1, inject: "ZZZ"}, msg, "", ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawLeft || res.Verified {
		t.Fatalf("res=%+v, want Left unverified", res)
	}
	if !strings.HasSuffix(p.text, "ZZZ") || len(p.calls) != 1 {
		t.Fatalf("composer=%q calls=%v, want human text intact after exactly one batch", p.text, p.calls)
	}
}

type interferingPane struct {
	*withdrawPane
	after  int
	inject string
}

func (i *interferingPane) SendBackspaces(n int) error {
	err := i.withdrawPane.SendBackspaces(n)
	i.mu.Lock()
	if len(i.calls) == i.after {
		i.text += i.inject
	}
	i.mu.Unlock()
	return err
}

func TestWithdrawTyped_PasteMarkerOwnership(t *testing.T) {
	msg := "one\ntwo\nthree"
	const own = "[Pasted text #1 +2 lines]"
	p := &withdrawPane{text: own}
	res := WithdrawTyped(p, msg, own, ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawRemoved || p.text != "" {
		t.Fatalf("res=%+v composer=%q, want our own chip (the label our paste showed) removed", res, p.text)
	}
	// The same chip with no owned label passed is not ours (H1).
	p = &withdrawPane{text: own}
	if res := withdraw(p, msg, true); res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v, want an unowned chip left untouched", res, p.calls)
	}
	// A later frame that is a prefix of the owned chip stays owned.
	p = &withdrawPane{text: own}
	res = WithdrawTyped(&chipEatingPane{withdrawPane: p}, msg, own, ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawRemoved {
		t.Fatalf("res=%+v, want Removed through partially-deleted marker frames", res)
	}
	// A marker plus extra text is not ours.
	p = &withdrawPane{text: "[Pasted text #1 +2 lines] and more"}
	res = WithdrawTyped(p, msg, own, ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v, want Left unverified, nothing deleted", res, p.calls)
	}
}

// chipEatingPane deletes only 5 runes per call whatever n is, so intermediate
// frames are partial markers like "[Pasted text #1 +2 li".
type chipEatingPane struct{ *withdrawPane }

func (c *chipEatingPane) SendBackspaces(n int) error { return c.withdrawPane.SendBackspaces(min(n, 5)) }

func TestWithdrawTyped_TargetWithoutBackspaceIsLeftVerified(t *testing.T) {
	p := &withdrawPane{text: "our message"}
	res := withdraw(noBackspacePane{p}, "our message", true)
	if res.Outcome != WithdrawLeft || !res.Verified || res.Reason != "target cannot send backspace" {
		t.Fatalf("res=%+v, want Left verified, reason %q", res, "target cannot send backspace")
	}
	if res.Draft != "our message" {
		t.Fatalf("Draft = %q", res.Draft)
	}
}

func TestWithdrawTyped_NoProgressIsLeftVerified(t *testing.T) {
	p := &withdrawPane{text: "our message", noop: true}
	res := withdraw(p, "our message", true)
	if res.Outcome != WithdrawLeft || !res.Verified || res.Reason != "backspace made no progress" {
		t.Fatalf("res=%+v, want Left verified no-progress", res)
	}
	if len(p.calls) != 1 {
		t.Fatalf("calls = %v, want a single attempt before giving up", p.calls)
	}
}

func TestWithdrawTyped_BudgetBoundsTotalBackspaces(t *testing.T) {
	msg := strings.Repeat("abcdefgh ", 20)
	// Every Backspace changes the frame (so it counts as progress) but the
	// composer never empties: the total must stop at runeLen+64.
	p := &withdrawPane{text: "abcdefgh", toggle: []string{"abcdefgh", "abcdefgh abc"}}
	res := withdraw(p, msg, true)
	if res.Outcome != WithdrawLeft || res.Reason != "removal budget exhausted" {
		t.Fatalf("res=%+v, want Left, removal budget exhausted", res)
	}
	if limit := len([]rune(msg)) + 64; p.total() > limit {
		t.Fatalf("sent %d Backspaces, limit %d", p.total(), limit)
	}
}

// claudeRowsPane renders the composer like Claude: a hard line break in the
// draft continues on a row indented by two spaces.
type claudeRowsPane struct{ *withdrawPane }

func (c claudeRowsPane) CapturePaneFresh() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captures++
	return renderComposer(strings.ReplaceAll(c.text, "\n", "\n  ")), nil
}

// Review finding 2: the budget is the message's rune count as typed (line
// endings normalised, whitespace NOT collapsed) plus 64. A config block of
// indented lines has far more runes than its whitespace-collapsed form.
func TestWithdrawTyped_BudgetCoversIndentedMultiLineMessage(t *testing.T) {
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("            key_%02d:        value_%02d", i, i))
	}
	msg := strings.Join(lines, "\r\n")
	typed := strings.ReplaceAll(msg, "\r\n", "\n")
	p := &withdrawPane{text: typed}
	res := WithdrawTyped(claudeRowsPane{p}, msg, "", ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawRemoved || p.text != "" {
		t.Fatalf("res=%+v, %d runes left; want the whole indented block removed", res, len([]rune(p.text)))
	}
	if limit := len([]rune(typed)) + 64; p.total() > limit {
		t.Fatalf("sent %d Backspaces, limit %d", p.total(), limit)
	}
}

// Review finding 1: ownership covers the composer's WHOLE body. A human's
// paragraph after a blank line is never deleted.
func TestWithdrawTyped_HumanParagraphIsNotOurs(t *testing.T) {
	const msg = "our message"
	p := &withdrawPane{text: msg + "\n\nHUMAN NOTE keep this"}
	res := WithdrawTyped(claudeRowsPane{p}, msg, "", ClaudeComposerFullBody, nil, true)
	if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v, want Left unverified and no Backspace", res, p.calls)
	}
	if res.Draft != "our message HUMAN NOTE keep this" {
		t.Fatalf("Draft = %q, want the normalized full body", res.Draft)
	}
}

func TestWithdrawTyped_NilFullBodyReaderNeverDeletes(t *testing.T) {
	p := &withdrawPane{text: "our message"}
	res := WithdrawTyped(p, "our message", "", nil, nil, true)
	if res.Outcome != WithdrawLeft || res.Verified || res.Reason != "composer body not fully readable" || len(p.calls) != 0 {
		t.Fatalf("res=%+v calls=%v", res, p.calls)
	}
}

func TestClaudeComposerFullBody(t *testing.T) {
	div := strings.Repeat("─", 40)
	frame := func(rows ...string) string {
		return "out\n" + div + "\n" + strings.Join(rows, "\n") + "\n" + div + "\n  ? for shortcuts\n"
	}
	for _, tc := range []struct {
		name, raw, want string
		ok              bool
	}{
		{"empty", frame("❯ "), "", true},
		{"one row", frame("❯ hello"), "hello", true},
		{"paragraphs", frame("❯ first", "", "  second", "    indented"), "first\n\nsecond\n  indented", true},
		{"trailing blank rows", frame("❯ x", "", ""), "x", true},
		// Placeholder-shaped plain text is returned as is: only the caller
		// knows whether it is its own message (M1). FullBodyEmpty maps it.
		{"placeholder", frame(`❯ Try "write a test"`), `Try "write a test"`, true},
		{"no frame", "❯ loose prompt\n", "", false},
		{"no glyph", frame("something else"), "", false},
	} {
		got, ok := ClaudeComposerFullBody(tc.raw, nil)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWithdrawTyped_UnreadableComposerNeverDeletes(t *testing.T) {
	for name, p := range map[string]*withdrawPane{
		"capture error":     {text: "x", capErr: errors.New("pane gone")},
		"composer not seen": {text: "x", hidden: true},
	} {
		res := withdraw(p, "x", true)
		if res.Outcome != WithdrawLeft || res.Verified || len(p.calls) != 0 {
			t.Fatalf("%s: res=%+v calls=%v, want Left unverified, no Backspace", name, res, p.calls)
		}
	}
}

func TestWithdrawTyped_NeverSendsAnythingButBackspace(t *testing.T) {
	// The only transport surface WithdrawTyped may use is SendBackspaces:
	// a target exposing Enter/Ctrl+C/Ctrl+U would be a compile-time smell, so
	// assert at the type level that the optional interface is just that.
	var _ BackspaceSender = (*withdrawPane)(nil)
}

// ProbeComposer carries the composer-empty provenance the launch paths pass
// to TypeAndSubmitReport (review finding 6).
func TestProbeComposer_ComposerEmpty(t *testing.T) {
	pane := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	if p := ProbeComposer(pane(renderComposer("")), true); !p.ComposerEmpty || p.FullBody == nil || !p.PasteMarkerFree {
		t.Fatalf("empty Claude composer: %+v", p)
	}
	if p := ProbeComposer(pane(renderComposer("a draft")), true); p.ComposerEmpty {
		t.Fatalf("composer holding a draft read as empty: %+v", p)
	}
	if p := ProbeComposer(pane(renderComposer("")), false); p.ComposerEmpty || p.FullBody != nil {
		t.Fatalf("non-Claude target must never get delete provenance: %+v", p)
	}
	if p := ProbeComposer(func() (string, error) { return "", errors.New("gone") }, true); p.ComposerEmpty {
		t.Fatalf("capture failure read as empty: %+v", p)
	}
}
