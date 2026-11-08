package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// Issue #3085: a guarded queued follow-up (--queue --require-input-prompt) to
// a finished Codex 0.167.0 session was reported refused with composer_blocked
// "input prompt unavailable; no keys typed" at an empty idle composer.
//
// The frames under testdata/issue3085 are real `tmux capture-pane -p -e`
// captures of Codex CLI 0.167.0 (paths replaced): the reporter's footers
// "GPT-6.1-Sol low Fast" and "GPT-6.1 Terra low Fast", the
// "← for agents · ? for shortcuts" hint with the right-aligned
// "⚠ N warnings · f2 to view", at pane widths 60 to 300. The unmodified
// guard accepts every idle frame; these cases pin that.
//
// Building the approval-menu case from a real capture showed the guard did
// not see Codex 0.167's approval picker as a menu: its footer reads
// "Press enter to confirm or esc to cancel", and the "› 1. Yes, proceed (y)"
// row passed the "› " prompt check. Before the first batch only the draft
// guard caught it (reported composer_blocked, not menu_open), and between a
// paste and its Enter nothing did. With the menu recognised, a refusal
// before typing reports menu_open, and the Codex submission marker must
// treat it as the definitive non-delivery it is: a marker left
// "transport_ambiguous" refuses every later send to the session.
// ---------------------------------------------------------------------------

func issue3085Frame(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "issue3085", name+".txt"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// issue3085ReportedPane is the pane text from the issue body (sanitized by
// the reporter), with the right-aligned warning.
func issue3085ReportedPane(model string) string {
	return strings.Join([]string{
		"  Worked for 2m 3s ...",
		"",
		"\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m",
		"",
		"  " + model + " · <project> · <task> · GPT-6.1-Sol · low · <project> · <host> · <branch> ...",
		"  ← for agents · ? for shortcuts" + strings.Repeat(" ", 140) + "⚠ 1 warning · f2 to view",
		"",
	}, "\n")
}

func issue3085IdleFrames(t *testing.T) map[string]string {
	frames := map[string]string{
		"reported Sol":   issue3085ReportedPane("GPT-6.1-Sol low Fast"),
		"reported Terra": issue3085ReportedPane("GPT-6.1 Terra low Fast"),
	}
	for _, name := range []string{"sol-fresh", "sol-idle", "sol-idle-w60", "sol-idle-w80", "sol-idle-w120", "sol-idle-w300", "terra-idle"} {
		frames[name] = issue3085Frame(t, name)
	}
	return frames
}

func TestIssue3085_CodexIdleComposerPassesPromptGuard(t *testing.T) {
	for name, pane := range issue3085IdleFrames(t) {
		t.Run(name, func(t *testing.T) {
			guard := &promptGuardTarget{tool: "codex"}
			if err := guard.check(pane, nil); err != nil {
				t.Fatalf("empty idle Codex composer refused: %v", err)
			}
			if err := guard.checkAfterTyping(pane, nil); err != nil {
				t.Fatalf("empty idle Codex composer withheld Enter: %v", err)
			}

			target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{pane}}
			tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0})
			tun.requireInputPrompt = true
			res, err := executeSend(target, "codex", "follow-up probe: reply OK", false, tun)
			if res.delivery == deliveryComposerBlocked || res.delivery == deliveryMenuOpen {
				t.Fatalf("guarded send refused an empty idle composer: delivery=%q err=%v", res.delivery, err)
			}
			if target.sendKeysCalls != 1 {
				t.Fatalf("message not typed into the idle composer: body=%d", target.sendKeysCalls)
			}
		})
	}
}

func TestIssue3085_CodexGuardStillRefusesBusyAndDraft(t *testing.T) {
	for _, name := range []string{"sol-busy", "terra-busy", "terra-draft"} {
		t.Run(name, func(t *testing.T) {
			pane := issue3085Frame(t, name)
			target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{pane}}
			tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0})
			tun.requireInputPrompt = true
			res, err := executeSend(target, "codex", "follow-up probe: reply OK", false, tun)
			if err == nil || res.delivery != deliveryComposerBlocked {
				t.Fatalf("delivery=%q err=%v, want %q", res.delivery, err, deliveryComposerBlocked)
			}
			if target.sendKeysCalls != 0 || target.sendEnterCalls != 0 || target.sendCtrlCCalls != 0 || target.sendChunkedCalls != 0 {
				t.Fatalf("refused pane received keys: body=%d enter=%d ctrlc=%d chunks=%d",
					target.sendKeysCalls, target.sendEnterCalls, target.sendCtrlCCalls, target.sendChunkedCalls)
			}
		})
	}
}

