package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// A failed `session send` must not orphan its own typed text in the target's
// composer (incident of 11-12 Nov: the pre-Enter check withheld Enter, the CLI
// said "not sent", and the ~1 kB payload stayed typed for 19 hours while every
// later send was refused as "composer is occupied or unreadable").
//
// These tests drive the whole executeSend pipeline against a stateful fake
// Claude composer, in both send modes. They use delivery string literals,
// jsonFields() keys and error substrings only, so this file compiles on the
// base commit and the fail-first check is behavioural. Tests that need new
// API (a residue store) live in send_residue_selfheal_test.go.
// ---------------------------------------------------------------------------

type orphanSeg struct {
	lit    string
	paste  bool
	breaks int
}

// orphanPane is an idle Claude pane. A multi-line body collapses behind a
// "[Pasted text #1 +N lines]" chip (one atomic unit for Backspace), exactly
// like Claude's composer; anything else is plain text. It has no
// SendBackspaces method: orphanPaneBS adds it.
type orphanPane struct {
	mu            sync.Mutex
	segs          []orphanSeg
	suggestion    bool   // an empty composer paints a ghost prompt suggestion
	submitOnEnter bool   // Enter submits the composer (clean target)
	truncateBy    int    // pasted chip declares this many fewer breaks than sent
	humanBefore   string // a human typed this in the guard->type window (our body lands after it)
	replaceWith   string // the composer shows only this after our body is sent (repaint/remount swapped the content)
	humanAfter    string // a human typed this right after our body landed
	typed         int
	enters        int
	submitted     []string
	activeLeft    int
	bsCalls       []int
}

func (p *orphanPane) text() string {
	var b strings.Builder
	for _, s := range p.segs {
		if s.paste {
			fmt.Fprintf(&b, "[Pasted text #1 +%d lines]", s.breaks)
		} else {
			b.WriteString(s.lit)
		}
	}
	return b.String()
}

func (p *orphanPane) typeBody(body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.typed++
	if p.replaceWith != "" {
		p.segs = append(p.segs, orphanSeg{lit: p.replaceWith})
		return
	}
	if p.humanBefore != "" {
		p.segs = append(p.segs, orphanSeg{lit: p.humanBefore})
		p.humanBefore = ""
	}
	if n := strings.Count(body, "\n"); n > 0 {
		p.segs = append(p.segs, orphanSeg{lit: body, paste: true, breaks: n - p.truncateBy})
	} else {
		p.segs = append(p.segs, orphanSeg{lit: body})
	}
	if p.humanAfter != "" {
		p.segs = append(p.segs, orphanSeg{lit: p.humanAfter})
		p.humanAfter = ""
	}
}

func (p *orphanPane) backspace(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < n && len(p.segs) > 0; i++ {
		last := &p.segs[len(p.segs)-1]
		if last.paste {
			p.segs = p.segs[:len(p.segs)-1]
			continue
		}
		r := []rune(last.lit)
		last.lit = string(r[:len(r)-1])
		if last.lit == "" {
			p.segs = p.segs[:len(p.segs)-1]
		}
	}
}

func (p *orphanPane) SendKeysAndEnter(body string) error {
	p.typeBody(body)
	return p.SendEnter()
}

func (p *orphanPane) SendKeysAndEnterChecked(body string, capture func() (string, error), check tmux.PostPasteCheck) error {
	p.typeBody(body)
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		if err == nil {
			err = errors.New("withheld")
		}
		return err
	}
	return p.SendEnter()
}

func (p *orphanPane) SendKeysChunked(body string) error { p.typeBody(body); return nil }

func (p *orphanPane) SendEnter() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enters++
	if p.submitOnEnter && len(p.segs) > 0 {
		var b strings.Builder
		for _, s := range p.segs {
			b.WriteString(s.lit)
		}
		p.submitted = append(p.submitted, b.String())
		p.segs = nil
		p.activeLeft = 2
	}
	return nil
}

func (p *orphanPane) SendCtrlC() error { return nil }

func (p *orphanPane) GetStatus() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeLeft > 0 {
		p.activeLeft--
		return "active", nil
	}
	return "waiting", nil
}

