package send

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const codexExecApprovalPrompt = `• Running curl https://example.com

  Would you like to run the following command?

  $ curl https://example.com

› 1. Yes, proceed (y)
  2. Yes, and don't ask again for commands that start with ` + "`curl`" + ` (p)
  3. No, and tell Codex what to do differently (esc)
`

type fakeCodexApprovalTarget struct {
	captures []string
	index    int
	sent     []string
	sendErr  error
}

func (f *fakeCodexApprovalTarget) CapturePaneFresh() (string, error) {
	if len(f.captures) == 0 {
		return "", errors.New("no captures configured")
	}
	idx := f.index
	if idx >= len(f.captures) {
		idx = len(f.captures) - 1
	}
	f.index++
	return f.captures[idx], nil
}

func (f *fakeCodexApprovalTarget) SendNamedKey(key string) error {
	f.sent = append(f.sent, key)
	return f.sendErr
}

func TestDetectCodexApprovalPrompt_RequiresLiveSelectedMenu(t *testing.T) {
	if got := detectCodexApprovalPrompt(codexExecApprovalPrompt); got == nil {
		t.Fatal("expected live Codex approval prompt to be detected")
	}

	staleHistoryAndComposer := strings.Replace(codexExecApprovalPrompt, "› 1.", "  1.", 1) +
		"\n› 1\n  gpt-5.4 · /tmp/project\n"
	if got := detectCodexApprovalPrompt(staleHistoryAndComposer); got != nil {
		t.Fatal("stale approval history plus a normal composer must not be detected")
	}
}

func TestApproveCodexPrompt_OnceSendsOneDigitWithoutEnter(t *testing.T) {
	target := &fakeCodexApprovalTarget{
		captures: []string{
			codexExecApprovalPrompt,
			codexExecApprovalPrompt,
			"• Running curl https://example.com\n  press esc to interrupt\n",
		},
	}

	result, err := ApproveCodexPrompt(target, "once", CodexApprovalOptions{
		VerifyTimeout: 50 * time.Millisecond,
		PollInterval:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ApproveCodexPrompt: %v", err)
	}
	if len(target.sent) != 1 || target.sent[0] != "1" {
		t.Fatalf("sent keys = %v, want exactly [1]", target.sent)
	}
	if !result.KeySent || !result.Verified || result.OptionNumber != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestApproveCodexPrompt_AlwaysSelectsDisplayedPersistentOption(t *testing.T) {
	target := &fakeCodexApprovalTarget{
		captures: []string{
			codexExecApprovalPrompt,
			codexExecApprovalPrompt,
			"› Continue with another task\n",
		},
	}

	result, err := ApproveCodexPrompt(target, "always", CodexApprovalOptions{
		VerifyTimeout: 50 * time.Millisecond,
		PollInterval:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ApproveCodexPrompt: %v", err)
	}
	if len(target.sent) != 1 || target.sent[0] != "2" {
		t.Fatalf("sent keys = %v, want exactly [2]", target.sent)
	}
	if result.OptionNumber != 2 {
		t.Fatalf("option number = %d, want 2", result.OptionNumber)
	}
}

func TestApproveCodexPrompt_NumericChoiceMustBeDisplayed(t *testing.T) {
	target := &fakeCodexApprovalTarget{captures: []string{codexExecApprovalPrompt}}

	_, err := ApproveCodexPrompt(target, "4", CodexApprovalOptions{})
	if err == nil || !strings.Contains(err.Error(), "not displayed") {
		t.Fatalf("expected not-displayed error, got %v", err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("must fail before sending; sent %v", target.sent)
	}
}

func TestApproveCodexPrompt_RejectsMultiDigitOption(t *testing.T) {
	prompt := strings.Replace(codexExecApprovalPrompt,
		"  3. No, and tell Codex what to do differently (esc)",
		"  10. No, and tell Codex what to do differently (esc)", 1)
	target := &fakeCodexApprovalTarget{captures: []string{prompt}}

	_, err := ApproveCodexPrompt(target, "10", CodexApprovalOptions{})
	if err == nil || !strings.Contains(err.Error(), "single keypress") {
		t.Fatalf("expected single-keypress error, got %v", err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("multi-digit option must fail before sending; sent %v", target.sent)
	}
}

func TestApproveCodexPrompt_HighlightMovementDoesNotVerify(t *testing.T) {
	moved := strings.Replace(codexExecApprovalPrompt, "› 1.", "  1.", 1)
	moved = strings.Replace(moved, "  2.", "› 2.", 1)
	target := &fakeCodexApprovalTarget{
		captures: []string{codexExecApprovalPrompt, codexExecApprovalPrompt, moved},
	}

	result, err := ApproveCodexPrompt(target, "1", CodexApprovalOptions{
		VerifyTimeout: 2 * time.Millisecond,
		PollInterval:  100 * time.Microsecond,
	})
	if err == nil || !strings.Contains(err.Error(), "did not clear") {
		t.Fatalf("highlight movement must not verify the prompt, got %v", err)
	}
	if !result.KeySent || result.Verified {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestApproveCodexPrompt_FailsClosedWhenPromptChangesBeforeSend(t *testing.T) {
	changed := strings.Replace(
		codexExecApprovalPrompt,
		"curl https://example.com",
		"curl https://openai.com",
		-1,
	)
	target := &fakeCodexApprovalTarget{
		captures: []string{codexExecApprovalPrompt, changed},
	}

	_, err := ApproveCodexPrompt(target, "1", CodexApprovalOptions{})
	if err == nil || !strings.Contains(err.Error(), "changed before") {
		t.Fatalf("expected changed-prompt error, got %v", err)
	}
	if len(target.sent) != 0 {
		t.Fatalf("changed prompt must not receive a key; sent %v", target.sent)
	}
}

func TestApproveCodexPrompt_DoesNotRetryUnverifiedKey(t *testing.T) {
	target := &fakeCodexApprovalTarget{
		captures: []string{codexExecApprovalPrompt},
	}

	result, err := ApproveCodexPrompt(target, "1", CodexApprovalOptions{
		VerifyTimeout: 2 * time.Millisecond,
		PollInterval:  100 * time.Microsecond,
	})
	if err == nil || !strings.Contains(err.Error(), "not retrying automatically") {
		t.Fatalf("expected bounded verification error, got %v", err)
	}
	if len(target.sent) != 1 || target.sent[0] != "1" {
		t.Fatalf("sent keys = %v, want exactly one [1]", target.sent)
	}
	if !result.KeySent || result.Verified {
		t.Fatalf("unexpected result: %+v", result)
	}
}