func TestIssue3085_CodexApprovalMenuIsAMenu(t *testing.T) {
	menu := issue3085Frame(t, "terra-approval")
	content := tmux.StripANSI(menu)
	if !session.InteractiveMenuOpen("codex", content) {
		t.Fatal("Codex 0.167 approval picker not recognised as an open menu")
	}

	// Before the first batch: refused as a menu, nothing typed.
	target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{menu}}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0})
	tun.requireInputPrompt = true
	res, err := executeSend(target, "codex", "follow-up probe: reply OK", false, tun)
	if err == nil || res.delivery != deliveryMenuOpen || !res.untyped {
		t.Fatalf("delivery=%q untyped=%v err=%v, want %q before typing", res.delivery, res.untyped, err, deliveryMenuOpen)
	}
	if target.sendKeysCalls != 0 || target.sendEnterCalls != 0 || target.sendCtrlCCalls != 0 || target.sendChunkedCalls != 0 {
		t.Fatalf("approval menu received keys: body=%d enter=%d ctrlc=%d chunks=%d",
			target.sendKeysCalls, target.sendEnterCalls, target.sendCtrlCCalls, target.sendChunkedCalls)
	}

	// Between paste and Enter: a picker that opened after the body was
	// typed must withhold the Enter, which would confirm "Yes, proceed".
	// The post-paste check failing is what withholds it (the mock does not
	// count that Enter), so err and menuOpen are the evidence.
	pane := &mockSendRetryTarget{panes: []string{issue3085Frame(t, "terra-idle"), menu}}
	guard := &promptGuardTarget{sendRetryTarget: pane, tool: "codex"}
	err = guard.SendKeysAndEnterChecked("follow-up probe: reply OK", nil, tmux.PostPasteCheck(func(string, error) (bool, error) { return true, nil }))
	if err == nil || !guard.menuOpen {
		t.Fatalf("Enter not withheld at an approval menu: err=%v menuOpen=%v", err, guard.menuOpen)
	}
	// The body was typed, so this is not a clean refusal: the Codex
	// submission marker must stay ambiguous.
	if delivery, _ := guard.refusal(); delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery=%q after typing, want %q", delivery, deliveryTypedNotSubmitted)
	}
}

// issue3085MenuAfterPaste shows the idle composer until the body is typed,
// then the approval picker: a menu that opened between paste and Enter.
type issue3085MenuAfterPaste struct {
	mockSendRetryTarget
	idle, menu string
}

func (m *issue3085MenuAfterPaste) CapturePaneFresh() (string, error) {
	if m.sendKeysCalls > 0 {
		return m.menu, nil
	}
	return m.idle, nil
}

func (m *issue3085MenuAfterPaste) SendKeysAndEnterChecked(_ string, capture func() (string, error), check tmux.PostPasteCheck) error {
	m.sendKeysCalls++
	pane, capErr := capture()
	if ok, err := check(pane, capErr); !ok {
		return err
	}
	m.sendEnterCalls++
	return nil
}

// A menu after typing is not a clean refusal: the body is in the pane, so
// the result must not claim untyped (which would clear the Codex marker).
func TestIssue3085_CodexMenuAfterTypingStaysAmbiguous(t *testing.T) {
	target := &issue3085MenuAfterPaste{
		mockSendRetryTarget: mockSendRetryTarget{statuses: []string{"waiting"}},
		idle:                issue3085Frame(t, "terra-idle"),
		menu:                issue3085Frame(t, "terra-approval"),
	}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 1, checkDelay: 0})
	tun.requireInputPrompt = true
	res, err := executeSend(target, "codex", "follow-up probe: reply OK", false, tun)
	if err == nil || res.delivery != deliveryTypedNotSubmitted || res.untyped {
		t.Fatalf("delivery=%q untyped=%v err=%v, want %q and not untyped", res.delivery, res.untyped, err, deliveryTypedNotSubmitted)
	}
	if target.sendKeysCalls != 1 || target.sendEnterCalls != 0 {
		t.Fatalf("body=%d enter=%d, want the body typed and Enter withheld", target.sendKeysCalls, target.sendEnterCalls)
	}
}