func (p *orphanPane) CapturePaneFresh() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.segs) == 0 && p.suggestion {
		return ghostPane(ghostRows["focused"]), nil
	}
	return claudeComposer(p.text()), nil
}

func (p *orphanPane) composer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text()
}

func (p *orphanPane) counts() (typed, enters, backspaceCalls int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.typed, p.enters, len(p.bsCalls)
}

func (p *orphanPane) configure(f func(*orphanPane)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// orphanPaneBS is an orphanPane whose transport can send Backspace.
// noop: the keys are accepted but delete nothing (a composer that ignores
// them). fail: the send-keys call itself errors.
type orphanPaneBS struct {
	*orphanPane
	noop bool
	fail bool
}

func (p *orphanPaneBS) SendBackspaces(n int) error {
	p.mu.Lock()
	p.bsCalls = append(p.bsCalls, n)
	p.mu.Unlock()
	if p.fail {
		return errors.New("tmux send-keys failed")
	}
	if !p.noop {
		p.backspace(n)
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

const orphanSingleLine = "[lane timer] wake: check the inbox and report"

// orphanMultiLine is a short multi-line body: 3 hard breaks, so Claude
// collapses it behind a paste chip and expectedPasteBreaks is 3.
const orphanMultiLine = "line one `a`\nline two `b`\nline three `c`\nline four `d`"

// orphanLongMultiLine is the incident's shape: ~1 kB, multi-line, backticks.
func orphanLongMultiLine() string {
	var lines []string
	for i := 1; i <= 16; i++ {
		lines = append(lines, fmt.Sprintf("step %02d: run `go build ./...` then check `git status` and report", i))
	}
	return strings.Join(lines, "\n")
}

// orphanLongSingleLine is ~1 kB on one line (no paste chip), with backticks.
func orphanLongSingleLine() string {
	return strings.TrimSpace(strings.Repeat("run `go build ./...` then check `git status` and report back; ", 16))
}

func orphanSend(t *testing.T, target sendRetryTarget, msg string, noWait bool) (sendDeliveryResult, error) {
	t.Helper()
	return executeSend(target, "claude", msg, noWait, issue2077Tuning(noWait))
}

func requireOrphanResidue(t *testing.T, res sendDeliveryResult, want string) map[string]interface{} {
	t.Helper()
	fields := res.jsonFields()
	if got := fields["composer_residue"]; got != want {
		t.Fatalf("jsonFields()[composer_residue] = %v, want %q (fields=%v)", got, want, fields)
	}
	return fields
}

// --- 1. clean send: no regression ------------------------------------------

func TestOrphan_CleanSendNeverSendsBackspace(t *testing.T) {
	for _, mode := range issue2077Modes() {
		for name, msg := range map[string]string{"single-line": orphanSingleLine, "multi-line": orphanMultiLine} {
			t.Run(mode.name+"/"+name, func(t *testing.T) {
				pane := &orphanPaneBS{orphanPane: &orphanPane{submitOnEnter: true, suggestion: true}}
				res, err := orphanSend(t, pane, msg, mode.noWait)
				if err != nil {
					t.Fatalf("clean send failed: delivery=%q err=%v", res.delivery, err)
				}
				typed, _, bs := pane.counts()
				if typed != 1 || bs != 0 {
					t.Fatalf("typed=%d backspace calls=%d, want 1 and 0", typed, bs)
				}
				if len(pane.submitted) != 1 || pane.submitted[0] != msg {
					t.Fatalf("submitted = %q, want exactly the message", pane.submitted)
				}
				if v, ok := res.jsonFields()["composer_residue"]; ok && v != "" {
					t.Fatalf("clean send reported composer_residue=%v", v)
				}
			})
		}
	}
}

// --- 2. foreign draft before typing ------------------------------------------

func TestOrphan_ForeignDraftBlocksBeforeTypingAndIsNeverTouched(t *testing.T) {
	const human = "half-typed thought from a human"
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &orphanPaneBS{orphanPane: &orphanPane{segs: []orphanSeg{{lit: human}}, submitOnEnter: true}}
			res, err := orphanSend(t, pane, orphanSingleLine, mode.noWait)
			if err == nil || res.delivery != "composer_blocked" {
				t.Fatalf("delivery=%q err=%v, want composer_blocked with an error", res.delivery, err)
			}
			if !strings.Contains(err.Error(), "existing draft preserved") {
				t.Fatalf("error %q must say the existing draft was preserved", err)
			}
			typed, enters, bs := pane.counts()
			if typed != 0 || enters != 0 || bs != 0 {
				t.Fatalf("typed=%d enters=%d backspaces=%d, want all 0 (nothing typed, nothing deleted)", typed, enters, bs)
			}
			if got := pane.composer(); got != human {
				t.Fatalf("composer = %q, want the human draft untouched", got)
			}
		})
	}
}

// The report must say which of the three cases this is: here, "nothing typed".
func TestOrphan_ForeignDraftReportsNoResidue(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &orphanPaneBS{orphanPane: &orphanPane{segs: []orphanSeg{{lit: "a human draft"}}}}
			res, err := orphanSend(t, pane, orphanSingleLine, mode.noWait)
			if err == nil {
				t.Fatal("want a refusal")
			}
			requireOrphanResidue(t, res, "none")
			if !strings.Contains(err.Error(), "nothing was typed") {
				t.Fatalf("error %q should say nothing was typed", err)
			}
		})
	}
}

// --- 3. composer changes underneath: never delete, say so ---------------------

func TestOrphan_ComposerChangedUnderUsIsNeverDeleted(t *testing.T) {
	cases := []struct {
		name  string
		msg   string
		setup func(p *orphanPane)
		human string // text that must survive
	}{
		// A ghost suggestion was shown pre-send and after typing the composer
		// holds other text that does not contain our message: the typed-composer
		// check refuses Enter ("composer holds other text") -- the incident's
		// error. (Human text merely PREFIXED to our body still contains the
		// message and passes the base check; that is a separate, pre-existing
		// window and is not asserted here.)
		{"composer holds other text (incident)", orphanSingleLine, func(p *orphanPane) { p.suggestion = true; p.replaceWith = "HUMAN WIP: draft the release notes" }, "HUMAN WIP: draft the release notes"},
		// The human typed right after our paste landed; the chip is truncated.
		{"human text after truncated paste", orphanMultiLine, func(p *orphanPane) { p.truncateBy = 1; p.humanAfter = " and also tell them HUMAN" }, "and also tell them HUMAN"},
	}
	for _, mode := range issue2077Modes() {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				pane := &orphanPaneBS{orphanPane: &orphanPane{}}
				pane.configure(tc.setup)
				res, err := orphanSend(t, pane, tc.msg, mode.noWait)
				if err == nil {
					t.Fatalf("want an error, delivery=%q", res.delivery)
				}
				typed, enters, bs := pane.counts()
				if bs != 0 {
					t.Fatalf("sent %d Backspace call(s) into a composer holding text that is not ours: %v", bs, pane.bsCalls)
				}
				if enters != 0 {
					t.Fatalf("pressed Enter %d times on a composer holding foreign text", enters)
				}
				if typed != 1 {
					t.Fatalf("typed %d times, want 1", typed)
				}
				if got := pane.composer(); !strings.Contains(got, strings.TrimSpace(tc.human)) {
					t.Fatalf("human text %q lost; composer = %q", tc.human, got)
				}
				if res.delivery != "typed_not_submitted" {
					t.Fatalf("delivery = %q, want typed_not_submitted (our text is still in the composer); err=%v; composer=%q", res.delivery, err, pane.composer())
				}
				fields := requireOrphanResidue(t, res, "left")
				if fields["composer_residue_verified"] != false {
					t.Fatalf("composer_residue_verified = %v, want false (composer mixed with other text)", fields["composer_residue_verified"])
				}
				if !strings.Contains(err.Error(), "left in the composer") || !strings.Contains(err.Error(), "mixed with other text") {
					t.Fatalf("error %q must say the text is left in the composer and may be mixed with other text", err)
				}
			})
		}
	}
}

// --- 4. verify fails but the composer holds only ours: withdraw ---------------

func TestOrphan_TruncatedPasteIsWithdrawn(t *testing.T) {
	for _, mode := range issue2077Modes() {
		for name, msg := range map[string]string{"short multi-line": orphanMultiLine, "long multi-line with backticks (~1kB)": orphanLongMultiLine()} {
			t.Run(mode.name+"/"+name, func(t *testing.T) {
				pane := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}}
				res, err := orphanSend(t, pane, msg, mode.noWait)
				if err == nil {
					t.Fatalf("want an error, delivery=%q", res.delivery)
				}
				if res.delivery != "typed_withdrawn" {
					t.Fatalf("delivery = %q, want typed_withdrawn; err=%v; composer still holds %q", res.delivery, err, pane.composer())
				}
				requireOrphanResidue(t, res, "removed")
				if got := pane.composer(); got != "" {
					t.Fatalf("composer = %q, want empty (our text removed)", got)
				}
				typed, enters, bs := pane.counts()
				if typed != 1 || enters != 0 || bs == 0 {
					t.Fatalf("typed=%d enters=%d backspace calls=%d, want 1, 0, >0", typed, enters, bs)
				}
				if !strings.Contains(err.Error(), "message not sent") || !strings.Contains(err.Error(), "removed from the composer") {
					t.Fatalf("error %q must say not sent and that the typed text was removed", err)
				}
				if res.jsonFields()["submitted"] != false {
					t.Fatalf("submitted = %v, want false", res.jsonFields()["submitted"])
				}
			})
		}
	}
}

// A swallowed Enter: the composer verifiably holds our message at the end of
// the verify budget, so it is withdrawn too (the issue #2077 shape, now
// cleaned up). ~1 kB single line with backticks exercises many batches.
func TestOrphan_SwallowedEnterIsWithdrawnAtEndOfBudget(t *testing.T) {
	for _, mode := range issue2077Modes() {
		for name, msg := range map[string]string{"short": orphanSingleLine, "long single-line with backticks (~1kB)": orphanLongSingleLine()} {
			t.Run(mode.name+"/"+name, func(t *testing.T) {
				pane := &orphanPaneBS{orphanPane: &orphanPane{}}
				res, err := orphanSend(t, pane, msg, mode.noWait)
				if err == nil {
					t.Fatalf("want an error, delivery=%q", res.delivery)
				}
				if res.delivery != "typed_withdrawn" {
					t.Fatalf("delivery = %q, want typed_withdrawn; err=%v; composer still holds %d bytes", res.delivery, err, len(pane.composer()))
				}
				requireOrphanResidue(t, res, "removed")
				if got := pane.composer(); got != "" {
					t.Fatalf("composer = %q, want empty", got)
				}
				typed, _, _ := pane.counts()
				if typed != 1 {
					t.Fatalf("typed %d times, want 1", typed)
				}
				// Never more Backspace than the visible text could need in total
				// plus the fixed slack.
				total := 0
				for _, n := range pane.bsCalls {
					total += n
				}
				if limit := len([]rune(msg)) + 64; total > limit {
					t.Fatalf("sent %d Backspaces for a %d-rune message (limit %d)", total, len([]rune(msg)), limit)
				}
			})
		}
	}
}

// --- 5. cleanup impossible: our text is left, and the report says so ----------

func TestOrphan_CleanupImpossibleIsReportedAsLeft(t *testing.T) {
	type mk func() (sendRetryTarget, *orphanPane)
	targets := []struct {
		name string
		mk   mk
	}{
		{"target cannot send backspace", func() (sendRetryTarget, *orphanPane) { p := &orphanPane{truncateBy: 1}; return p, p }},
		{"backspace is a no-op", func() (sendRetryTarget, *orphanPane) {
			p := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}, noop: true}
			return p, p.orphanPane
		}},
		{"backspace send-keys fails", func() (sendRetryTarget, *orphanPane) {
			p := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}, fail: true}
			return p, p.orphanPane
		}},
	}
	for _, mode := range issue2077Modes() {
		for _, tg := range targets {
			t.Run(mode.name+"/"+tg.name, func(t *testing.T) {
				target, pane := tg.mk()
				res, err := orphanSend(t, target, orphanMultiLine, mode.noWait)
				if err == nil {
					t.Fatalf("want an error, delivery=%q", res.delivery)
				}
				if res.delivery != "typed_not_submitted" {
					t.Fatalf("delivery = %q, want typed_not_submitted; err=%v", res.delivery, err)
				}
				fields := requireOrphanResidue(t, res, "left")
				if fields["composer_residue_verified"] != true {
					t.Fatalf("composer_residue_verified = %v, want true (the composer holds only our chip)", fields["composer_residue_verified"])
				}
				for _, want := range []string{"message not sent and left in the composer", "a human must submit or clear it"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q lacks %q", err, want)
					}
				}
				if _, enters, _ := pane.counts(); enters != 0 {
					t.Fatalf("pressed Enter %d times", enters)
				}
				if pane.composer() == "" {
					t.Fatal("setup: the composer should still hold our chip")
				}
			})
		}
	}
	// The end-of-budget path (swallowed Enter) with no way to clean up.
	for _, mode := range issue2077Modes() {
		t.Run(mode.name+"/swallowed Enter, target cannot send backspace", func(t *testing.T) {
			pane := &orphanPane{}
			res, err := orphanSend(t, pane, orphanSingleLine, mode.noWait)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			fields := requireOrphanResidue(t, res, "left")
			if fields["composer_residue_verified"] != true {
				t.Fatalf("composer_residue_verified = %v, want true", fields["composer_residue_verified"])
			}
			if got := pane.composer(); got != orphanSingleLine {
				t.Fatalf("composer = %q, want our message left as typed", got)
			}
		})
	}
}

// --- 6. follow-up sends (no residue store wired) -----------------------------

func TestOrphan_FollowUpSendAfterWithdrawDeliversNormally(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}}
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if err == nil || res.delivery != "typed_withdrawn" {
				t.Fatalf("setup: first send delivery=%q err=%v, want typed_withdrawn; composer=%q", res.delivery, err, pane.composer())
			}
			pane.configure(func(p *orphanPane) { p.truncateBy = 0; p.submitOnEnter = true })
			const next = "[inbox] a new transition notification is waiting"
			res2, err := orphanSend(t, pane, next, mode.noWait)
			if err != nil {
				t.Fatalf("follow-up send refused: delivery=%q err=%v", res2.delivery, err)
			}
			if len(pane.submitted) != 1 || pane.submitted[0] != next {
				t.Fatalf("submitted = %q, want exactly the follow-up message", pane.submitted)
			}
		})
	}
}

// Without a residue record the left text is indistinguishable from a human's
// draft: the follow-up send is refused and nothing is deleted or submitted.
func TestOrphan_FollowUpAfterLeftWithoutRecordIsRefusedAndNothingDeleted(t *testing.T) {
	for _, mode := range issue2077Modes() {
		t.Run(mode.name, func(t *testing.T) {
			pane := &orphanPaneBS{orphanPane: &orphanPane{truncateBy: 1}, noop: true}
			res, err := orphanSend(t, pane, orphanMultiLine, mode.noWait)
			if err == nil || res.delivery != "typed_not_submitted" {
				t.Fatalf("setup: first send delivery=%q err=%v, want typed_not_submitted", res.delivery, err)
			}
			before := pane.composer()
			_, entersBefore, bsBefore := pane.counts()
			res2, err := orphanSend(t, pane, "[inbox] next", mode.noWait)
			if err == nil || res2.delivery != "composer_blocked" {
				t.Fatalf("follow-up delivery=%q err=%v, want composer_blocked", res2.delivery, err)
			}
			typed, enters, bs := pane.counts()
			if typed != 1 || enters != entersBefore || bs != bsBefore {
				t.Fatalf("follow-up touched the pane: typed=%d enters %d->%d backspaces %d->%d", typed, entersBefore, enters, bsBefore, bs)
			}
			if got := pane.composer(); got != before {
				t.Fatalf("composer changed from %q to %q", before, got)
			}
		})
	}
}
